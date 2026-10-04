package modelprobe

import (
	"context"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func resetLiveSectionsForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { pruneLiveSections(map[string]struct{}{}) })
}

// TestSectionForAuthFallsBackToLiveOverlay pins the carrier split: credentials
// without a file backend (config API keys) resolve their section from the
// live overlay recorded by Store.ApplyOutcome, while file-backed credentials
// keep reading their metadata.
func TestSectionForAuthFallsBackToLiveOverlay(t *testing.T) {
	resetLiveSectionsForTest(t)
	store := &Store{AuthDir: t.TempDir()}

	keyAuth := &cliproxyauth.Auth{ID: "opencode-go:apikey:ovr-1", Provider: "opencode-go"}
	merged := store.ApplyOutcome(keyAuth, &Section{
		CheckedAt: "2026-10-04T00:00:00Z",
		PerModel:  map[string]*ModelOutcome{"dead-model": {Status: StatusNotAvailable}},
		Pruned:    []string{"dead-model"},
	})
	if merged == nil {
		t.Fatal("merged section nil")
	}
	got := SectionForAuth(&cliproxyauth.Auth{ID: keyAuth.ID, Provider: "opencode-go"})
	if got == nil || len(got.Pruned) != 1 || got.Pruned[0] != "dead-model" {
		t.Fatalf("overlay section = %+v", got)
	}

	// File-backed credentials ignore the overlay even when an entry exists.
	fileAuth := &cliproxyauth.Auth{ID: "file-ovr-1.json", FileName: "file-ovr-1.json", Provider: "cursor"}
	recordLiveSection(fileAuth.ID, &Section{
		CheckedAt: "2026-10-04T00:00:00Z",
		PerModel:  map[string]*ModelOutcome{"ghost-model": {Status: StatusNotAvailable}},
		Pruned:    []string{"ghost-model"},
	})
	if section := SectionForAuth(fileAuth); section != nil {
		t.Fatalf("file-backed credential must stay on metadata, got %+v", section)
	}
}

// TestStoreApplyOutcomeMergesAgainstLiveOverlay: rows probed in earlier
// cycles survive later partial cycles for file-less credentials — the merge
// base comes from the overlay, not the (always empty) clone metadata.
func TestStoreApplyOutcomeMergesAgainstLiveOverlay(t *testing.T) {
	resetLiveSectionsForTest(t)
	store := &Store{AuthDir: t.TempDir()}
	store.ApplyOutcome(&cliproxyauth.Auth{ID: "opencode-go:apikey:ovr-2", Provider: "opencode-go"}, &Section{
		CheckedAt: "2026-10-04T00:00:00Z",
		PerModel:  map[string]*ModelOutcome{"m-1": {Status: StatusNotAvailable}},
		Pruned:    []string{"m-1"},
	})
	second := store.ApplyOutcome(&cliproxyauth.Auth{ID: "opencode-go:apikey:ovr-2", Provider: "opencode-go"}, &Section{
		CheckedAt: "2026-10-04T01:00:00Z",
		PerModel:  map[string]*ModelOutcome{"m-2": {Status: StatusUsable}},
		Usable:    []string{"m-2"},
	})
	if second == nil || len(second.PerModel) != 2 || len(second.Pruned) != 1 || second.Pruned[0] != "m-1" {
		t.Fatalf("merged must keep prior rows: %+v", second)
	}
}

// TestHiddenModelIDsLiveOverlayForConfigKeyCredential reproduces the
// production failure: the scheduler only sees deep clones of config API-key
// credentials, so outcomes applied to a throwaway clone never reach later
// readers. The live overlay must drive hiding, visibility and revive with the
// same semantics as file-backed credentials.
func TestHiddenModelIDsLiveOverlayForConfigKeyCredential(t *testing.T) {
	resetLiveSectionsForTest(t)
	store := &Store{AuthDir: t.TempDir()} // no matching file: persist is a best-effort no-op like production
	clone := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{ID: "opencode-go:apikey:ovr-3", Provider: "opencode-go"}
	}

	// Probe outcomes land on a throwaway clone, exactly like a scheduler cycle.
	store.ApplyOutcome(clone(), &Section{
		CheckedAt: "2026-10-04T00:00:00Z",
		PerModel: map[string]*ModelOutcome{
			"dead-1": {Status: StatusNotAvailable},
			"dead-2": {Status: StatusNotAvailable},
			"busy":   {Status: StatusLimited},
			"flaky":  {Status: StatusUnreachable},
			"badkey": {Status: StatusAuthError},
			"alive":  {Status: StatusUsable},
		},
		Usable: []string{"alive"},
		Pruned: []string{"dead-1", "dead-2"},
	})

	hidden := HiddenModelIDs([]*cliproxyauth.Auth{clone()})
	for _, id := range []string{"dead-1", "dead-2"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("%s must hide: %v", id, idsOf(hidden))
		}
	}
	for _, id := range []string{"busy", "flaky", "badkey", "alive"} {
		if _, ok := hidden[id]; ok {
			t.Fatalf("%s must stay visible: %v", id, idsOf(hidden))
		}
	}

	// Revive: a later probe marks the dead models usable; the next assembly
	// un-hides them without any restart or credential reload.
	store.ApplyOutcome(clone(), &Section{
		CheckedAt: "2026-10-04T02:00:00Z",
		PerModel: map[string]*ModelOutcome{
			"dead-1": {Status: StatusUsable},
			"dead-2": {Status: StatusUsable},
		},
		Usable: []string{"dead-1", "dead-2"},
	})
	if hiddenAfter := HiddenModelIDs([]*cliproxyauth.Auth{clone()}); len(hiddenAfter) != 0 {
		t.Fatalf("revived models must reappear: %v", idsOf(hiddenAfter))
	}
}

// TestSchedulerKeyCredentialBackoffUsesLiveOverlay: with file-less
// credentials the scheduler resolves the previous section from the overlay
// even though every cycle sees a fresh clone, so failure streaks and the
// exponential backoff engage instead of re-probing everything every cycle.
func TestSchedulerKeyCredentialBackoffUsesLiveOverlay(t *testing.T) {
	resetLiveSectionsForTest(t)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	clock.Set(start)
	mock := &mockExec{handlers: map[string]error{
		"flaky": statusError{503},
	}}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		NowFunc:         clock.Now,
		DriverOverrides: map[string]RequestExecutor{"opencode-go": mock},
	})
	freshClone := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{ID: "opencode-go:apikey:ovr-4", Provider: "opencode-go"}
	}
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{freshClone()} },
		func(*cliproxyauth.Auth) []string { return []string{"flaky", "steady"} },
		SchedulerOptions{Interval: 24 * time.Hour, RecheckInterval: 7 * 24 * time.Hour},
	)

	// Cycle 1: both probe; flaky fails (streak 1), steady is usable.
	if probed := scheduler.probeAuth(context.Background(), freshClone(), 1); !probed {
		t.Fatal("first cycle must probe both models")
	}
	if got := len(mock.calls); got != 2 {
		t.Fatalf("cycle 1 probes = %v", got)
	}
	section := SectionForAuth(freshClone())
	if section == nil || section.PerModel["flaky"] == nil || section.PerModel["flaky"].Failures != 1 {
		t.Fatalf("live section after cycle 1 = %+v", section)
	}

	// 30h later (inside the 2^1*24h failure backoff for flaky, inside the 7d
	// usable recheck window for steady): nothing re-probes even though the
	// cycle sees only a fresh clone with an empty metadata.
	clock.Add(30 * time.Hour)
	mock.calls = nil
	if probed := scheduler.probeAuth(context.Background(), freshClone(), 2); probed {
		t.Fatal("backoff must gate re-probing")
	}
	if len(mock.calls) != 0 {
		t.Fatalf("no probes expected during backoff: %v", mock.calls)
	}

	// At 50h the 48h backoff expired: flaky re-probes and the streak grows.
	clock.Add(20 * time.Hour)
	if probed := scheduler.probeAuth(context.Background(), freshClone(), 3); !probed {
		t.Fatal("backoff window expired: must probe again")
	}
	if got := mock.calls; len(got) != 1 || got[0] != "flaky" {
		t.Fatalf("cycle 3 probes = %v, want [flaky]", got)
	}
	if got := SectionForAuth(freshClone()).PerModel["flaky"].Failures; got != 2 {
		t.Fatalf("failure streak must continue across clones, got %d", got)
	}
}

// TestRunCyclePrunesRemovedCredentialLiveSections keeps the overlay bounded:
// sections for credentials no longer listed drop on the next cycle.
func TestRunCyclePrunesRemovedCredentialLiveSections(t *testing.T) {
	resetLiveSectionsForTest(t)
	sectionSeed := func() *Section {
		return &Section{
			CheckedAt: "2026-10-04T00:00:00Z",
			PerModel:  map[string]*ModelOutcome{"m": {Status: StatusNotAvailable}},
		}
	}
	recordLiveSection("gone:apikey:ovr-5", sectionSeed())
	recordLiveSection("kept:apikey:ovr-5", sectionSeed())

	engine := NewEngine(nil, Options{})
	kept := &cliproxyauth.Auth{ID: "kept:apikey:ovr-5", Provider: "gemini"} // no probe driver: cycle is a pure bookkeeping pass
	scheduler := NewScheduler(engine, nil,
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{kept} },
		func(*cliproxyauth.Auth) []string { return []string{"m"} },
		SchedulerOptions{},
	)
	if probed := scheduler.RunCycle(context.Background()); probed != 0 {
		t.Fatalf("gemini must not probe: %d", probed)
	}
	if liveSectionFor("gone:apikey:ovr-5") != nil {
		t.Fatal("overlay entry for removed credential must be pruned")
	}
	if liveSectionFor("kept:apikey:ovr-5") == nil {
		t.Fatal("overlay entry for live credential must survive")
	}
}
