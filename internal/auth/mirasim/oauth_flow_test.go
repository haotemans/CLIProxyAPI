package mirasim

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStartBrowserLogin_StartPageAndAuthorize(t *testing.T) {
	fake := newFakeMirasim(t)
	settings := Settings{AdminURL: fake.url()}
	coordinator := NewBrowserCoordinator()

	start, err := coordinator.StartBrowserLogin(context.Background(), settings, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}
	if !strings.HasPrefix(start.StartURL, BrowserStartPath+"?state=") {
		t.Fatalf("start url = %q", start.StartURL)
	}
	if start.State == "" {
		t.Fatal("state empty")
	}
	if start.DefaultProvider != "github" {
		t.Fatalf("default provider = %q", start.DefaultProvider)
	}

	// Start page lists discovered providers with the default marked.
	status, body := coordinator.StartPage(start.State)
	if status != 200 {
		t.Fatalf("start page status = %d", status)
	}
	page := string(body)
	for _, want := range []string{"Continue with GitHub", "Continue with Google", "class=\"button default\"", "token_url"} {
		if !strings.Contains(page, want) {
			t.Fatalf("start page missing %q", want)
		}
	}

	// The authorize route 302s to the provider login URL with redirect_uri bound
	// to this server's loopback callback and the session state.
	status, location, pageBody := coordinator.HandleAuthorize(urlQuery(map[string]string{"state": start.State, "provider": "github"}))
	if status != http.StatusFound || pageBody != nil {
		t.Fatalf("authorize status = %d page=%v", status, pageBody != nil)
	}
	if !strings.Contains(location, fake.url()+"/auth/oauth/github/login") {
		t.Fatalf("authorize location = %q", location)
	}
	if !strings.Contains(location, "redirect_uri=") || !strings.Contains(location, "state="+start.State) {
		t.Fatalf("authorize location missing query: %q", location)
	}

	// An unoffered provider is refused without consuming the login.
	status, _, pageBody = coordinator.HandleAuthorize(urlQuery(map[string]string{"state": start.State, "provider": "microsoft"}))
	if status != 400 || !strings.Contains(string(pageBody), "not available") {
		t.Fatalf("unoffered provider status=%d", status)
	}

	// A loopback-only origin rule mirroring the plugin.
	if _, err := coordinator.StartBrowserLogin(context.Background(), settings, "https://cpa.example.com", ""); err == nil {
		t.Fatal("non-loopback callback origin must be refused")
	}
}

func TestBrowserCallback_LatchAndDrainOnce(t *testing.T) {
	fake := newFakeMirasim(t)
	coordinator := NewBrowserCoordinator()
	start, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}

	callbackValues := urlQuery(map[string]string{
		"state":         start.State,
		"access_token":  "at-1",
		"refresh_token": "rt-1",
	})
	status, body := coordinator.HandleCallback(callbackValues)
	if status != 200 || !strings.Contains(string(body), "sign-in complete") {
		t.Fatalf("callback status=%d body=%s", status, body)
	}

	// Second callback on the same login is conflicted.
	status, _ = coordinator.HandleCallback(callbackValues)
	if status != http.StatusConflict {
		t.Fatalf("reused callback status = %d", status)
	}

	done, message, _ := coordinator.PollTerminalResult(start.State)
	if !done || message != "" {
		t.Fatalf("poll = done=%v message=%q", done, message)
	}
	accessToken, refreshToken, err := coordinator.DrainCredential(start.State)
	if err != nil || accessToken != "at-1" || refreshToken != "rt-1" {
		t.Fatalf("drain = %q %q %v", accessToken, refreshToken, err)
	}
	// Drained credentials cannot be installed twice.
	if _, _, err := coordinator.DrainCredential(start.State); err == nil {
		t.Fatal("second drain must fail")
	}
}

func TestBrowserCallback_PasteFlowAndRejections(t *testing.T) {
	fake := newFakeMirasim(t)
	coordinator := NewBrowserCoordinator()
	start, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}

	// A wrong paste is rebuffed without spending the login.
	badPaste := url.Values{pastedCallbackField: {"https://unrelated.example.com/x?q=1"}}
	status, body := coordinator.HandleCallback(badPaste)
	if status != 400 || !strings.Contains(string(body), "not the Mirasim callback") {
		t.Fatalf("wrong paste status=%d", status)
	}
	// The login is still pending after the bad paste.
	status, body = coordinator.StartPage(start.State)
	if status != 200 {
		t.Fatalf("login consumed by wrong paste: %d", status)
	}

	// A correct paste latches (host ignored per paste rules).
	pasted := "http://127.0.0.1:8317" + BrowserCallbackPath + "?state=" + start.State + "&access_token=at-2&refresh_token=rt-2"
	status, body = coordinator.HandleCallback(url.Values{pastedCallbackField: {pasted}})
	if status != 200 || !strings.Contains(string(body), "sign-in complete") {
		t.Fatalf("paste status=%d body=%s", status, body)
	}
	accessToken, refreshToken, _ := coordinator.DrainCredential(start.State)
	if accessToken != "at-2" || refreshToken != "rt-2" {
		t.Fatalf("pasted credentials = %q %q", accessToken, refreshToken)
	}

	// Rejections: missing tokens + unknown state.
	start2, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin(2): %v", err)
	}
	status, _ = coordinator.HandleCallback(urlQuery(map[string]string{"state": start2.State, "access_token": "only-access"}))
	if status != 400 {
		t.Fatalf("missing refresh status=%d", status)
	}
	status, _ = coordinator.HandleCallback(urlQuery(map[string]string{"state": "not-a-session", "access_token": "a", "refresh_token": "r"}))
	if status != 400 {
		t.Fatalf("unknown state status=%d", status)
	}
}

func TestEmailLoginFlow_sendAndVerify(t *testing.T) {
	fake := newFakeMirasim(t)
	coordinator := NewBrowserCoordinator()
	start, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}

	// Send a code: page renders the code entry form.
	status, body := coordinator.HandleEmailSend(context.Background(), url.Values{"state": {start.State}, emailAddressField: {"u@example.com"}})
	if status != 200 || !strings.Contains(string(body), "token_code") {
		t.Fatalf("email send status=%d body=%.80s", status, body)
	}

	// Wrong code costs an attempt but leaves the login pending.
	status, body = coordinator.HandleEmailVerify(context.Background(), url.Values{"state": {start.State}, emailCodeField: {"999999"}})
	if status != 400 || !strings.Contains(string(body), "not accepted") {
		t.Fatalf("wrong code status=%d body=%.80s", status, body)
	}

	status, body = coordinator.HandleEmailVerify(context.Background(), url.Values{"state": {start.State}, emailCodeField: {fake.EmailCode}})
	if status != 200 || !strings.Contains(string(body), "sign-in complete") {
		t.Fatalf("verify status=%d", status)
	}
	accessToken, refreshToken, err := coordinator.DrainCredential(start.State)
	if err != nil || accessToken == "" || refreshToken != "email-login-refresh-token" {
		t.Fatalf("email credentials = %q %q %v", accessToken, refreshToken, err)
	}

	// Rate limiting surface: a third fresh login can't spam sends beyond the cap.
	start3, _ := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	for i := 0; i < maxEmailCodeSends; i++ {
		status, _ = coordinator.HandleEmailSend(context.Background(), url.Values{"state": {start3.State}, emailAddressField: {"u2@example.com"}})
		if i == 0 && status != 200 {
			t.Fatalf("send %d status=%d", i, status)
		}
	}
	status, _ = coordinator.HandleEmailSend(context.Background(), url.Values{"state": {start3.State}})
	if status != 429 {
		t.Fatalf("capped send status = %d", status)
	}
}

func TestEmailVerify_ExhaustsAndFailsLogin(t *testing.T) {
	fake := newFakeMirasim(t)
	coordinator := NewBrowserCoordinator()
	start, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}
	if status, _ := coordinator.HandleEmailSend(context.Background(), url.Values{"state": {start.State}, emailAddressField: {"u@example.com"}}); status != 200 {
		t.Fatalf("send status=%d", status)
	}
	for i := 0; i < maxEmailVerifyAttempts; i++ {
		status, _ := coordinator.HandleEmailVerify(context.Background(), url.Values{"state": {start.State}, emailCodeField: {"wrong"}})
		if i < maxEmailVerifyAttempts-1 && status != 400 {
			t.Fatalf("attempt %d status=%d", i, status)
		}
	}
	done, message, _ := coordinator.PollTerminalResult(start.State)
	if !done || message == "" {
		t.Fatalf("exhausted login: done=%v message=%q", done, message)
	}
}

func TestBrowserSessionExpiry(t *testing.T) {
	fake := newFakeMirasim(t)
	coordinator := NewBrowserCoordinator()
	start, err := coordinator.StartBrowserLogin(context.Background(), Settings{AdminURL: fake.url()}, "http://127.0.0.1:8317", "")
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}
	// Move the clock beyond the session TTL: the next lookup purges it.
	coordinator.now = func() time.Time { return time.Now().Add(2 * browserLoginTTL) }
	status, _ := coordinator.StartPage(start.State)
	if status == 200 {
		t.Fatal("past-dated session should be expired immediately")
	}
}

func TestFinalizeOAuthStorage_ValidatesLikeThePlugin(t *testing.T) {
	fake := newFakeMirasim(t)
	settings := Settings{AdminURL: fake.url(), RelayURL: fake.url()}

	storage, err := FinalizeOAuthStorage(context.Background(), settings, fakeJWT(3600, nil), "rt-1")
	if err != nil {
		t.Fatalf("FinalizeOAuthStorage: %v", err)
	}
	if fake.SeenModels != 1 {
		t.Fatalf("validation did not read /v1/models: %d", fake.SeenModels)
	}
	if storage.Email != "operator@example.com" || storage.Plan != "pro" {
		t.Fatalf("profile not captured: %+v", storage)
	}
	if storage.AccessToken == "" || storage.RefreshToken != "rt-1" || !ValidDeviceKey([]byte(storage.DevicePrivateKey)) {
		t.Fatalf("storage = %+v", storage)
	}
}

func TestRefreshAccessToken_RotatesAndReschedules(t *testing.T) {
	fake := newFakeMirasim(t)
	settings := Settings{AdminURL: fake.url(), RelayURL: fake.url()}
	storage, err := FinalizeOAuthStorage(context.Background(), settings, fakeJWT(3600, nil), "seed-refresh-token")
	if err != nil {
		t.Fatalf("FinalizeOAuthStorage: %v", err)
	}
	checked := storage.ProfileCheckTime()
	if checked.IsZero() {
		t.Fatal("profile check time missing")
	}

	if errRefresh := RefreshAccessToken(context.Background(), &storage, ""); errRefresh != nil {
		t.Fatalf("RefreshAccessToken: %v", errRefresh)
	}
	if storage.RefreshToken != "rotated-refresh-token" {
		t.Fatalf("refresh token not rotated: %q", storage.RefreshToken)
	}
	if storage.Plan != "pro" {
		t.Fatalf("plan not refreshed: %+v", storage)
	}
	// The next lifecycle entry is the earlier of the token refresh lead and the
	// 5-minute profile cadence; right after a profile check that is the cadence.
	next := NextRefreshAfter(&storage, time.Now())
	if next.Before(time.Now().Add(4*time.Minute)) || next.After(time.Now().Add(6*time.Minute)) {
		t.Fatalf("next refresh = %s, want roughly the 5-minute profile cadence", next)
	}

	// RefreshForSchedule with an old profile check must refresh again.
	storage.ProfileCheckedAt = time.Now().Add(-ProfileRefreshInterval * 2).UTC().Format(time.RFC3339)
	if err := RefreshForSchedule(context.Background(), &storage, "", time.Now().UTC()); err != nil {
		t.Fatalf("RefreshForSchedule: %v", err)
	}
	if storage.RefreshToken != "rotated-refresh-token" {
		t.Fatalf("token unexpectedly changed again: %q", storage.RefreshToken)
	}
	if got := storage.ProfileCheckTime(); got.IsZero() {
		t.Fatal("scheduled profile refresh not recorded")
	}
}

func TestRefreshAccessPacket_ErrorsAreOpaque(t *testing.T) {
	storage := Storage{AccessToken: "a", RefreshToken: "r"}
	if err := RefreshAccessToken(context.Background(), &storage, ""); err == nil {
		t.Fatal("refresh without admin URL must fail")
	}
	if err := RefreshAccessToken(context.Background(), &Storage{}, ""); err == nil {
		t.Fatal("refresh without token must fail")
	}
}

func TestCLISettingsNormalizeAndBuildLoginURL(t *testing.T) {
	settings := Settings{}.Normalize()
	if settings.AdminURL != DefaultAdminURL || settings.RelayURL != DefaultRelayURL || settings.ClientVersion != DefaultClientVersion || settings.LoginProvider != DefaultLoginProvider {
		t.Fatalf("normalize = %+v", settings)
	}
	authURL, err := BuildLoginURL("https://auth.example.com/", "github", "http://127.0.0.1:8317/mirasim/oauth/callback?state=x", "st-1")
	if err != nil {
		t.Fatalf("BuildLoginURL: %v", err)
	}
	if !strings.Contains(authURL, "auth/oauth/github/login") || !strings.Contains(authURL, "redirect_uri=") || !strings.Contains(authURL, "state=st-1") || !strings.Contains(authURL, "mirasim%2Foauth%2Fcallback") {
		t.Fatalf("authURL = %q", authURL)
	}
}

func urlQuery(values map[string]string) map[string][]string {
	out := make(map[string][]string, len(values))
	for k, v := range values {
		out[k] = []string{v}
	}
	return out
}
