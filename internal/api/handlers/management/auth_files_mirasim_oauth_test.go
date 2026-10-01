package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// mirasimOAuthTestServices fakes auth.mirasim.ai (discovery/refresh/me/email)
// and relay.mirasim.ai (device session + signed /v1/models validation).
type mirasimOAuthTestServices struct {
	t *testing.T

	admin *httptest.Server
	relay *httptest.Server

	seenModels     int
	seenRefresh    int
	lastEmail      string
	lastVerifyCode string
}

func newMirasimOAuthTestServices(t *testing.T) *mirasimOAuthTestServices {
	t.Helper()
	services := &mirasimOAuthTestServices{t: t}
	services.admin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/oauth/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []string{"github", "google"}})
		case "/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "operator@example.com", "plan": "pro"})
		case "/auth/refresh":
			services.seenRefresh++
			header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
			claims, _ := json.Marshal(map[string]any{"sub": "mirasim-user-1", "exp": time.Now().Add(2 * time.Hour).Unix(), "plan": "pro"})
			accessToken := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  accessToken,
				"refresh_token": "rotated-refresh-token",
				"expires_in":    3600,
			})
		case "/auth/code":
			var body struct {
				Email string `json:"email"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			services.lastEmail = body.Email
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/auth/verify":
			var body struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Code != "123456" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
			claims, _ := json.Marshal(map[string]any{"sub": "mirasim-email-user", "exp": time.Now().Add(2 * time.Hour).Unix()})
			accessToken := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "refresh_token": "email-login-refresh-token"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(services.admin.Close)

	services.relay = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/device/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "oauth-ticket-1", "expiresIn": 600})
		case "/v1/models":
			services.seenModels++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(services.relay.Close)
	return services
}

func setupMirasimOAuthHandler(t *testing.T, services *mirasimOAuthTestServices) (*Handler, *memoryAuthStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Cleanup(SetMirasimOAuthEndpointsForTest(services.admin.URL, services.relay.URL))
	store := &memoryAuthStore{}
	h := &Handler{
		cfg:            &config.Config{Port: 8317, AuthDir: t.TempDir()},
		configFilePath: writeTestConfigFile(t),
		tokenStore:     store,
	}
	return h, store
}

func TestRequestMirasimToken_StartShape(t *testing.T) {
	services := newMirasimOAuthTestServices(t)
	h, _ := setupMirasimOAuthHandler(t, services)
	_ = h

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mirasim-auth-url", nil)
	h.RequestMirasimToken(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Flow   string `json:"flow"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Status != "ok" || payload.State == "" {
		t.Fatalf("payload = %+v", payload)
	}
	// Plugin-era contract: a root-relative start page URL carrying the state.
	if !strings.HasPrefix(payload.URL, "/mirasim/oauth/start?state=") || !strings.Contains(payload.URL, payload.State) {
		t.Fatalf("start url wrong: %q", payload.URL)
	}
	if payload.Flow != "browser_oauth" {
		t.Fatalf("flow = %q", payload.Flow)
	}

	// The TUI asks for an absolute loopback start URL it can open directly.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mirasim-auth-url?is_webui=true", nil)
	h.RequestMirasimToken(ctx)
	var abs struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &abs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(abs.URL, "http://127.0.0.1:8317/mirasim/oauth/start?state=") {
		t.Fatalf("webui url wrong: %q", abs.URL)
	}
}

func TestMirasimOAuthCallback_EndToEndSave(t *testing.T) {
	services := newMirasimOAuthTestServices(t)
	h, store := setupMirasimOAuthHandler(t, services)

	// Start a browser login through the handler so the session is registered.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mirasim-auth-url", nil)
	h.RequestMirasimToken(ctx)
	var started struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("start unmarshal: %v", err)
	}
	state := started.State

	// Mirasim redirects the browser to the loopback callback route.
	callbackValues := url.Values{
		"state":         {state},
		"access_token":  {"at-callback"},
		"refresh_token": {"rt-callback"},
	}
	status, body := h.mirasimOauthCoordinator().HandleCallback(callbackValues)
	if status != 200 || !strings.Contains(string(body), "sign-in complete") {
		t.Fatalf("callback status=%d", status)
	}

	// The finalizer goroutine drains, validates remotely and saves.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, "mirasim") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Session completed: status endpoint contract is an "ok" terminal state.
	statusResp, completed, ok := getOAuthSessionTerminal(state)
	if !ok || completed != "ok" || statusResp != "ok" {
		t.Fatalf("session terminal = ok=%v state=%s status=%s", ok, completed, statusResp)
	}
	if services.seenModels == 0 {
		t.Fatal("finalize skipped the signed /v1/models validation")
	}
	// The credential file landed in the store with the mirasim oauth identity.
	items, errList := store.List(context.Background())
	if errList != nil || len(items) != 1 {
		t.Fatalf("saved credentials = %d %v", len(items), errList)
	}
	saved := items[0]
	if saved.Provider != "mirasim" {
		t.Fatalf("saved provider = %q", saved.Provider)
	}
	if kind, _ := saved.Metadata["auth_kind"].(string); kind != "oauth" {
		t.Fatalf("saved auth_kind = %v", saved.Metadata["auth_kind"])
	}
	if saved.Metadata["device_private_key"] == nil || saved.Metadata["access_token"] == nil {
		t.Fatalf("saved credential missing oauth fields: %+v", saved.Metadata)
	}
}

// getOAuthSessionTerminal mirrors the panel's getAuthStatus poll semantics.
func getOAuthSessionTerminal(state string) (string, string, bool) {
	provider, status, _, _, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		return "", "", false
	}
	statusText := "pending"
	if completed {
		statusText = "ok"
	} else if status != "" {
		statusText = status
	}
	if provider != "mirasim" {
		return statusText, statusText + "-wrong-provider", false
	}
	return statusText, "ok", true
}

func TestMirasimOAuthEmailCode_EndToEndSave(t *testing.T) {
	services := newMirasimOAuthTestServices(t)
	h, store := setupMirasimOAuthHandler(t, services)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mirasim-auth-url", nil)
	h.RequestMirasimToken(ctx)
	var started struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("start unmarshal: %v", err)
	}
	coordinator := h.mirasimOauthCoordinator()

	status, body := coordinator.HandleEmailSend(context.Background(), url.Values{"state": {started.State}, "token_email": {"operator@example.com"}})
	if status != 200 || !strings.Contains(string(body), "token_code") {
		t.Fatalf("email send status=%d body=%.100s", status, body)
	}
	if services.lastEmail != "operator@example.com" {
		t.Fatalf("auth service email = %q", services.lastEmail)
	}
	status, body = coordinator.HandleEmailVerify(context.Background(), url.Values{"state": {started.State}, "token_code": {"123456"}})
	if status != 200 || !strings.Contains(string(body), "sign-in complete") {
		t.Fatalf("verify status=%d body=%.100s", status, body)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(started.State, "mirasim") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if services.seenModels == 0 {
		t.Fatal("email login finalize skipped the signed validation")
	}
	items, errList := store.List(context.Background())
	if errList != nil || len(items) != 1 {
		t.Fatalf("saved credentials = %d %v", len(items), errList)
	}
	if refresh, _ := items[0].Metadata["refresh_token"].(string); refresh != "email-login-refresh-token" {
		t.Fatalf("saved refresh token = %v", items[0].Metadata["refresh_token"])
	}
}
