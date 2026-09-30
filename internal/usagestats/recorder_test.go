package usagestats

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := configureDatabase(db); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	return db
}

func newTestRecorder(t *testing.T, cfg Config) *Recorder {
	t.Helper()
	db := openTestDB(t)
	r, err := newForTest(db, cfg)
	if err != nil {
		t.Fatalf("newForTest: %v", err)
	}
	return r
}

// pushAndFlush records events and drains them deterministically.
func pushAndFlush(t *testing.T, r *Recorder, events ...Event) {
	t.Helper()
	for _, ev := range events {
		r.Record(ev)
	}
	for i := 0; i < (len(events)/flushBatchCap)+1; i++ {
		r.flushOnce()
	}
}

func TestOpenMigratesSchemaAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "stats.db")
	r, err := Open(Config{Path: path, Pricer: NewPricer(nil)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = r.Close()
}

func TestSummaryAggregationByProvider(t *testing.T) {
	r := newTestRecorder(t, Config{Pricer: NewPricer(nil)})
	now := time.Now().UnixMilli()
	pushAndFlush(t, r,
		Event{TimestampMS: now - 1000, Provider: "claude", AuthFile: "a.json", Model: "claude-sonnet-4-5", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, UpstreamCostUSD: 0.01, Status: "ok"},
		Event{TimestampMS: now - 1001, Provider: "claude", AuthFile: "a.json", Model: "claude-sonnet-4-5", InputTokens: 300, OutputTokens: 100, TotalTokens: 400, Status: "ok"},
		Event{TimestampMS: now - 1002, Provider: "codex", AuthFile: "b.json", Model: "gpt-5-codex", InputTokens: 50, OutputTokens: 40, TotalTokens: 90, UpstreamCostUSD: 0.002, Status: "error", ErrorKind: "http_429"},
		Event{TimestampMS: now - int64(100*24*time.Hour/time.Millisecond) - 1, Provider: "claude", AuthFile: "a.json", Model: "old", InputTokens: 999, OutputTokens: 999, TotalTokens: 1998},
	)

	rows, err := r.Summary(context.Background(), now-2000, now, GroupProvider)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	byKey := map[string]SummaryRow{}
	for _, row := range rows {
		if row.Key == "" {
			t.Fatal("empty key")
		}
		byKey[row.Key] = row
	}
	claude := byKey["claude"]
	if claude.Requests != 2 || claude.InputTokens != 400 || claude.OutputTokens != 150 || claude.TotalTokens != 550 {
		t.Fatalf("claude row = %+v", claude)
	}
	if claude.UpstreamCostUSD < 0.009 || claude.UpstreamCostUSD > 0.011 {
		t.Fatalf("claude upstream cost = %v", claude.UpstreamCostUSD)
	}
	codex := byKey["codex"]
	if codex.Requests != 1 {
		t.Fatalf("codex row = %+v", codex)
	}

	// Time bounds: the 100-day-old event is outside the window.
	all, err := r.Summary(context.Background(), 0, 0, GroupProvider)
	if err != nil {
		t.Fatalf("unbounded summary: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unbounded rows = %d, want 2", len(all))
	}
	for _, row := range all {
		if row.Key == "claude" && row.Requests != 3 {
			t.Fatalf("unbounded claude requests = %d, want 3", row.Requests)
		}
	}
}

func TestSummaryGroupByVariants(t *testing.T) {
	r := newTestRecorder(t, Config{})
	now := time.Now().UnixMilli()
	pushAndFlush(t, r,
		Event{TimestampMS: now, Provider: "kiro", AuthFile: "k.json", APIKeyHash: "aa11bb22", Endpoint: "/v1/messages", Model: "kiro-auto", InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		Event{TimestampMS: now, Provider: "kiro", AuthFile: "other.json", APIKeyHash: "aa11bb22", Endpoint: "/v1/messages", Model: "kiro-auto", InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
	)

	byFile, err := r.Summary(context.Background(), 0, 0, GroupAuthFile)
	if err != nil || len(byFile) != 2 {
		t.Fatalf("auth_file rows = %+v err=%v", byFile, err)
	}
	byKey, err := r.Summary(context.Background(), 0, 0, GroupAPIKey)
	if err != nil || len(byKey) != 1 || byKey[0].Key != "aa11bb22" || byKey[0].Requests != 2 {
		t.Fatalf("api_key rows = %+v err=%v", byKey, err)
	}
	byDay, err := r.Summary(context.Background(), 0, 0, GroupDay)
	if err != nil || len(byDay) != 1 || byDay[0].Key == "" || !strings.Contains(byDay[0].Key, "-") {
		t.Fatalf("day rows = %+v err=%v", byDay, err)
	}
	if _, err := r.Summary(context.Background(), 0, 0, GroupBy("bogus")); err == nil {
		t.Fatal("bogus grouping must error")
	}
}

func TestSeriesBucketing(t *testing.T) {
	r := newTestRecorder(t, Config{})
	day1 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).UnixMilli()
	day2 := day1 + int64(24*time.Hour/time.Millisecond)
	pushAndFlush(t, r,
		Event{TimestampMS: day1, Provider: "claude", Model: "sonnet", InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		Event{TimestampMS: day1 + int64(time.Hour/time.Millisecond), Provider: "claude", Model: "sonnet", InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		Event{TimestampMS: day2, Provider: "claude", Model: "sonnet", InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
	)

	rows, err := r.Series(context.Background(), 0, 0, "day", GroupProvider)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("series rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Bucket >= rows[1].Bucket {
		t.Fatalf("buckets not ascending: %+v", rows)
	}
	if rows[0].Requests != 2 || rows[1].Requests != 1 {
		t.Fatalf("bucket counts = %+v", rows)
	}
	if rows[0].InputTokens != 3 || rows[1].InputTokens != 4 {
		t.Fatalf("bucket tokens = %+v", rows)
	}

	if _, err := r.Series(context.Background(), 0, 0, "week", GroupProvider); err == nil {
		t.Fatal("unsupported interval must error")
	}
	if _, err := r.Series(context.Background(), 0, 0, "day", GroupAuthFile); err == nil {
		t.Fatal("series limited to provider|model")
	}
}

func TestRetentionPrune(t *testing.T) {
	r := newTestRecorder(t, Config{RetentionDays: 90})
	now := time.Now().UnixMilli()
	old := now - int64(120*24*time.Hour/time.Millisecond)
	pushAndFlush(t, r,
		Event{TimestampMS: old, Provider: "claude", Model: "x", InputTokens: 1},
		Event{TimestampMS: now, Provider: "claude", Model: "x", InputTokens: 1},
	)
	r.prune()
	totals, err := r.Totals(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Requests != 1 {
		t.Fatalf("after prune requests = %d, want 1", totals.Requests)
	}
}

func TestDroppedOverflow(t *testing.T) {
	db := openTestDB(t)
	r, err := newForTest(db, Config{})
	if err != nil {
		t.Fatalf("newForTest: %v", err)
	}
	for i := 0; i < channelSize+5; i++ {
		r.Record(Event{Provider: "x"})
	}
	if got := r.Dropped(); got != 5 {
		t.Fatalf("dropped = %d, want 5", got)
	}
}

func TestEventFromRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ginCtx.Params = gin.Params{{Key: "path", Value: "/v1/chat/completions"}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	record := coreusage.Record{
		Provider:    "cline",
		AuthID:      "cline-a.json",
		APIKey:      "super-secret-raw-key",
		Model:       "anthropic/claude-sonnet-4-6",
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: 429},
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5, CachedTokens: 3, TotalTokens: 15, CostUSD: 0.00042},
		Latency:     1500 * time.Millisecond,
		Stream:      true,
		RequestedAt: time.Unix(1_700_000_000, 0),
	}
	ev := EventFromRecord(ctx, record)
	if ev.Provider != "cline" || ev.AuthFile != "cline-a.json" {
		t.Fatalf("identity = %+v", ev)
	}
	if ev.APIKeyHash == "" {
		t.Fatal("api key must be hashed under recording")
	}
	if ev.APIKeyHash == "super-secret-raw-key" || len(ev.APIKeyHash) != 8 {
		t.Fatalf("api key label must be an 8-char hash prefix, got %q", ev.APIKeyHash)
	}
	if got := MaskKeyLabel("super-secret-raw-key"); got != ev.APIKeyHash {
		t.Fatalf("MaskKeyLabel mismatch %q vs %q", got, ev.APIKeyHash)
	}
	if MaskKeyLabel("") != "" || MaskKeyLabel("   ") != "" {
		t.Fatal("blank key must stay blank")
	}
	if ev.Status != "error" || ev.ErrorKind != "http_429" {
		t.Fatalf("failure mapping = %+v", ev)
	}
	if ev.UpstreamCostUSD != 0.00042 {
		t.Fatalf("upstream cost = %v", ev.UpstreamCostUSD)
	}
	if ev.LatencyMS != 1500 {
		t.Fatalf("latency = %d", ev.LatencyMS)
	}
	if ev.TimestampMS != 1_700_000_000_000 {
		t.Fatalf("ts = %d", ev.TimestampMS)
	}
	if ev.TotalTokens != 15 || ev.CachedTokens != 3 {
		t.Fatalf("token mapping = %+v", ev)
	}
}

func TestRecorderHandleUsagePluginInterface(t *testing.T) {
	r := newTestRecorder(t, Config{})
	var plugin coreusage.Plugin = r
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider: "cursor", AuthID: "c.json", Model: "composer-2",
		Detail: coreusage.Detail{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
	})
	r.flushOnce()
	totals, err := r.Totals(context.Background(), 0, 0)
	if err != nil || totals.Requests != 1 {
		t.Fatalf("usage plugin path failed: %+v err=%v", totals, err)
	}
}

var _ coreusage.Plugin = (*Recorder)(nil)
