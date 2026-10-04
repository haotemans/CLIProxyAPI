package modelprobe

import (
	"context"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSpacingRange(t *testing.T) {
	cases := []struct {
		value string
		min   time.Duration
		max   time.Duration
	}{
		{"", 3 * time.Second, 12 * time.Second},
		{"2s-8s", 2 * time.Second, 8 * time.Second},
		{"8s-2s", 2 * time.Second, 8 * time.Second}, // swapped
		{"500ms", 500 * time.Millisecond, 500 * time.Millisecond},
		{"1s-2s", time.Second, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			min, max, err := SpacingRange(tc.value)
			if err != nil {
				t.Fatalf("SpacingRange(%q): %v", tc.value, err)
			}
			if min != tc.min || max != tc.max {
				t.Fatalf("SpacingRange(%q) = %v..%v, want %v..%v", tc.value, min, max, tc.min, tc.max)
			}
		})
	}
	if _, _, err := SpacingRange("bogus"); err == nil {
		t.Fatal("bogus must error")
	}
	if _, _, err := SpacingRange("1s-bogus"); err == nil {
		t.Fatal("bogus max must error")
	}
}

func TestEffectiveJitter(t *testing.T) {
	if got := EffectiveJitter(0); got != 0 {
		t.Fatalf("explicit zero stays exact-cadence: %v", got)
	}
	if got := EffectiveJitter(-1); got != 0 {
		t.Fatalf("negative clamps to 0: %v", got)
	}
	if got := EffectiveJitter(2); got != 1 {
		t.Fatalf("clamps to 1: %v", got)
	}
	if got := EffectiveJitter(0.5); got != 0.5 {
		t.Fatalf("0.5 passes: %v", got)
	}
}

func TestJitteredIntervalBounds(t *testing.T) {
	interval := 24 * time.Hour
	for _, u := range []float64{0, 0.25, 0.5, 0.75, 1} {
		got := jitteredInterval(interval, 0.5, u)
		lo := time.Duration(float64(interval) * 0.5)
		hi := time.Duration(float64(interval) * 1.5)
		if got < lo || got > hi {
			t.Fatalf("u=%v -> %v out of [%v, %v]", u, got, lo, hi)
		}
	}
	// Exact mid (u=0.5) == interval.
	if got := jitteredInterval(interval, 0.5, 0.5); got != interval {
		t.Fatalf("mid = %v, want %v", got, interval)
	}
	// u=0 lands at interval*(1-jitter).
	if got := jitteredInterval(interval, 0.5, 0); got != 12*time.Hour {
		t.Fatalf("u=0 = %v, want 12h", got)
	}
	// Jitter 0 = fixed cadence exactly.
	for _, u := range []float64{0, 0.5, 1} {
		if got := jitteredInterval(interval, 0, u); got != interval {
			t.Fatalf("jitter=0 u=%v = %v", u, got)
		}
	}
	// Negative span never happens.
	if got := jitteredInterval(interval, 2, 0); got <= 0 {
		t.Fatalf("jitter>1 must not invert: %v", got)
	}
}

func TestEngineSpacingAppliedPerProbe(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	var slept []time.Duration
	var calls int
	engine := NewEngine(nil, Options{
		MaxParallel: 1,
		SpacingMin:  2 * time.Second,
		SpacingMax:  4 * time.Second,
		SpacingRand: func() float64 { return 0.5 },
		Sleeper: func(_ context.Context, d time.Duration) error {
			calls++
			slept = append(slept, d)
			return nil
		},
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{ID: "x"}
	_ = engine.CredentialCycle(context.Background(), auth, "cursor", []string{"m1", "m2", "m3"})
	if calls != 3 {
		t.Fatalf("spacing sleeper calls = %d, want 3", calls)
	}
	for i, d := range slept {
		if d != 3*time.Second {
			t.Fatalf("slept[%d] = %v, want 3s (0.5 midpoint)", i, d)
		}
	}
	if len(mock.calls) != 3 {
		t.Fatalf("probes = %v", mock.calls)
	}
}

func TestEngineSpacingHonorsContextCancel(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	cancelled := false
	ctx, cancel := context.WithCancel(context.Background())
	engine := NewEngine(nil, Options{
		MaxParallel: 1,
		SpacingMin:  time.Second,
		SpacingMax:  2 * time.Second,
		SpacingRand: func() float64 { return 0 },
		Sleeper: func(ctx context.Context, d time.Duration) error {
			cancelled = true
			cancel()
			return ctx.Err()
		},
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{ID: "x"}
	_ = engine.CredentialCycle(ctx, auth, "cursor", []string{"m1", "m2", "m3"})
	if !cancelled {
		t.Fatal("sleeper not invoked")
	}
	if len(mock.calls) != 0 {
		t.Fatalf("probes must stop on canceled spacing sleep: %v", mock.calls)
	}
}

func TestSchedulerAuthErrorBackoff(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{
		ID:       "cursor-a.json",
		Provider: "cursor",
		Metadata: map[string]any{
			"access_token": "at",
			MetadataKey: &Section{
				CheckedAt: "2026-09-30T00:00:00Z",
				PerModel: map[string]*ModelOutcome{
					"composer-2": {Status: StatusAuthError, Error: "invalid token"},
				},
			},
		},
	}
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"composer-2"} },
		SchedulerOptions{},
	)

	// Cycle 1,2,3: skipped (every 4th cycle allowed).
	for cycle := uint64(1); cycle <= 3; cycle++ {
		probed := scheduler.probeAuth(context.Background(), auth, cycle)
		if probed {
			t.Fatalf("cycle %d must skip auth_error credential", cycle)
		}
	}
	if len(mock.calls) != 0 {
		t.Fatalf("probes must be skipped: %v", mock.calls)
	}
	section := ReadSection(auth.Metadata)
	if section == nil || !section.Skipped {
		t.Fatalf("skip decision must be recorded: %+v", section)
	}
	if section.SkipReason == "" || !strings.Contains(section.SkipReason, "auth_error") {
		t.Fatalf("skip reason = %q", section.SkipReason)
	}

	// Cycle 4: probed.
	if probed := scheduler.probeAuth(context.Background(), auth, 4); !probed {
		t.Fatal("cycle 4 must probe")
	}
	if len(mock.calls) != 1 {
		t.Fatalf("cycle 4 probes = %v", mock.calls)
	}
}

func TestSchedulerLimitedBackoffSkipsOddCycles(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{
		ID:       "cursor-b.json",
		Provider: "cursor",
		Metadata: map[string]any{
			"access_token": "at",
			MetadataKey: &Section{
				CheckedAt: "2026-09-30T00:00:00Z",
				PerModel: map[string]*ModelOutcome{
					"busy-model": {Status: StatusLimited},
					"ok-model":   {Status: StatusUsable},
				},
			},
		},
	}
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"busy-model", "ok-model"} },
		SchedulerOptions{},
	)

	// Odd cycle (1): busy-model skipped via limited backoff, ok-model probed.
	if probed := scheduler.probeAuth(context.Background(), auth, 1); !probed {
		t.Fatal("odd cycle must still probe non-limited models")
	}
	section := ReadSection(auth.Metadata)
	if section == nil || len(section.Skips) != 1 || !strings.Contains(section.Skips["busy-model"], "limited") {
		t.Fatalf("limited skip not recorded: %+v", section)
	}
	if len(mock.calls) != 1 || mock.calls[0] != "ok-model" {
		t.Fatalf("odd cycle probes = %v", mock.calls)
	}

	// Even cycle (2): busy-model re-enters; ok-model was just probed in cycle 1
	// and stays gated by the usable-recheck tier window.
	if probed := scheduler.probeAuth(context.Background(), auth, 2); !probed {
		t.Fatal("even cycle must probe")
	}
	if len(mock.calls) != 2 || mock.calls[1] != "busy-model" {
		t.Fatalf("even cycle probes = %v, want [ok-model busy-model]", mock.calls)
	}
}

func TestFilterLimitedBackoffDeterministic(t *testing.T) {
	prev := &Section{PerModel: map[string]*ModelOutcome{
		"busy": {Status: StatusLimited},
		"ok":   {Status: StatusUsable},
	}}
	models, skips := filterLimitedBackoff([]string{"busy", "ok"}, prev, 1)
	if len(models) != 1 || models[0] != "ok" {
		t.Fatalf("odd cycle models = %v", models)
	}
	if !strings.Contains(skips["busy"], "limited") {
		t.Fatalf("skips = %+v", skips)
	}
	models, skips = filterLimitedBackoff([]string{"busy", "ok"}, prev, 2)
	if len(models) != 2 || len(skips) != 0 {
		t.Fatalf("even cycle: models=%v skips=%v", models, skips)
	}
}
