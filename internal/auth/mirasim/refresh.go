// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): authentication-service
// calls (token refresh, account profile, email sign-in, provider discovery).
package mirasim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

const (
	// ProfileRefreshInterval is how often the account profile is re-read
	// (plan changes surface without per-request polling).
	ProfileRefreshInterval = 5 * time.Minute

	refreshTimeout   = 60 * time.Second
	profileTimeout   = 5 * time.Second
	emailTimeout     = 20 * time.Second
	discoveryTimeout = 5 * time.Second

	maxAdminResponse = 64 << 10
	maxAdminDetail   = 512

	emailCodeResource   = "/auth/code"
	emailVerifyResource = "/auth/verify"
	providersResource   = "/auth/oauth/providers"
)

var (
	providerSlug    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	emailAddressRe  = regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`)
	errMissingToken = fmt.Errorf("Mirasim refresh token is missing")
)

// newAuthHTTPClient builds the private client used for auth-service calls.
// The request body (refresh token, email code) must stay out of the host
// request logger, so these always use a private proxy-aware transport.
func newAuthHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, func(), error) {
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, nil, fmt.Errorf("configure Mirasim auth proxy: %w", err)
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	closeClient := func() {}
	if transport != nil {
		client.Transport = transport
		closeClient = transport.CloseIdleConnections
	}
	return client, closeClient, nil
}

func adminEndpointURL(adminURL, resource string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(adminURL), "/")
	if base == "" {
		return "", fmt.Errorf("invalid Mirasim authentication service URL")
	}
	return base + resource, nil
}

// postAdminJSON posts a JSON body to the authentication service through the
// private client and returns the raw response body on HTTP 2xx.
func postAdminJSON(ctx context.Context, adminURL, proxyURL, resource string, body map[string]string, timeout time.Duration) ([]byte, error) {
	endpoint, err := adminEndpointURL(adminURL, resource)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client, closeClient, err := newAuthHTTPClient(proxyURL, timeout)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("create Mirasim authentication service request")
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Mirasim authentication service is unreachable; retry sign-in")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminResponse+1))
	if err != nil || len(raw) > maxAdminResponse {
		return nil, fmt.Errorf("invalid Mirasim authentication service response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if detail := adminErrorDetail(raw); detail != "" {
			return nil, fmt.Errorf("Mirasim authentication service returned: %s", detail)
		}
		return nil, fmt.Errorf("Mirasim authentication service returned HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// adminErrorDetail mirrors the official client, which shows the service's own
// detail string when it has one.
func adminErrorDetail(raw []byte) string {
	var payload struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	detail := strings.TrimSpace(payload.Detail)
	if len(detail) > maxAdminDetail {
		detail = detail[:maxAdminDetail]
	}
	for _, char := range detail {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return detail
}

// RefreshTokenResponse is the /auth/refresh payload. The refresh token
// rotates on every refresh; a missing one keeps the issued value.
type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// RefreshAccessToken rotates the mirasim access token. It persists new plan
// claims and timing into storage.
func RefreshAccessToken(ctx context.Context, storage *Storage, proxyURL string) error {
	if storage == nil {
		return errMissingToken
	}
	refreshToken := strings.TrimSpace(storage.RefreshToken)
	if refreshToken == "" {
		return errMissingToken
	}
	raw, err := postAdminJSON(ctx, storage.AdminURL, proxyURL, "/auth/refresh", map[string]string{"refresh_token": refreshToken}, refreshTimeout)
	if err != nil {
		return err
	}
	var payload RefreshTokenResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode Mirasim token refresh response")
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	if payload.AccessToken == "" {
		return fmt.Errorf("Mirasim token refresh returned no access token")
	}
	now := time.Now().UTC()
	storage.AccessToken = payload.AccessToken
	storage.PopulateIdentityFromAccessToken()
	if plan, planExpiresAt := AccessTokenPlan(payload.AccessToken); strings.TrimSpace(plan) != "" {
		storage.Plan = plan
		storage.PlanExpiresAt = planExpiresAt
	}
	storage.RecordTokenTiming(payload.AccessToken, payload.ExpiresIn, now)
	if payload.RefreshToken != "" {
		storage.RefreshToken = payload.RefreshToken
	}
	return nil
}

// AccountProfile is the /auth/me payload (non-secret account state).
type AccountProfile struct {
	Email           string
	Plan            string
	PlanExpiresAt   *int64
	PlanExpiryKnown bool
}

// FetchAccountProfile reads the authenticated account profile.
func FetchAccountProfile(ctx context.Context, adminURL, accessToken, proxyURL string) (AccountProfile, error) {
	endpoint, err := adminEndpointURL(adminURL, "/auth/me")
	if err != nil {
		return AccountProfile{}, err
	}
	client, closeClient, err := newAuthHTTPClient(proxyURL, profileTimeout)
	if err != nil {
		return AccountProfile{}, err
	}
	defer closeClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AccountProfile{}, fmt.Errorf("create Mirasim profile request")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(request)
	if err != nil {
		return AccountProfile{}, fmt.Errorf("read Mirasim account profile: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminResponse+1))
	if err != nil || len(body) > maxAdminResponse {
		return AccountProfile{}, fmt.Errorf("invalid Mirasim account profile response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AccountProfile{}, fmt.Errorf("Mirasim account profile returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Email   string          `json:"email"`
		Plan    string          `json:"plan"`
		PlanExp json.RawMessage `json:"plan_exp"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return AccountProfile{}, fmt.Errorf("decode Mirasim account profile: %w", err)
	}
	profile := AccountProfile{
		Email: safeProfileValue(payload.Email, 320),
		Plan:  safeProfileValue(payload.Plan, 128),
	}
	if len(payload.PlanExp) > 0 {
		profile.PlanExpiryKnown = true
		if trimmed := bytes.TrimSpace(payload.PlanExp); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(payload.PlanExp))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err == nil {
				if number, ok := value.(json.Number); ok {
					if expiresAt, err := number.Int64(); err == nil && expiresAt > 0 {
						profile.PlanExpiresAt = &expiresAt
					}
				}
			}
		}
	}
	return profile, nil
}

func safeProfileValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

// RequestEmailCode asks Mirasim to mail a sign-in code. A development build
// of the service echoes the code back; that is deliberately not surfaced.
func RequestEmailCode(ctx context.Context, adminURL, proxyURL, email string) error {
	_, err := postAdminJSON(ctx, adminURL, proxyURL, emailCodeResource, map[string]string{"email": email}, emailTimeout)
	return err
}

// VerifyEmailCode exchanges a mailed code for renewable credentials. A
// response without a refresh token is refused rather than saved: CPA cannot
// keep such a credential alive past the access token's own expiry.
func VerifyEmailCode(ctx context.Context, adminURL, proxyURL, email, code string) (string, string, error) {
	raw, err := postAdminJSON(ctx, adminURL, proxyURL, emailVerifyResource, map[string]string{"email": email, "code": code}, emailTimeout)
	if err != nil {
		return "", "", err
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", fmt.Errorf("invalid Mirasim email sign-in response")
	}
	accessToken := strings.TrimSpace(payload.AccessToken)
	refreshToken := strings.TrimSpace(payload.RefreshToken)
	if accessToken == "" {
		return "", "", fmt.Errorf("Mirasim email sign-in returned no access token")
	}
	if refreshToken == "" {
		return "", "", fmt.Errorf("Mirasim email sign-in returned no renewable credential")
	}
	return accessToken, refreshToken, nil
}

// LoginProvider is one offered sign-in method from discovery.
type LoginProvider struct{ ID, Label string }

// DiscoverLoginProviders lists the sign-in methods mirasim currently offers.
// Discovery is public and unauthenticated.
func DiscoverLoginProviders(ctx context.Context, adminURL, proxyURL string) ([]LoginProvider, error) {
	endpoint, err := adminEndpointURL(adminURL, providersResource)
	if err != nil {
		return nil, err
	}
	client, closeClient, err := newAuthHTTPClient(proxyURL, discoveryTimeout)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid OAuth discovery URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Mirasim sign-in providers are unreachable; retry login")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mirasim sign-in provider discovery returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, fmt.Errorf("invalid Mirasim sign-in provider response")
	}
	var payload struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("invalid Mirasim sign-in provider response")
	}
	providers := []LoginProvider{}
	seen := map[string]bool{}
	for _, id := range payload.Providers {
		id = strings.ToLower(strings.TrimSpace(id))
		if !providerSlug.MatchString(id) || seen[id] {
			continue
		}
		label := id
		switch id {
		case "github":
			label = "GitHub"
		case "google":
			label = "Google"
		}
		providers = append(providers, LoginProvider{ID: id, Label: label})
		seen[id] = true
	}
	return providers, nil
}

// ProviderOffered reports whether the discovered set contains id.
func ProviderOffered(providers []LoginProvider, id string) bool {
	for _, p := range providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// ResolveLoginProvider takes the first named candidate in precedence order
// and falls back to DefaultLoginProvider when nothing is named.
func ResolveLoginProvider(candidates ...string) (string, error) {
	for _, candidate := range append(candidates, DefaultLoginProvider) {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if !providerSlug.MatchString(candidate) {
			return "", fmt.Errorf("invalid Mirasim sign-in provider")
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no Mirasim sign-in provider configured")
}

// BuildLoginURL renders the authorize URL the browser is sent to for one
// provider. Mirasim drops the state parameter of this login URL but keeps the
// redirect_uri query and appends the tokens to it, so the state must travel
// inside the callback address to come back at all.
func BuildLoginURL(adminURL, provider, callbackURL, state string) (string, error) {
	if !providerSlug.MatchString(provider) {
		return "", fmt.Errorf("invalid Mirasim sign-in provider")
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(adminURL), "/") + "/auth/oauth/" + url.PathEscape(provider) + "/login")
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid Mirasim authentication service URL")
	}
	query := base.Query()
	query.Set("redirect_uri", callbackURL)
	query.Set("state", state)
	base.RawQuery = query.Encode()
	return base.String(), nil
}

// normalizeLoginEmail validates the account address for email sign-in.
func normalizeLoginEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 254 || !emailAddressRe.MatchString(value) {
		return "", fmt.Errorf("invalid Mirasim account email address")
	}
	return value, nil
}

// normalizeLoginCode validates the mailed sign-in code.
func normalizeLoginCode(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid Mirasim sign-in code")
	}
	return value, nil
}
