package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func registerInspectionAuth(t *testing.T, mgr *coreauth.Manager, auth *coreauth.Auth) {
	t.Helper()
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("register %s: %v", auth.ID, err)
	}
}

func setupInspectionHandler(t *testing.T) *Handler {
	t.Helper()
	mgr := coreauth.NewManager(nil, nil, nil)

	// Healthy codex credential with 24h traffic (seeded via usagestats below).
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "codex-good.json", FileName: "codex-good.json", Provider: "codex",
		Status: coreauth.StatusActive, Attributes: map[string]string{"path": "/auths/codex-good.json"},
	})

	// Credential with a terminal unauthorized failure → bad + relogin.
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "codex-expired.json", FileName: "codex-expired.json", Provider: "codex",
		Status: coreauth.StatusError, Unavailable: true,
		Attributes:      map[string]string{"path": "/auths/codex-expired.json"},
		LastError:       &coreauth.Error{HTTPStatus: 401, Message: "token expired"},
		LastRefreshedAt: time.Now().Add(-48 * time.Hour),
	})

	// Credential sitting disabled for weeks → warn + delete.
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "claude-disabled.json", FileName: "claude-disabled.json", Provider: "claude",
		Status: coreauth.StatusDisabled, Disabled: true,
		Attributes: map[string]string{"path": "/auths/claude-disabled.json"},
		UpdatedAt:  time.Now().Add(-30 * 24 * time.Hour),
	})

	// Quota-exhausted claude credential → warn + rotate.
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "claude-quota.json", FileName: "claude-quota.json", Provider: "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/auths/claude-quota.json"},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "usage limit reached",
			NextRecoverAt: time.Now().Add(2 * time.Hour),
		},
	})

	// Probe-flagged auth error → bad + relogin even without traffic.
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "codex-probe.json", FileName: "codex-probe.json", Provider: "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/auths/codex-probe.json"},
		Metadata: map[string]any{
			modelprobe.MetadataKey: map[string]any{
				"checked_at": time.Now().UTC().Format(time.RFC3339),
				"models": map[string]any{
					"gpt-5": map[string]any{"status": "auth_error", "error": "401 unauthorized"},
				},
			},
		},
	})

	// Config API-key credential (no path attribute) must be skipped.
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "config-key-1", Provider: "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "sk-test", "auth_kind": "apikey"},
	})

	return &Handler{authManager: mgr}
}

func seedInspectionEvents(t *testing.T, r *usagestats.Recorder) {
	t.Helper()
	base := time.Now().UnixMilli()
	// Healthy credential: mostly success.
	r.Record(usagestats.Event{TimestampMS: base - 100, Provider: "codex", AuthFile: "codex-good.json", Model: "gpt-5", Status: "ok"})
	// codex-minimal.json: auth errors dominant (3 of 4, 401-led).
	r.Record(usagestats.Event{TimestampMS: base - 90, Provider: "codex", AuthFile: "codex-minimal.json", Model: "gpt-5", Status: "error", ErrorKind: "http_401"})
	r.Record(usagestats.Event{TimestampMS: base - 80, Provider: "codex", AuthFile: "codex-minimal.json", Model: "gpt-5", Status: "error", ErrorKind: "http_401"})
	r.Record(usagestats.Event{TimestampMS: base - 70, Provider: "codex", AuthFile: "codex-minimal.json", Model: "gpt-5", Status: "error", ErrorKind: "http_500"})
	r.Record(usagestats.Event{TimestampMS: base - 60, Provider: "codex", AuthFile: "codex-minimal.json", Model: "gpt-5", Status: "ok"})
	// claude-quota.json: some requests, mostly fine (quota signal from state).
	r.Record(usagestats.Event{TimestampMS: base - 50, Provider: "claude", AuthFile: "claude-quota.json", Model: "claude-sonnet-4-5", Status: "ok"})
	r.Flush()
}

func TestGetPoolInspection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder, err := usagestats.Open(usagestats.Config{Path: t.TempDir() + "/insp.db"})
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	seedInspectionEvents(t, recorder)
	restore := usagestats.SetGlobalForTest(recorder)
	defer restore()

	h := setupInspectionHandler(t)
	// Credential whose only problem is 24h auth-error dominance.
	registerInspectionAuth(t, h.authManager, &coreauth.Auth{
		ID: "codex-minimal.json", FileName: "codex-minimal.json", Provider: "codex",
		Status: coreauth.StatusActive, Attributes: map[string]string{"path": "/auths/codex-minimal.json"},
	})

	router := gin.New()
	router.GET("/pool-inspection", h.GetPoolInspection)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pool-inspection", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Credentials []struct {
			AuthFile    string   `json:"auth_file"`
			Provider    string   `json:"provider"`
			Health      string   `json:"health"`
			Suggestions []string `json:"suggestions"`
			Signals     struct {
				Requests24H    int64  `json:"requests_24h"`
				Errors24H      int64  `json:"errors_24h"`
				DominantError  string `json:"dominant_error"`
				QuotaExceeded  bool   `json:"quota_exceeded"`
				ProbeAuthError bool   `json:"probe_auth_error"`
				Unauthorized   bool   `json:"unauthorized"`
			} `json:"signals"`
		} `json:"credentials"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}

	byFile := map[string]int{}
	byHealth := map[string]string{}
	bySuggestions := map[string][]string{}
	var signalsByFile = map[string]struct {
		Requests24H    int64  `json:"requests_24h"`
		Errors24H      int64  `json:"errors_24h"`
		DominantError  string `json:"dominant_error"`
		QuotaExceeded  bool   `json:"quota_exceeded"`
		ProbeAuthError bool   `json:"probe_auth_error"`
		Unauthorized   bool   `json:"unauthorized"`
	}{}
	for i, cred := range payload.Credentials {
		byFile[cred.AuthFile] = i
		byHealth[cred.AuthFile] = cred.Health
		bySuggestions[cred.AuthFile] = cred.Suggestions
		signalsByFile[cred.AuthFile] = cred.Signals
	}

	// Six file-backed credentials; the config API key is excluded.
	if len(payload.Credentials) != 6 {
		t.Fatalf("credentials = %d, want 6 (config api key excluded): %+v", len(payload.Credentials), payload.Credentials)
	}
	if _, ok := byFile["config-key-1"]; ok {
		t.Fatal("config api-key credential must not be inspected")
	}

	assertHealth := func(file, health string, suggestions ...string) {
		t.Helper()
		if byHealth[file] != health {
			t.Errorf("%s health = %q, want %q", file, byHealth[file], health)
		}
		got := bySuggestions[file]
		if len(got) != len(suggestions) {
			t.Errorf("%s suggestions = %v, want %v", file, got, suggestions)
			return
		}
		for i, code := range suggestions {
			if got[i] != code {
				t.Errorf("%s suggestions = %v, want %v", file, got, suggestions)
				return
			}
		}
	}
	assertHealth("codex-good.json", "good")
	assertHealth("codex-expired.json", "bad", "relogin")
	assertHealth("codex-probe.json", "bad", "relogin")
	assertHealth("codex-minimal.json", "bad", "relogin")
	assertHealth("claude-disabled.json", "warn", "delete")
	assertHealth("claude-quota.json", "warn", "rotate")

	if summary := payload.Summary; summary["good"] != 1 || summary["warn"] != 2 || summary["bad"] != 3 {
		t.Fatalf("summary = %+v, want good=1 warn=2 bad=3", summary)
	}

	// The 24h error-dominance signal comes from the usage archive.
	sig := signalsByFile["codex-minimal.json"]
	if sig.Requests24H != 4 || sig.Errors24H != 3 || sig.DominantError != "http_401" {
		t.Fatalf("codex-minimal signals = %+v", sig)
	}
	if !signalsByFile["claude-quota.json"].QuotaExceeded {
		t.Fatal("claude-quota quota_exceeded signal missing")
	}
	if !signalsByFile["codex-probe.json"].ProbeAuthError {
		t.Fatal("codex-probe probe_auth_error signal missing")
	}
	if !signalsByFile["codex-expired.json"].Unauthorized {
		t.Fatal("codex-expired unauthorized signal missing")
	}

	// Bad credentials sort before warn and good.
	if payload.Credentials[0].Health != "bad" || payload.Credentials[len(payload.Credentials)-1].Health != "good" {
		t.Fatalf("sorting wrong: first=%s last=%s",
			payload.Credentials[0].Health, payload.Credentials[len(payload.Credentials)-1].Health)
	}

	// Provider filter narrows the listing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/pool-inspection?provider=claude", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered status = %d", rec.Code)
	}
	var filtered struct {
		Credentials []struct {
			Provider string `json:"provider"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("unmarshal filtered: %v", err)
	}
	if len(filtered.Credentials) != 2 {
		t.Fatalf("provider=claude → %d credentials, want 2", len(filtered.Credentials))
	}
	for _, cred := range filtered.Credentials {
		if cred.Provider != "claude" {
			t.Fatalf("provider filter leaked %q", cred.Provider)
		}
	}
}

func TestGetPoolInspection_ProviderBlockedSuggestsWaitNotRelogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restore := usagestats.SetGlobalForTest(nil)
	defer restore()

	mgr := coreauth.NewManager(nil, nil, nil)
	blockedSection := map[string]any{
		"checked_at":   time.Now().UTC().Format(time.RFC3339),
		"catalog_size": 3,
		"models": map[string]any{
			"a": map[string]any{"status": "provider_blocked"},
			"b": map[string]any{"status": "provider_blocked"},
		},
	}
	registerInspectionAuth(t, mgr, &coreauth.Auth{
		ID: "cline-blocked.json", FileName: "cline-blocked.json", Provider: "cline",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/auths/cline-blocked.json"},
		Metadata: map[string]any{
			modelprobe.MetadataKey: blockedSection,
		},
	})
	authErr := &coreauth.Auth{
		ID: "cline-autherr.json", FileName: "cline-autherr.json", Provider: "cline",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/auths/cline-autherr.json"},
		Metadata: map[string]any{
			modelprobe.MetadataKey: map[string]any{
				"checked_at": time.Now().UTC().Format(time.RFC3339),
				"models": map[string]any{
					"a": map[string]any{"status": "auth_error"},
				},
			},
		},
	}
	registerInspectionAuth(t, mgr, authErr)

	h := &Handler{authManager: mgr}
	router := gin.New()
	router.GET("/pool-inspection", h.GetPoolInspection)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pool-inspection?provider=cline", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Credentials []struct {
			AuthFile    string   `json:"auth_file"`
			Health      string   `json:"health"`
			Suggestions []string `json:"suggestions"`
			Signals     struct {
				ProbeBlocked   bool `json:"probe_blocked"`
				ProbeAuthError bool `json:"probe_auth_error"`
			} `json:"signals"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Credentials) != 2 {
		t.Fatalf("credentials = %d, want 2", len(payload.Credentials))
	}
	byFile := map[string]struct {
		Health      string
		Suggestions []string
		Blocked     bool
	}{}
	for _, cred := range payload.Credentials {
		byFile[cred.AuthFile] = struct {
			Health      string
			Suggestions []string
			Blocked     bool
		}{cred.Health, cred.Suggestions, cred.Signals.ProbeBlocked}
	}
	blocked := byFile["cline-blocked.json"]
	if blocked.Health != "warn" {
		t.Fatalf("blocked credential health = %q, want warn (not bad, not good)", blocked.Health)
	}
	if len(blocked.Suggestions) != 1 || blocked.Suggestions[0] != "wait-provider" {
		t.Fatalf("blocked suggestions = %v, want [wait-provider]", blocked.Suggestions)
	}
	if !blocked.Blocked {
		t.Fatal("probe_blocked signal missing")
	}
	authErrFile := byFile["cline-autherr.json"]
	if len(authErrFile.Suggestions) != 1 || authErrFile.Suggestions[0] != "relogin" {
		t.Fatalf("auth_error suggestions = %v, want [relogin]", authErrFile.Suggestions)
	}
	if authErrFile.Health != "warn" && authErrFile.Health != "bad" {
		t.Fatalf("auth_error credential health = %q", authErrFile.Health)
	}
}
