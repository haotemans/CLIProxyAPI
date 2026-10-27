package modelprobe

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPruneUnusableHidesFailuresWithoutLosingRecovery(t *testing.T) {
	section := &Section{PerModel: map[string]*ModelOutcome{
		"good": {Status: StatusUsable}, "busy": {Status: StatusLimited},
		"down": {Status: StatusUnreachable}, "expired": {Status: StatusAuthError},
		"unknown": {Status: StatusUnknown},
	}}
	models := []*registry.ModelInfo{{ID: "good"}, {ID: "busy"}, {ID: "down"}, {ID: "expired"}, {ID: "unknown"}, {ID: "new"}}
	if got := FilterUnusableBySection(section, models); len(got) != 3 {
		t.Fatalf("want healthy, unknown, and unprobed models; got %v", got)
	}
	section.PerModel["busy"] = &ModelOutcome{Status: StatusUsable}
	if got := FilterUnusableBySection(section, models); len(got) != 4 {
		t.Fatal("successful recheck must restore the temporarily hidden model")
	}
}

func TestSchedulerRecoversHiddenModelsBesideHealthyCatalog(t *testing.T) {
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	auth := tierAuth("hidden-recovery.json", map[string]*ModelOutcome{
		"good":   {Status: StatusUsable, CheckedMS: start.UnixMilli()},
		"hidden": {Status: StatusNotAvailable, Failures: 1, CheckedMS: start.Add(-4 * time.Hour).UnixMilli()},
	})
	scheduler, driver := tierSchedulerFixture(t, auth, []string{"good"}, nil, clock)
	scheduler.pruneUnusable = true
	scheduler.interval = time.Hour
	if !scheduler.probeAuth(context.Background(), auth, 2) {
		t.Fatal("hidden model must remain in the recovery queue")
	}
	if len(driver.calls) != 1 || driver.calls[0] != "hidden" {
		t.Fatalf("fresh healthy model should be skipped, hidden model rechecked: %v", driver.calls)
	}
	section := SectionForAuth(auth)
	if len(section.Usable) != 2 || len(section.Pruned) != 0 {
		t.Fatalf("partial recheck lost healthy outcomes or failed to revive: %+v", section)
	}
}

func TestSchedulerMixedAuthFailuresDoNotDelayHealthyAccount(t *testing.T) {
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	auth := tierAuth("mixed-auth-recovery.json", map[string]*ModelOutcome{
		"good":   {Status: StatusUsable, CheckedMS: start.UnixMilli()},
		"hidden": {Status: StatusAuthError, Failures: 1, CheckedMS: start.Add(-72 * time.Hour).UnixMilli()},
	})
	scheduler, driver := tierSchedulerFixture(t, auth, []string{"good", "new"}, nil, clock)
	scheduler.pruneUnusable = true
	if !scheduler.probeAuth(context.Background(), auth, 1) || len(driver.calls) != 2 {
		t.Fatalf("one model's auth failure must not postpone new models or recovery: %v", driver.calls)
	}
}

type heldProbeExecutor struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (e *heldProbeExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	e.started <- struct{}{}
	select {
	case <-e.release:
		return coreexecutor.Response{}, nil
	case <-ctx.Done():
		return coreexecutor.Response{}, ctx.Err()
	}
}

func TestSchedulerDeduplicatesConcurrentCredentialProbes(t *testing.T) {
	driver := &heldProbeExecutor{started: make(chan struct{}, 2), release: make(chan struct{})}
	engine := NewEngine(nil, Options{MaxParallel: 2, DriverOverrides: map[string]RequestExecutor{"cursor": driver}})
	auth := &coreauth.Auth{ID: "dedupe-probe", Provider: "cursor"}
	scheduler := NewScheduler(engine, &Store{}, nil, func(*coreauth.Auth) []string { return []string{"model"} }, SchedulerOptions{})
	done := make(chan struct{})
	go func() { defer close(done); scheduler.TriggerAuth(context.Background(), auth) }()
	<-driver.started
	if scheduler.probeAuth(context.Background(), auth, 1) {
		t.Error("scheduled cycle duplicated the in-flight first-import probe")
	}
	scheduler.TriggerAuth(context.Background(), auth)
	close(driver.release)
	<-done
	if driver.calls.Load() != 1 {
		t.Fatalf("duplicate requests: %d", driver.calls.Load())
	}
}

func TestEngineCapsConcurrencyAcrossCredentials(t *testing.T) {
	driver := &heldProbeExecutor{started: make(chan struct{}, 3), release: make(chan struct{})}
	engine := NewEngine(nil, Options{MaxParallel: 2, DriverOverrides: map[string]RequestExecutor{"cursor": driver}})
	done := make(chan struct{}, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) {
			engine.ProbeOne(context.Background(), &coreauth.Auth{ID: id}, "cursor", "model")
			done <- struct{}{}
		}(id)
	}
	<-driver.started
	<-driver.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine.ProbeOne(ctx, &coreauth.Auth{ID: "c"}, "cursor", "model")
	close(driver.release)
	<-done
	<-done
	if driver.calls.Load() != 2 {
		t.Fatal("per-credential workers bypassed the shared concurrency cap")
	}
}

func TestMediaProbeDoesNotGenerateOrPrune(t *testing.T) {
	driver := &captureMockExec{}
	engine := NewEngine(nil, Options{DriverOverrides: map[string]RequestExecutor{"xai": driver}})
	for _, model := range []string{"grok-imagine-image", "grok-imagine-video", "grok-voice-tts-1.0"} {
		if got := engine.ProbeOne(context.Background(), &coreauth.Auth{}, "xai", model); got.Status != StatusUnknown {
			t.Fatalf("media request was misclassified: %+v", got)
		}
	}
	if driver.calls != 0 {
		t.Fatal("automatic text probing started a media generation request")
	}
	if !NewEngine(nil, Options{}).HasDriver("antigravity") {
		t.Fatal("Antigravity must support capability probes")
	}
}

func TestPruneUnusableModelListKeepsAlternateHealthyCredential(t *testing.T) {
	bad := tierAuth("bad-model-path", map[string]*ModelOutcome{"shared": {Status: StatusUnreachable}})
	good := tierAuth("good-model-path", map[string]*ModelOutcome{"shared": {Status: StatusUsable}})
	if _, hidden := HiddenModelIDs([]*coreauth.Auth{bad}, true)["shared"]; !hidden {
		t.Fatal("failed model leaked into the public list")
	}
	if _, hidden := HiddenModelIDs([]*coreauth.Auth{bad, good}, true)["shared"]; hidden {
		t.Fatal("one failed account hid a model available through another account")
	}
}
