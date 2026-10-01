package usagestats

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openEventsTestRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := Open(Config{Path: filepath.Join(t.TempDir(), "events.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func seedEvents(t *testing.T, r *Recorder, base int64) {
	t.Helper()
	events := []Event{
		{TimestampMS: base - 300, Provider: "claude", AuthFile: "a.json", APIKeyHash: "k1", Endpoint: "/v1/messages", Model: "claude-sonnet-4-5", Status: "ok"},
		{TimestampMS: base - 200, Provider: "codex", AuthFile: "b.json", APIKeyHash: "k2", Endpoint: "/v1/responses", Model: "gpt-5", Status: "error", ErrorKind: "http_401"},
		{TimestampMS: base - 100, Provider: "codex", AuthFile: "b.json", APIKeyHash: "k2", Endpoint: "/v1/responses", Model: "gpt-5", Status: "error", ErrorKind: "http_429"},
		{TimestampMS: base, Provider: "claude", AuthFile: "c.json", APIKeyHash: "", Endpoint: "/v1/messages", Model: "claude-opus-4-1", Status: "ok"},
	}
	for _, ev := range events {
		r.Record(ev)
	}
	r.Flush()
}

func TestEventsFilteringAndPagination(t *testing.T) {
	r := openEventsTestRecorder(t)
	base := time.Now().UnixMilli()
	seedEvents(t, r, base)
	ctx := context.Background()

	// Default page size (50) covers all four rows, newest first.
	rows, total, err := r.Events(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("total=%d len=%d, want 4/4", total, len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].TsMS < rows[i].TsMS {
			t.Fatalf("rows not ordered ts desc: %v before %v", rows[i-1].TsMS, rows[i].TsMS)
		}
	}

	// Equality filters combine.
	rows, total, err = r.Events(ctx, EventFilter{Provider: "codex", Status: "error"})
	if err != nil {
		t.Fatalf("Events filtered: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("filtered total=%d len=%d, want 2/2", total, len(rows))
	}
	if rows[0].ErrorKind != "http_429" {
		t.Fatalf("newest codex error = %q, want http_429", rows[0].ErrorKind)
	}

	// Time bounds + auth_file + page size clamp + offset paging.
	page1, total, err := r.Events(ctx, EventFilter{FromMS: base - 250, PageSize: 1, Page: 1})
	if err != nil {
		t.Fatalf("Events page1: %v", err)
	}
	if total != 3 || len(page1) != 1 {
		t.Fatalf("page1 total=%d len=%d, want 3/1", total, len(page1))
	}
	page2, _, err := r.Events(ctx, EventFilter{FromMS: base - 250, PageSize: 1, Page: 2})
	if err != nil {
		t.Fatalf("Events page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID == page1[0].ID {
		t.Fatalf("page2 should hold the next distinct row: %+v vs %+v", page2, page1)
	}

	// Page size is capped at MaxEventPageSize and page at 1.
	filter := EventFilter{Page: -3, PageSize: 10_000}.Normalize()
	if filter.Page != 1 || filter.PageSize != MaxEventPageSize {
		t.Fatalf("Normalize = %+v", filter)
	}

	// api_key equality uses the stored hash label.
	rows, total, err = r.Events(ctx, EventFilter{APIKey: "k2"})
	if err != nil {
		t.Fatalf("Events api_key: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("api_key total=%d len=%d, want 2/2", total, len(rows))
	}
}

func TestAuthFileStatsAndErrorKinds(t *testing.T) {
	r := openEventsTestRecorder(t)
	base := time.Now().UnixMilli()
	seedEvents(t, r, base)
	ctx := context.Background()

	stats, err := r.AuthFileStats(ctx, 0, 0)
	if err != nil {
		t.Fatalf("AuthFileStats: %v", err)
	}
	if got := stats["a.json"]; got.Requests != 1 || got.Errors != 0 {
		t.Fatalf("a.json = %+v, want {1 0}", got)
	}
	if got := stats["b.json"]; got.Requests != 2 || got.Errors != 2 {
		t.Fatalf("b.json = %+v, want {2 2}", got)
	}

	// Window outside the events yields nothing.
	stats, err = r.AuthFileStats(ctx, base+1000, base+2000)
	if err != nil {
		t.Fatalf("AuthFileStats window: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("windowed stats = %+v, want empty", stats)
	}

	kinds, err := r.AuthFileErrorKindCounts(ctx, 0, 0)
	if err != nil {
		t.Fatalf("AuthFileErrorKindCounts: %v", err)
	}
	if kinds["b.json"]["http_401"] != 1 || kinds["b.json"]["http_429"] != 1 {
		t.Fatalf("b.json kinds = %+v", kinds["b.json"])
	}
	if _, ok := kinds["a.json"]; ok {
		t.Fatalf("a.json should have no error kinds: %+v", kinds["a.json"])
	}
}
