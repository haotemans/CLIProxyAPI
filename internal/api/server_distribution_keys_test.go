package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/distribution"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func boolPtr(v bool) *bool { return &v }

func distributionTestRouter(cfg *config.Config, nowFn func() time.Time, principal string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal != "" {
			c.Set("userApiKey", principal)
		}
		c.Next()
	}, distributionKeysMiddleware(func() *config.Config { return cfg }, nowFn))
	router.Any("/v1/*path", handler)
	return router
}

func distributionStatus(t *testing.T, router *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	var reader *strings.Reader
	reader = strings.NewReader(body)
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(rec, req)
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	return rec.Code, payload
}

func TestDistributionKeysMiddlewareMasterAndDisabled(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-master"},
			DistributionKeys: []config.DistributionKey{
				{Key: "dk-enabled", Name: "alice"},
				{Key: "dk-disabled", Enabled: boolPtr(false)},
			},
		},
	}

	okHandler := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	code, _ := distributionStatus(t, distributionTestRouter(cfg, nil, "sk-master", okHandler), http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	if code != http.StatusOK {
		t.Fatalf("master key must pass untouched, got %d", code)
	}

	code, payload := distributionStatus(t, distributionTestRouter(cfg, nil, "dk-enabled", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusOK || payload["ok"] != true {
		t.Fatalf("enabled distribution key must pass: %d %+v", code, payload)
	}

	code, payload = distributionStatus(t, distributionTestRouter(cfg, nil, "dk-disabled", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusForbidden {
		t.Fatalf("disabled key status = %d, want 403", code)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "key_disabled" {
		t.Fatalf("disabled error = %+v", payload)
	}
}

func TestDistributionKeysMiddlewareExpiry(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{DistributionKeys: []config.DistributionKey{
		{Key: "dk-fresh", ExpiresAt: "2026-12-01T00:00:00Z"},
		{Key: "dk-stale", ExpiresAt: "2026-01-01T00:00:00Z"},
	}}}
	// Clock pinned past the stale expiry but before the fresh one.
	nowFn := func() time.Time { return time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) }
	okHandler := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }

	code, _ := distributionStatus(t, distributionTestRouter(cfg, nowFn, "dk-fresh", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusOK {
		t.Fatalf("future expiry must pass, got %d", code)
	}
	code, payload := distributionStatus(t, distributionTestRouter(cfg, nowFn, "dk-stale", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusForbidden {
		t.Fatalf("past expiry must 403, got %d", code)
	}
	if errObj, _ := payload["error"].(map[string]any); errObj["code"] != "key_expired" {
		t.Fatalf("expiry error = %+v", payload)
	}
}

func TestDistributionKeysMiddlewareModelWhitelist(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{DistributionKeys: []config.DistributionKey{
		{Key: "dk-limited-models", AllowedModels: []string{"gpt-5"}},
	}}}
	readBodyHandler := func(c *gin.Context) {
		buf := make([]byte, 256)
		n, _ := c.Request.Body.Read(buf)
		c.JSON(http.StatusOK, gin.H{"body": string(buf[:n])})
	}

	code, payload := distributionStatus(t, distributionTestRouter(cfg, nil, "dk-limited-models", readBodyHandler), http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if code != http.StatusOK {
		t.Fatalf("whitelisted model must pass: %d %+v", code, payload)
	}
	if !strings.Contains(payload["body"].(string), `"gpt-5"`) {
		t.Fatalf("middleware must restore the body for the handler, got %+v", payload)
	}

	code, payload = distributionStatus(t, distributionTestRouter(cfg, nil, "dk-limited-models", readBodyHandler), http.MethodPost, "/v1/chat/completions", `{"model":"claude-sonnet-4-5","messages":[]}`)
	if code != http.StatusForbidden {
		t.Fatalf("non-whitelisted model must 403, got %d", code)
	}
	if errObj, _ := payload["error"].(map[string]any); errObj["code"] != "model_not_allowed" || errObj["param"] != "model" {
		t.Fatalf("whitelist error = %+v", payload)
	}

	// Empty whitelist = every model; model-less requests always pass.
	code, _ = distributionStatus(t, distributionTestRouter(&config.Config{SDKConfig: config.SDKConfig{DistributionKeys: []config.DistributionKey{{Key: "dk-open"}}}}, nil, "dk-open", readBodyHandler), http.MethodPost, "/v1/chat/completions", `{"model":"anything"}`)
	if code != http.StatusOK {
		t.Fatalf("open whitelist must pass: %d", code)
	}
}

func TestDistributionKeysMiddlewareQuotaGate(t *testing.T) {
	t.Cleanup(func() { distribution.SetGlobal(nil) })
	dir := t.TempDir()
	store, err := distribution.NewUsageStore(dir+"/usage.json", nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	distribution.SetGlobal(store)

	cfg := &config.Config{SDKConfig: config.SDKConfig{DistributionKeys: []config.DistributionKey{
		{Key: "dk-broke", QuotaUSD: 1.0},
		{Key: "dk-rich", QuotaUSD: 0},
	}}}
	okHandler := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }

	// Spend 1.25 against the priced default table (gpt-5 = 1.25 in / 10 out).
	store.HandleUsage(t.Context(), coreusage.Record{
		APIKey: "dk-broke",
		Model:  "gpt-5",
		Detail: coreusage.Detail{InputTokens: 500_000, OutputTokens: 25_000},
	})
	if spent := store.Spent(usagestats.MaskKeyLabel("dk-broke")); spent < 0.83 {
		t.Fatalf("fixture spend = %v, want >= 0.83", spent)
	}
	// Artificially top up to ≥ the 1.0 cap via the ledger API.
	store.Add(usagestats.MaskKeyLabel("dk-broke"), 1.0, 0, 0)

	code, payload := distributionStatus(t, distributionTestRouter(cfg, nil, "dk-broke", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusTooManyRequests {
		t.Fatalf("over-quota key must 429, got %d", code)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "quota_exceeded" || errObj["type"] != "insufficient_quota" {
		t.Fatalf("quota error = %+v", payload)
	}

	// No quota configured = unlimited ledger spending never gates.
	code, _ = distributionStatus(t, distributionTestRouter(cfg, nil, "dk-rich", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusOK {
		t.Fatalf("unlimited key must pass: %d", code)
	}

	// After a reset the same key passes again (recoverable quota).
	store.Reset(usagestats.MaskKeyLabel("dk-broke"))
	code, _ = distributionStatus(t, distributionTestRouter(cfg, nil, "dk-broke", okHandler), http.MethodGet, "/v1/models", "")
	if code != http.StatusOK {
		t.Fatalf("post-reset key must pass: %d", code)
	}
}
