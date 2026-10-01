package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

func pricingTestContext(method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	ctx.Request = httptest.NewRequest(method, target, reader)
	ctx.Request.Header.Set("Content-Type", "application/json")
	return rec, ctx
}

func TestGetUsageMetersPricingMergedView(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStats: config.UsageStatsConfig{
				Pricing: map[string]config.UsageStatsPrice{
					"Composer-2": {Input: 1.25, Output: 6},
					"gpt-5":      {Input: 0.5, Output: 5, Source: usagestats.PriceSourceLitellm},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec, ctx := pricingTestContext(http.MethodGet, "/v0/management/usage-meters/pricing", "")
	h.GetUsageMetersPricing(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Entries []struct {
			Model  string  `json:"model"`
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
			Source string  `json:"source"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sources := map[string]string{}
	for _, entry := range payload.Entries {
		sources[entry.Model] = entry.Source
	}
	if sources["composer-2"] != "user" {
		t.Fatalf("composer-2 source = %q (key must be normalized lowercase): %+v", sources["composer-2"], payload.Entries)
	}
	if sources["gpt-5"] != "litellm" {
		t.Fatalf("gpt-5 source = %q, want litellm", sources["gpt-5"])
	}
	if sources["claude-opus-4-5"] != "default" {
		t.Fatalf("embedded default missing: %q", sources["claude-opus-4-5"])
	}
	if len(payload.Entries) < 10 {
		t.Fatalf("expected defaults + overrides, got %d entries", len(payload.Entries))
	}
}

func TestPutAndDeleteUsageMetersPrice(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}

	rec, ctx := pricingTestContext(http.MethodPut, "/v0/management/usage-meters/pricing/gpt-5", `{"input": 2.5, "output": 12.5}`)
	ctx.Params = gin.Params{{Key: "model", Value: "gpt-5"}}
	h.PutUsageMetersPrice(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rec.Code, rec.Body.String())
	}
	entry, ok := h.cfg.UsageStats.Pricing["gpt-5"]
	if !ok || entry.Input != 2.5 || entry.Output != 12.5 {
		t.Fatalf("override not stored: %+v", h.cfg.UsageStats.Pricing)
	}
	if entry.Source != "" {
		t.Fatalf("user override must not carry a litellm source marker: %+v", entry)
	}

	// Reload from disk proves the v8 yaml round-trip persisted the section.
	reloaded, errLoad := config.LoadConfig(h.configFilePath)
	if errLoad != nil {
		t.Fatalf("reload config: %v", errLoad)
	}
	if got := reloaded.UsageStats.Pricing["gpt-5"]; got.Input != 2.5 || got.Output != 12.5 {
		t.Fatalf("persisted entry = %+v", got)
	}

	// Slash-containing model ids arrive via the ?model= query fallback.
	rec, ctx = pricingTestContext(http.MethodPut, "/v0/management/usage-meters/pricing/_?model=deepseek/deepseek-v4", `{"input": 0.27, "output": 1.1}`)
	ctx.Params = gin.Params{{Key: "model", Value: "_"}}
	h.PutUsageMetersPrice(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT query-model status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok = h.cfg.UsageStats.Pricing["deepseek/deepseek-v4"]; !ok {
		t.Fatalf("query override not stored: %+v", h.cfg.UsageStats.Pricing)
	}

	// Invalid bodies are rejected.
	rec, ctx = pricingTestContext(http.MethodPut, "/v0/management/usage-meters/pricing/gpt-5", `{"input": -1, "output": 3}`)
	ctx.Params = gin.Params{{Key: "model", Value: "gpt-5"}}
	h.PutUsageMetersPrice(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative price status = %d, want 400", rec.Code)
	}

	rec, ctx = pricingTestContext(http.MethodDelete, "/v0/management/usage-meters/pricing/gpt-5", "")
	ctx.Params = gin.Params{{Key: "model", Value: "gpt-5"}}
	h.DeleteUsageMetersPrice(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok = h.cfg.UsageStats.Pricing["gpt-5"]; ok {
		t.Fatalf("override still present after delete: %+v", h.cfg.UsageStats.Pricing)
	}

	rec, ctx = pricingTestContext(http.MethodDelete, "/v0/management/usage-meters/pricing/gpt-5", "")
	ctx.Params = gin.Params{{Key: "model", Value: "gpt-5"}}
	h.DeleteUsageMetersPrice(ctx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", rec.Code)
	}
}

func TestSyncUsageMetersPricing(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"gpt-5":       {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001},
			"gpt-5-mini":  {"input_cost_per_token": 0.00000025, "output_cost_per_token": 0.000002},
			"free-model":  {"input_cost_per_token": 0, "output_cost_per_token": 0}
		}`))
	}))
	defer fixture.Close()
	originalURL := usagestats.LiteLLMPricesURL
	usagestats.LiteLLMPricesURL = fixture.URL
	t.Cleanup(func() { usagestats.LiteLLMPricesURL = originalURL })

	h := &Handler{
		cfg: &config.Config{
			UsageStats: config.UsageStatsConfig{
				Pricing: map[string]config.UsageStatsPrice{
					"gpt-5": {Input: 9, Output: 99}, // user override must survive
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec, ctx := pricingTestContext(http.MethodPost, "/v0/management/usage-meters/pricing/sync-litellm", "")
	h.SyncUsageMetersPricing(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Synced         int `json:"synced"`
		Added          int `json:"added"`
		SkippedUser    int `json:"skipped_user"`
		SkippedInvalid int `json:"skipped_invalid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Added != 1 || payload.SkippedUser != 1 || payload.SkippedInvalid != 1 || payload.Synced != 1 {
		t.Fatalf("counts = %+v, want added=1 skipped_user=1 skipped_invalid=1", payload)
	}
	if got := h.cfg.UsageStats.Pricing["gpt-5"]; got.Input != 9 || got.Output != 99 || got.Source != "" {
		t.Fatalf("user override clobbered by sync: %+v", got)
	}
	got := h.cfg.UsageStats.Pricing["gpt-5-mini"]
	if got.Input != 0.25 || got.Output != 2 || got.Source != usagestats.PriceSourceLitellm {
		t.Fatalf("synced entry = %+v, want {0.25 2 litellm}", got)
	}
	if _, exists := h.cfg.UsageStats.Pricing["free-model"]; exists {
		t.Fatal("zero-cost catalog entry must not be synced")
	}

	// A second sync refreshes the litellm entry instead of duplicating it.
	rec, ctx = pricingTestContext(http.MethodPost, "/v0/management/usage-meters/pricing/sync-litellm", "")
	h.SyncUsageMetersPricing(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("second sync status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.UsageStats.Pricing["gpt-5-mini"]; got.Source != usagestats.PriceSourceLitellm || got.Input != 0.25 {
		t.Fatalf("second sync mutated litellm entry: %+v", got)
	}

	// The merged view reports the litellm source for the synced entry.
	rec, ctx = pricingTestContext(http.MethodGet, "/v0/management/usage-meters/pricing", "")
	h.GetUsageMetersPricing(ctx)
	var view struct {
		Entries []struct {
			Model  string `json:"model"`
			Source string `json:"source"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	sources := map[string]string{}
	for _, entry := range view.Entries {
		sources[entry.Model] = entry.Source
	}
	if sources["gpt-5-mini"] != "litellm" || sources["gpt-5"] != "user" {
		t.Fatalf("merged sources wrong: gpt-5-mini=%q gpt-5=%q", sources["gpt-5-mini"], sources["gpt-5"])
	}
}

func TestGetUsageMetersEventsHandler(t *testing.T) {
	recorder := setupUsageMetersRecorder(t)
	restore := usagestats.SetGlobalForTest(recorder)
	defer restore()
	h := &Handler{}

	router := gin.New()
	router.GET("/usage-meters/events", h.GetUsageMetersEvents)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/usage-meters/events?provider=kiro&status=error&page_size=10", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Enabled bool `json:"enabled"`
		Total   int  `json:"total"`
		Rows    []struct {
			Provider  string `json:"provider"`
			AuthFile  string `json:"auth_file"`
			Model     string `json:"model"`
			Status    string `json:"status"`
			ErrorKind string `json:"error"`
		} `json:"rows"`
		PageSize int `json:"page_size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if !payload.Enabled || payload.Total != 1 || len(payload.Rows) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	row := payload.Rows[0]
	if row.Provider != "kiro" || row.AuthFile != "k.json" || row.ErrorKind != "http_429" || row.Status != "error" {
		t.Fatalf("row = %+v", row)
	}

	// Invalid pagination is rejected.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/usage-meters/events?page=-1", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("page=-1 status = %d, want 400", rec.Code)
	}
}
