// Package cursor implements Cursor OAuth PKCE authentication and token refresh.
// Login is a URL-poll flow: the user opens cursor.com/loginDeepControl in any
// browser (no local callback server needed, so it works on headless servers),
// and the CLI polls api2.cursor.sh/auth/poll until the tokens are issued.
package cursor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// CursorLoginURL is the Cursor deep-control login page the user visits.
	CursorLoginURL = "https://cursor.com/loginDeepControl"
	// CursorPollURL returns tokens once the user approved the login.
	CursorPollURL = "https://api2.cursor.sh/auth/poll"
	// CursorRefreshURL exchanges a refresh token for a fresh token pair.
	CursorRefreshURL = "https://api2.cursor.sh/auth/exchange_user_api_key"

	pollMaxAttempts      = 150
	pollBaseDelay        = 1 * time.Second
	pollMaxDelay         = 10 * time.Second
	pollBackoffMultiply  = 1.2
	maxConsecutiveErrors = 10

	// authHTTPTimeout covers poll and refresh round trips to the Cursor auth
	// endpoints; model requests themselves run through the executor.
	authHTTPTimeout = 30 * time.Second
)

// CursorAuth performs the Cursor login polling and refresh-token rotation
// against api2.cursor.sh using a proxy-aware HTTP client.
type CursorAuth struct {
	httpClient *http.Client
}

// NewCursorAuth creates a Cursor auth helper using config proxy settings.
func NewCursorAuth(cfg *config.Config) *CursorAuth {
	return NewCursorAuthWithProxyURL(cfg, "")
}

// NewCursorAuthWithProxyURL creates a Cursor auth helper with an explicit proxy
// URL. proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewCursorAuthWithProxyURL(cfg *config.Config, proxyURL string) *CursorAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &CursorAuth{
		httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: authHTTPTimeout}),
	}
}

// AuthParams holds the PKCE parameters for Cursor login.
type AuthParams struct {
	Verifier  string
	Challenge string
	UUID      string
	LoginURL  string
}

// TokenPair holds the access and refresh tokens from Cursor.
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// GeneratePKCE creates a PKCE verifier and challenge pair.
func GeneratePKCE() (verifier, challenge string, err error) {
	verifierBytes := make([]byte, 96)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("cursor: failed to generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// GenerateAuthParams creates the full set of auth params for Cursor login.
func GenerateAuthParams() (*AuthParams, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}

	uuidBytes := make([]byte, 16)
	if _, err = rand.Read(uuidBytes); err != nil {
		return nil, fmt.Errorf("cursor: failed to generate UUID: %w", err)
	}
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16])

	loginURL := fmt.Sprintf("%s?challenge=%s&uuid=%s&mode=login&redirectTarget=cli",
		CursorLoginURL, challenge, uuid)

	return &AuthParams{
		Verifier:  verifier,
		Challenge: challenge,
		UUID:      uuid,
		LoginURL:  loginURL,
	}, nil
}

// PollForAuth polls the Cursor auth endpoint until the user completes login.
func (a *CursorAuth) PollForAuth(ctx context.Context, uuid, verifier string) (*TokenPair, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := pollBaseDelay
	consecutiveErrors := 0

	for attempt := 0; attempt < pollMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		url := fmt.Sprintf("%s?uuid=%s&verifier=%s", CursorPollURL, uuid, verifier)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("cursor: failed to create poll request: %w", err)
		}

		resp, err := a.httpClient.Do(req)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				return nil, fmt.Errorf("cursor: too many consecutive poll errors (last: %v)", err)
			}
			delay = minDuration(time.Duration(float64(delay)*pollBackoffMultiply), pollMaxDelay)
			continue
		}

		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cursor: close poll response body error: %v", errClose)
		}
		if errRead != nil {
			return nil, fmt.Errorf("cursor: failed to read poll response: %w", errRead)
		}

		if resp.StatusCode == http.StatusNotFound {
			// Still waiting for user to authorize
			consecutiveErrors = 0
			delay = minDuration(time.Duration(float64(delay)*pollBackoffMultiply), pollMaxDelay)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var tokens TokenPair
			if err = json.Unmarshal(body, &tokens); err != nil {
				return nil, fmt.Errorf("cursor: failed to parse auth response: %w", err)
			}
			return &tokens, nil
		}

		return nil, fmt.Errorf("cursor: poll failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("cursor: authentication polling timeout (waited ~%.0f seconds)",
		float64(pollMaxAttempts)*pollMaxDelay.Seconds()/2)
}

// RefreshToken refreshes a Cursor access token using the refresh token.
func (a *CursorAuth) RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("cursor: refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CursorRefreshURL,
		strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("cursor: failed to create refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: token refresh request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cursor: close refresh response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cursor: failed to read refresh response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cursor: token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokens TokenPair
	if err = json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("cursor: failed to parse refresh response: %w", err)
	}

	// Keep original refresh token if not returned
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}

	return &tokens, nil
}

// ParseJWTSub extracts the "sub" claim from a Cursor JWT access token.
// Cursor JWTs contain "sub" like "auth0|user_XXXX" which uniquely identifies
// the account. Returns empty string if parsing fails.
func ParseJWTSub(token string) string {
	decoded := decodeJWTPayload(token)
	if decoded == nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// SubToShortHash converts a JWT sub claim to a short hex hash for use in filenames.
// e.g. "auth0|user_2x..." → "a3f8b2c1"
func SubToShortHash(sub string) string {
	if sub == "" {
		return ""
	}
	h := sha256.Sum256([]byte(sub))
	return fmt.Sprintf("%x", h[:4]) // 8 hex chars
}

// decodeJWTPayload decodes the payload (middle) part of a JWT.
func decodeJWTPayload(token string) []byte {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	return decoded
}

// GetTokenExpiry extracts the JWT expiry from an access token with a 5-minute safety margin.
// Falls back to 1 hour from now if the token can't be parsed.
func GetTokenExpiry(token string) time.Time {
	decoded := decodeJWTPayload(token)
	if decoded == nil {
		return time.Now().Add(1 * time.Hour)
	}

	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp == 0 {
		return time.Now().Add(1 * time.Hour)
	}

	sec, frac := math.Modf(claims.Exp)
	expiry := time.Unix(int64(sec), int64(frac*1e9))
	// Subtract 5-minute safety margin
	return expiry.Add(-5 * time.Minute)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
