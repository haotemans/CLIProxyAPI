package modelprobe

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFailureBackoffBounds(t *testing.T) {
	interval := 24 * time.Hour
	recheck := 7 * 24 * time.Hour
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, interval},
		{1, 2 * interval},
		{2, 4 * interval},
		{3, recheck}, // 2^3 * 24h = 192h exceeds the 7d cap
		{-2, interval},
		{10, recheck}, // capped hard at the recheck interval
	}
	for _, tc := range cases {
		if got := failureBackoff(tc.failures, interval, recheck); got != tc.want {
			t.Fatalf("failures=%d backoff = %v, want %v", tc.failures, got, tc.want)
		}
	}
	// Defaults: zero interval falls back to 6h, zero recheck to 7d, and the
	// cap still applies when interval itself exceeds recheck.
	if got := failureBackoff(0, 0, 0); got != 6*time.Hour {
		t.Fatalf("zero interval default = %v", got)
	}
	if got := failureBackoff(0, interval, time.Hour); got != time.Hour {
		t.Fatalf("recheck smaller than interval must cap: %v", got)
	}
}

// fakeClock drives engine.Now through a manually advanced clock (no sleeps).
type fakeClock struct{ now atomic.Int64 }

func (c *fakeClock) Set(t time.Time)     { c.now.Store(t.UnixMilli()) }
func (c *fakeClock) Add(d time.Duration) { c.now.Add(d.Milliseconds()) }
func (c *fakeClock) Now() time.Time      { return time.UnixMilli(c.now.Load()) }

func tierSchedulerFixture(t *testing.T, auth *cliproxyauth.Auth, catalog []string, handlers map[string]error, clock *fakeClock) (*Scheduler, *mockExec) {
	t.Helper()
	mock := &mockExec{handlers: handlers}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		NowFunc:         clock.Now,
		SpacingMin:      0,
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return catalog },
		SchedulerOptions{
			Interval:        24 * time.Hour,
			RecheckInterval: 7 * 24 * time.Hour,
		},
	)
	return scheduler, mock
}

func tierAuth(name string, rows map[string]*ModelOutcome) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       name,
		Provider: "cursor",
		Metadata: map[string]any{
			"access_token": "at",
			MetadataKey: &Section{
				CheckedAt: "2026-09-20T00:00:00Z",
				PerModel:  rows,
			},
		},
	}
}

func TestSchedulerTwoTierRecheckWindow(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	auth := tierAuth("tier-a.json", map[string]*ModelOutcome{
		// Known-alive checked 3 days ago: inside the 7d recheck window.
		"alive-fresh": {Status: StatusUsable, CheckedMS: start.Add(-72 * time.Hour).UnixMilli()},
		// Known-alive checked 10 days ago: overdue for the low-frequency tier.
		"alive-stale": {Status: StatusUsable, CheckedMS: start.Add(-240 * time.Hour).UnixMilli()},
	})
	scheduler, mock := tierSchedulerFixture(t, auth, []string{"alive-fresh", "alive-stale", "brand-new"}, nil, clock)

	if probed := scheduler.probeAuth(context.Background(), auth, 1); !probed {
		t.Fatal("cycle must probe overdue and never-probed candidates")
	}
	if got := mock.calls; len(got) != 2 {
		t.Fatalf("first cycle probes = %v, want alive-stale + brand-new", got)
	}
	section := ReadSection(auth.Metadata)
	if section == nil || section.Skips["alive-fresh"] == "" {
		t.Fatalf("fresh usable model must record a recheck skip: %+v", section.Skips)
	}
	got := map[string]bool{}
	for _, id := range mock.calls {
		got[id] = true
	}
	if !got["alive-stale"] || !got["brand-new"] || got["alive-fresh"] {
		t.Fatalf("due set = %v", got)
	}

	// Advance past the whole recheck window: the fresh row from cycle 1 is
	// now stale, everything re-enters.
	clock.Add(8 * 24 * time.Hour)
	mock.calls = nil
	if probed := scheduler.probeAuth(context.Background(), auth, 2); !probed {
		t.Fatal("after the recheck window all known models re-probe")
	}
	if got := len(mock.calls); got != 3 {
		t.Fatalf("post-window probes = %v, want all 3", got)
	}
}

func TestSchedulerExponentialFailureBackoff(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	auth := tierAuth("backoff-a.json", map[string]*ModelOutcome{
		// One recorded failure 49h ago: 2^1 * 24h = 48h backoff -> due.
		"flaky": {Status: StatusUnreachable, CheckedMS: start.Add(-49 * time.Hour).UnixMilli(), Failures: 1},
	})
	scheduler, mock := tierSchedulerFixture(t, auth, []string{"flaky"}, map[string]error{
		"flaky": statusError{503},
	}, clock)

	if probed := scheduler.probeAuth(context.Background(), auth, 1); !probed {
		t.Fatal("past the 48h backoff the model must probe")
	}
	if got := len(mock.calls); got != 1 {
		t.Fatalf("probes = %v", got)
	}
	section := ReadSection(auth.Metadata)
	if got := section.PerModel["flaky"].Failures; got != 2 {
		t.Fatalf("streak = %d, want 2 (continued from previous)", got)
	}

	// Immediately re-cycling: 2^2 * 24h = 96h backoff, nothing probes.
	mock.calls = nil
	if probed := scheduler.probeAuth(context.Background(), auth, 2); probed {
		t.Fatal("fresh failure must wait out the 96h backoff")
	}
	if len(mock.calls) != 0 {
		t.Fatalf("no probes during backoff: %v", mock.calls)
	}

	// 95h later still gated; 97h later due again, streak grows to 3.
	clock.Add(95 * time.Hour)
	if probed := scheduler.probeAuth(context.Background(), auth, 3); probed {
		t.Fatal("95h into a 96h backoff must stay gated")
	}
	clock.Add(2 * time.Hour)
	if probed := scheduler.probeAuth(context.Background(), auth, 4); !probed {
		t.Fatal("past the 96h backoff must probe")
	}
	section = ReadSection(auth.Metadata)
	if got := section.PerModel["flaky"].Failures; got != 3 {
		t.Fatalf("streak = %d, want 3", got)
	}
}

func TestSchedulerReprobesRevivedNotAvailableImmediately(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	auth := tierAuth("revive-a.json", map[string]*ModelOutcome{
		// Pruned long ago with a huge streak: a not_available row re-entering
		// the catalog ignores every backoff window.
		"was-dead": {Status: StatusNotAvailable, CheckedMS: start.UnixMilli(), Failures: 9},
	})
	scheduler, mock := tierSchedulerFixture(t, auth, []string{"was-dead"}, nil, clock)
	if probed := scheduler.probeAuth(context.Background(), auth, 1); !probed {
		t.Fatal("a re-entering not_available model must probe immediately")
	}
	if got := len(mock.calls); got != 1 {
		t.Fatalf("probes = %v", got)
	}
}

func TestContinueFailureStreaks(t *testing.T) {
	previous := &Section{PerModel: map[string]*ModelOutcome{
		"won":        {Status: StatusUnreachable, Failures: 3},
		"busy":       {Status: StatusLimited, Failures: 2},
		"healed":     {Status: StatusUnreachable, Failures: 4},
		"first-fail": {Status: StatusUsable},
	}}
	fresh := &Section{PerModel: map[string]*ModelOutcome{
		"won":        {Status: StatusUnreachable},
		"busy":       {Status: StatusLimited},
		"healed":     {Status: StatusUsable},
		"first-fail": {Status: StatusAuthError},
		"new-fail":   {Status: StatusUnreachable},
	}}
	ContinueFailureStreaks(previous, fresh)
	if got := fresh.PerModel["won"].Failures; got != 4 {
		t.Fatalf("lost streak = %d, want 4", got)
	}
	if got := fresh.PerModel["busy"].Failures; got != 2 {
		t.Fatalf("limited must preserve without growing, got %d", got)
	}
	if got := fresh.PerModel["healed"].Failures; got != 0 {
		t.Fatalf("usable must reset the streak, got %d", got)
	}
	if got := fresh.PerModel["first-fail"].Failures; got != 1 {
		t.Fatalf("first failure after success = %d, want 1", got)
	}
	if got := fresh.PerModel["new-fail"].Failures; got != 1 {
		t.Fatalf("never-before-seen failure = %d, want 1", got)
	}
}
