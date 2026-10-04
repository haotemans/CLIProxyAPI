package distribution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestNewUsageStorePersistsAtomicallyAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "distribution-usage.json")
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewUsageStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewUsageStore: %v", err)
	}
	store.Add("abcd1234", 1.25, 100, 20)
	store.Add("abcd1234", 0.5, 50, 10)
	store.Add("deadbeef", 0.25, 0, 0)

	if got := store.Spent("abcd1234"); got != 1.75 {
		t.Fatalf("spent = %v, want 1.75", got)
	}
	snap := store.Snapshot()
	if snap["abcd1234"].Requests != 2 || snap["abcd1234"].InputTokens != 150 || snap["abcd1234"].OutputTokens != 30 {
		t.Fatalf("snapshot row = %+v", snap["abcd1234"])
	}
	if snap["abcd1234"].LastRequestAt == "" {
		t.Fatal("last_request_at must be stamped from the injected clock")
	}
	if _, errRead := os.Stat(path); errRead != nil {
		t.Fatalf("state file must exist after Add: %v", errRead)
	}
	// No temp file left behind.
	if _, errStat := os.Stat(path + ".tmp"); errStat == nil {
		t.Fatal("temp file must not survive the atomic rename")
	}

	// Restart: a fresh store on the same path keeps the cumulative spend.
	store2, err := NewUsageStore(path, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := store2.Spent("abcd1234"); got != 1.75 {
		t.Fatalf("reloaded spent = %v, want 1.75", got)
	}

	// Reset zeroes counters and persists.
	store.Reset("abcd1234")
	if got := store.Spent("abcd1234"); got != 0 {
		t.Fatalf("post-reset spent = %v", got)
	}
	store3, _ := NewUsageStore(path, nil)
	if snap3 := store3.Snapshot(); snap3["abcd1234"].SpentUSD != 0 || snap3["deadbeef"].SpentUSD != 0.25 {
		t.Fatalf("reset persistence = %+v", snap3)
	}
}

func TestUsageStoreHandleUsageAccumulatesWithPricerFallback(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	store, err := NewUsageStore(filepath.Join(dir, "state.json"), func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewUsageStore: %v", err)
	}
	// No upstream-reported cost: the shared pricer (default table) resolves
	// gpt-5 at 1.25 in / 10 out per 1M.
	store.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "dk-test-handle-usage",
		Model:  "gpt-5",
		Detail: coreusage.Detail{InputTokens: 200_000, OutputTokens: 50_000},
	})
	hash := usagestats.MaskKeyLabel("dk-test-handle-usage")
	want := 200_000*1.25/1_000_000 + 50_000*10.0/1_000_000
	if got := store.Spent(hash); got != want {
		t.Fatalf("spent = %v, want pricer-computed %v", got, want)
	}
	// Upstream-reported USD wins over the pricer.
	store.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "dk-test-handle-usage",
		Model:  "gpt-5",
		Detail: coreusage.Detail{CostUSD: 0.75},
	})
	if got := store.Spent(hash); got != want+0.75 {
		t.Fatalf("spent with upstream cost = %v, want %v", got, want+0.75)
	}
	// Keyless records never enter the ledger.
	before := len(store.Snapshot())
	store.HandleUsage(context.Background(), coreusage.Record{Model: "gpt-5"})
	if got := len(store.Snapshot()); got != before {
		t.Fatalf("keyless record leaked into the ledger: %d -> %d", before, got)
	}
}

func TestDistributionStartDeduplicatesPerPath(t *testing.T) {
	t.Cleanup(func() { SetGlobal(nil) })
	dir := t.TempDir()
	cfg := &config.Config{AuthDir: dir}
	first, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	second, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start again: %v", err)
	}
	if first != second {
		t.Fatal("Start must reuse the same store for the same auth dir")
	}
	if Global() != first {
		t.Fatal("Global must return the started store")
	}
	if Global().Spent("x") != 0 {
		t.Fatal("fresh store must report zero")
	}
	if _, err = Start(&config.Config{}); err == nil {
		t.Fatal("missing auth-dir must error, not panic")
	}
}
