package mirasim

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeMirasimServer stands in for auth.mirasim.ai (OAuth/auth service) plus
// relay.mirasim.ai (device ticket + signed control calls).
type fakeMirasimServer struct {
	t *testing.T

	// Providers lists the discovery answer (default github+google).
	Providers []string
	// EmailCode is the code /auth/verify accepts for any address (default 123456).
	EmailCode string

	// TicketIssued is the device-session answer (default ticket-test/600s). Set
	// TicketStatus to 404/501 to simulate a relay without device sessions.
	TicketStatus int
	Ticket       string

	// SeenSignRequests counts relay control calls; LastSignHeaders captures the
	// last control call's headers.
	SeenLimits   int
	SeenMint     int
	SeenModels   int
	LastMintAuth string
	LastLimits   http.Header

	server *httptest.Server
}

func newFakeMirasim(t *testing.T) *fakeMirasimServer {
	t.Helper()
	fake := &fakeMirasimServer{
		t:         t,
		Providers: []string{"github", "google"},
		EmailCode: "123456",
		Ticket:    "ticket-test",
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeMirasimServer) url() string { return f.server.URL }

func (f *fakeMirasimServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/auth/oauth/providers":
		writeJSON(w, map[string]any{"providers": f.Providers})
	case "/auth/refresh":
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.RefreshToken) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{
			"access_token":  fakeJWT(900, map[string]any{"plan": "pro"}),
			"refresh_token": "rotated-refresh-token",
			"expires_in":    3600,
		})
	case "/auth/me":
		planExp := time.Now().Add(720 * time.Hour).Unix()
		writeJSON(w, map[string]any{"email": "operator@example.com", "plan": "pro", "plan_exp": planExp})
	case "/auth/code":
		var body struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !emailAddressRe.MatchString(strings.TrimSpace(body.Email)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case "/auth/verify":
		var body struct {
			Email string `json:"email"`
			Code  string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Code) != f.EmailCode {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid code"})
			return
		}
		writeJSON(w, map[string]any{
			"access_token":  fakeJWT(3600, nil),
			"refresh_token": "email-login-refresh-token",
		})
	case "/v1/device/session":
		f.SeenMint++
		f.LastMintAuth = r.Header.Get("Authorization")
		if f.TicketStatus != 0 && f.TicketStatus != 200 {
			w.WriteHeader(f.TicketStatus)
			return
		}
		writeJSON(w, map[string]any{"ticket": f.Ticket, "expiresIn": 600})
	case "/v1/models":
		f.SeenModels++
		writeJSON(w, map[string]any{"data": []any{}})
	case "/v1/limits":
		f.SeenLimits++
		f.LastLimits = r.Header.Clone()
		writeJSON(w, map[string]any{"plan": "pro", "limit": 100.0, "used": 25.0, "next_reset": "tomorrow"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// fakeJWT renders an unsigned test JWT carrying exp=+seconds and the extra claims.
func fakeJWT(expiresInSeconds int64, extra map[string]any) string {
	claims := map[string]any{
		"sub": "mirasim-user-1",
		"exp": time.Now().Unix() + expiresInSeconds,
	}
	for k, v := range extra {
		claims[k] = v
	}
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
}

// newTestStorage builds a complete in-memory credential against the fake server.
func newTestStorage(t *testing.T, fake *fakeMirasimServer) Storage {
	t.Helper()
	storage, err := InstallOAuth(NewStorage(Settings{AdminURL: fake.url(), RelayURL: fake.url()}), fakeJWT(3600, nil), "seed-refresh-token")
	if err != nil {
		t.Fatalf("InstallOAuth: %v", err)
	}
	return storage
}
