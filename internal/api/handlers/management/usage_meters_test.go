package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type usageStatsRow struct {
	Key             string  `json:"key"`
	Requests        int64   `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	UpstreamCostUSD float64 `json:"upstream_cost_usd"`
	ComputedCostUSD float64 `json:"computed_cost_usd"`
}

type usageStatsBucket struct {
	Bucket   string  `json:"bucket"`
	Key      string  `json:"key"`
	Requests int64   `json:"requests"`
	CostUSD  float64 `json:"-"` // covered via computed/upstream
}

type summaryPayload struct {
	Enabled bool            `json:"enabled"`
	Rows    []usageStatsRow `json:"rows"`
	Totals  usageStatsRow   `json:"totals"`
	GroupBy string          `json:"group_by"`
	Dropped int64           `json:"dropped"`
}

type seriesPayload struct {
	Enabled  bool               `json:"enabled"`
	Rows     []usageStatsBucket `json:"rows"`
	Interval string             `json:"interval"`
	GroupBy  string             `json:"group_by"`
}

func setupUsageMetersRecorder(t *testing.T) *usagestats.Recorder {
	t.Helper()
	r, err := usagestats.Open(usagestats.Config{
		Path:   filepath.Join(t.TempDir(), "stats.db"),
		Pricer: usagestats.NewPricer(nil),
	})
	if err != nil {
		t.Fatalf("Open recorder: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	rec := usagestats.Event{TimestampMS: now - 500, Provider: "claude", AuthFile: "a.json", Model: "claude-sonnet-4-5", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Status: "ok", LatencyMS: 120}
	r.Record(rec)
	r.Record(usagestats.Event{TimestampMS: now - 400, Provider: "kiro", AuthFile: "k.json", Model: "kiro-auto", InputTokens: 200, OutputTokens: 20, TotalTokens: 220, UpstreamCostUSD: 0.005, Status: "error", ErrorKind: "http_429"})
	r.Flush() // deterministic drain, no sleep-based waits in tests
	return r
}

func TestUsageMetersSummaryAndSeries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := setupUsageMetersRecorder(t)
	restore := usagestats.SetGlobalForTest(recorder)
	defer restore()
	h := &Handler{}

	router := gin.New()
	router.GET("/usage-meters/summary", h.GetUsageMetersSummary)
	router.GET("/usage-meters/series", h.GetUsageMetersSeries)

	summaryReq := httptest.NewRequest(http.MethodGet, "/usage-meters/summary?group_by=provider", nil)
	summaryRec := httptest.NewRecorder()
	router.ServeHTTP(summaryRec, summaryReq)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("summary status = %d body=%s", summaryRec.Code, summaryRec.Body.String())
	}
	var summary summaryPayload
	if err := json.Unmarshal(summaryRec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("summary unmarshal: %v body=%s", err, summaryRec.Body.String())
	}
	if !summary.Enabled {
		t.Fatal("enabled should be true when recorder active")
	}
	if len(summary.Rows) != 2 {
		t.Fatalf("summary rows = %d, want 2: %+v", len(summary.Rows), summary.Rows)
	}
	if summary.Totals.Requests != 2 || summary.Totals.InputTokens != 300 {
		t.Fatalf("totals = %+v", summary.Totals)
	}

	byModelReq := httptest.NewRequest(http.MethodGet, "/usage-meters/summary?group_by=model", nil)
	byModelRec := httptest.NewRecorder()
	router.ServeHTTP(byModelRec, byModelReq)
	if byModelRec.Code != http.StatusOK {
		t.Fatalf("by-model status = %d", byModelRec.Code)
	}

	badReq := httptest.NewRequest(http.MethodGet, "/usage-meters/summary?group_by=bogus", nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid group_by status = %d, want 400", badRec.Code)
	}

	seriesReq := httptest.NewRequest(http.MethodGet, "/usage-meters/series?interval=day&group_by=provider", nil)
	seriesRec := httptest.NewRecorder()
	router.ServeHTTP(seriesRec, seriesReq)
	if seriesRec.Code != http.StatusOK {
		t.Fatalf("series status = %d body=%s", seriesRec.Code, seriesRec.Body.String())
	}
	var series seriesPayload
	if err := json.Unmarshal(seriesRec.Body.Bytes(), &series); err != nil {
		t.Fatalf("series unmarshal: %v", err)
	}
	if !series.Enabled || len(series.Rows) == 0 {
		t.Fatalf("series rows empty: %+v", series)
	}
	if series.Interval != "day" || series.GroupBy != "provider" {
		t.Fatalf("series params = %+v", series)
	}
}

func TestUsageMetersDisabledReturnsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restore := usagestats.SetGlobalForTest(nil)
	defer restore()
	h := &Handler{}
	router := gin.New()
	router.GET("/usage-meters/summary", h.GetUsageMetersSummary)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage-meters/summary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled status = %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["enabled"] != false {
		t.Fatalf("enabled = %v, want false", payload["enabled"])
	}
}

func TestParseUsageWindowBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	ctx.Request = httptest.NewRequest(http.MethodGet, "/x?from=bogus&to=3", nil)
	_, _, ok := parseUsageWindow(ctx)
	if !ok {
		t.Fatal("garbage-from parses laxly, must not reject")
	}
}

func TestKiroTokenDataFromAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token":  "AT",
		"profile_arn":   "arn:aws:codewhisperer:us-east-1:1234:profile/x",
		"refresh_token": "RT",
	}}
	data := kiroTokenDataFromAuth(auth)
	if data == nil || data.AccessToken != "AT" || data.RefreshToken != "RT" || data.ProfileArn == "" {
		t.Fatalf("token extraction = %+v", data)
	}
	if got := kiroTokenDataFromAuth(&cliproxyauth.Auth{}); got.AccessToken != "" {
		t.Fatalf("empty auth must produce empty tokens: %+v", got)
	}
	if kiroauth.DefaultKiroRegion == "" {
		t.Fatal("kiro region constant sanity")
	}
}

func TestNativeQuotaSupportTypes(t *testing.T) {
	if !nativeQuotaSupported("kiro") || !nativeQuotaSupported("Kiro ") {
		t.Fatal("kiro must be native-supported")
	}
	if !nativeQuotaSupported("mirasim") {
		t.Fatal("mirasim must be native-supported")
	}
	if nativeQuotaSupported("cursor") || nativeQuotaSupported("cline") {
		t.Fatal("cursor/cline have no quota endpoint and must not be supported")
	}
}

func TestMapMirasimLimitsVariants(t *testing.T) {
	full := []byte(`{"plan":"PRO","limit":100,"used":25,"reset_at":"2026-10-01T00:00:00Z"}`)
	resp := mapMirasimLimits(full, "", nil)
	if resp.Subscription == nil || resp.Subscription.Plan != "PRO" {
		t.Fatalf("plan mapping = %+v", resp.Subscription)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %+v", resp.Groups)
	}
	bucket := resp.Groups[0].Buckets[0]
	if bucket.RemainingFraction < 0.74 || bucket.RemainingFraction > 0.76 {
		t.Fatalf("remaining fraction = %v, want ~0.75", bucket.RemainingFraction)
	}
	if bucket.ResetTime != "2026-10-01T00:00:00Z" {
		t.Fatalf("reset time = %q", bucket.ResetTime)
	}
	if len(resp.Summary) != 2 {
		t.Fatalf("summary metrics = %+v", resp.Summary)
	}

	credits := []byte(`{"total_credits":50,"used_credits":10,"next_reset":"soon"}`)
	resp2 := mapMirasimLimits(credits, "", nil)
	if len(resp2.Groups) != 1 || resp2.Groups[0].Buckets[0].RemainingFraction != 0.8 {
		t.Fatalf("credits variant = %+v", resp2)
	}
	if resp2.Groups[0].Buckets[0].ResetTime != "soon" {
		t.Fatalf("reset fallback = %+v", resp2.Groups[0].Buckets[0])
	}

	fractionOnly := []byte(`{"remaining_fraction":0.3}`)
	resp3 := mapMirasimLimits(fractionOnly, "", nil)
	if len(resp3.Groups) != 1 || resp3.Groups[0].Buckets[0].RemainingFraction != 0.3 {
		t.Fatalf("fraction variant = %+v", resp3)
	}

	if resp4 := mapMirasimLimits([]byte(`not json`), "", nil); len(resp4.Groups) != 0 {
		t.Fatalf("invalid body must not create groups: %+v", resp4)
	}

	// Vendor lane: budget windows with paid marker (the official client's
	// /v1/limits shape); model-scoped windows split into their own group,
	// localized group names, period labels and trimmed counters.
	vendor := []byte(`{"paid":false,"windows":[
		{"name":"weekly","budget":100,"used":25,"reset_at":"2026-10-08T00:00:00Z"},
		{"name":"claude-sonnet-4-6","budget":40,"used":40,"model_scoped":true,"status":"limit_reached"}
	]}`)
	respVendor := mapMirasimLimits(vendor, "go", []string{"claude-sonnet-4-6"})
	if respVendor.Subscription == nil || respVendor.Subscription.Plan != "go" || respVendor.Subscription.TierName != "free" {
		t.Fatalf("vendor subscription = %+v", respVendor.Subscription)
	}
	if len(respVendor.Groups) != 2 {
		t.Fatalf("vendor groups = %+v", respVendor.Groups)
	}
	accBucket := respVendor.Groups[0].Buckets[0]
	if respVendor.Groups[0].DisplayName != "账号额度" || accBucket.RemainingFraction != 0.75 || accBucket.ResetTime != "2026-10-08T00:00:00Z" {
		t.Fatalf("account window bucket = %+v", accBucket)
	}
	if accBucket.Description != "已用 25 / 100（25.0%）" {
		t.Fatalf("description must be trimmed: %q", accBucket.Description)
	}
	// Non-integral counters keep two-trimmed decimals; integral ones drop .0.
	budgetVal := 18640.0
	usedVal := 2185.5
	trimmed := mirasimWindowBucket(mirasimLimitsWindow{
		Name:    "weekly",
		Budget:  &budgetVal,
		Used:    &usedVal,
		ResetAt: json.RawMessage(`null`),
	}, false)
	if !strings.Contains(trimmed.Description, "已用 2185.5 / 18640") || strings.Contains(trimmed.Description, "18640.0") {
		t.Fatalf("fractional description = %q", trimmed.Description)
	}
	if len(respVendor.Summary) != 1 {
		t.Fatalf("summary metrics = %+v", respVendor.Summary)
	}
	summaryLabel := fmt.Sprint(respVendor.Summary[0].Label)
	if summaryLabel != "近 7 天已用" {
		t.Fatalf("summary label = %q, want period label", summaryLabel)
	}
	modelBucket := respVendor.Groups[1].Buckets[0]
	if respVendor.Groups[1].DisplayName != "按模型额度" || modelBucket.Window != "claude-sonnet-4-6" || modelBucket.RemainingFraction != 0 {
		t.Fatalf("model window bucket = %+v", modelBucket)
	}

	// Noise windows (0-use scoped families absent from the roster) drop out;
	// a matching family or any usage keeps the bucket.
	noisy := []byte(`{"windows":[
		{"name":"weekly","budget":100,"used":5},
		{"name":"7d_claude","budget":18640,"used":0,"model_scoped":true},
		{"name":"7d_kimi","budget":18640,"used":0,"model_scoped":true},
		{"name":"7d_deepseek","budget":18640,"used":12,"model_scoped":true}
	]}`)
	respNoisy := mapMirasimLimits(noisy, "go", []string{"kimi-k3", "glm-5.3-flash", "deepseek-flash"})
	if len(respNoisy.Groups) != 2 || len(respNoisy.Groups[1].Buckets) != 2 {
		t.Fatalf("noise-filtered groups = %+v", respNoisy.Groups)
	}
	windowsSeen := map[string]bool{}
	for _, bucket := range respNoisy.Groups[1].Buckets {
		windowsSeen[bucket.Window] = true
	}
	if windowsSeen["7d_claude"] {
		t.Fatalf("0-use absent-family window must drop: %+v", windowsSeen)
	}
	if !windowsSeen["7d_kimi"] || !windowsSeen["7d_deepseek"] {
		t.Fatalf("roster-family or used windows must stay: %+v", windowsSeen)
	}
	// Without a roster the filter fails open.
	respOpen := mapMirasimLimits(noisy, "go", nil)
	if len(respOpen.Groups) != 2 || len(respOpen.Groups[1].Buckets) != 3 {
		t.Fatalf("roster-less must keep every scoped window: %+v", respOpen.Groups)
	}

	// plan=go style empty relay answer: explicit marker instead of blank {}.
	respEmpty := mapMirasimLimits([]byte(`{}`), "go", nil)
	if respEmpty.Subscription == nil || respEmpty.Subscription.Plan != "go" {
		t.Fatalf("marker subscription = %+v", respEmpty.Subscription)
	}
	if len(respEmpty.Groups) != 1 || respEmpty.Groups[0].Buckets[0].Description == "" {
		t.Fatalf("empty relay answer must carry an explicit marker bucket: %+v", respEmpty.Groups)
	}
}

func TestFetchMirasimQuotaAgainstLocalServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/limits" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan":"PRO","limit":100,"used":40}`))
	}))
	defer server.Close()

	h := &Handler{}
	auth := &cliproxyauth.Auth{
		Provider:   "mirasim",
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}
	resp, handled, err := h.tryNativeQuotaFetch(context.Background(), auth)
	if !handled {
		t.Fatal("mirasim must be handled by native fetcher")
	}
	if err != nil {
		t.Fatalf("native mirasim fetch error: %v", err)
	}
	if resp.Subscription == nil || resp.Subscription.Plan != "PRO" {
		t.Fatalf("resp = %+v", resp)
	}
	if len(resp.Groups) != 1 || resp.Groups[0].Buckets[0].RemainingFraction != 0.6 {
		t.Fatalf("fraction = %+v", resp.Groups)
	}
}
