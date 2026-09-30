package modelprobe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type statusError struct{ code int }

func (e statusError) Error() string   { return "status" }
func (e statusError) StatusCode() int { return e.code }

func TestClassifyProbeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Status
	}{
		{"429 limited", statusError{429}, StatusLimited},
		{"401 auth", statusError{401}, StatusAuthError},
		{"403 auth", statusError{403}, StatusAuthError},
		{"503 unreachable", statusError{503}, StatusUnreachable},
		{"404 not available", statusError{404}, StatusNotAvailable},
		{"400 not available", statusError{400}, StatusNotAvailable},
		{"tier message", errors.New("model not enabled for your account"), StatusNotAvailable},
		{"unknown model", errors.New("unknown model: gpt-9"), StatusNotAvailable},
		{"tier gated", errors.New("requires pro plan upgrade"), StatusNotAvailable},
		{"rate limit", errors.New("rate limit exceeded"), StatusLimited},
		{"quota", errors.New("insufficient user quota"), StatusLimited},
		{"suspended", errors.New("account suspended"), StatusAuthError},
		{"network", errors.New("connection reset by peer"), StatusUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProbeError(tc.err); got != tc.want {
				t.Fatalf("classifyProbeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	if got := classifyProbeError(nil); got != StatusUsable {
		t.Fatalf("nil error = %v", got)
	}
}

func TestSectionRoundTripAndMerge(t *testing.T) {
	metadata := map[string]any{}
	section := &Section{
		CheckedAt: "2026-10-01T00:00:00Z",
		Usable:    []string{"model-a", "model-b"},
		Pruned:    []string{"model-c"},
		PerModel: map[string]*ModelOutcome{
			"model-a": {Status: StatusUsable},
			"model-b": {Status: StatusUsable},
			"model-c": {Status: StatusNotAvailable, Error: "blocked"},
		},
	}
	metadata = UpdateFileMetadata(metadata, section)
	read := ReadSection(metadata)
	if read == nil {
		t.Fatal("section missing after round trip")
	}
	if len(read.Usable) != 2 || len(read.Pruned) != 1 || read.Pruned[0] != "model-c" {
		t.Fatalf("round trip mismatch: %+v", read)
	}
	if read.PerModel["model-c"].Status != StatusNotAvailable {
		t.Fatalf("per-model status = %+v", read.PerModel["model-c"])
	}

	// Merge: model-c becomes usable again (re-add flow).
	fresh := &Section{
		CheckedAt: "2026-10-02T00:00:00Z",
		PerModel: map[string]*ModelOutcome{
			"model-c": {Status: StatusUsable},
		},
		Usable: []string{"model-c"},
	}
	merged := MergeSections(read, fresh)
	if merged == nil || len(merged.Pruned) != 0 {
		t.Fatalf("merged should clear pruning: %+v", merged)
	}
	if _, ok := merged.PerModel["model-a"]; !ok {
		t.Fatalf("stale rows must be kept: %+v", merged.PerModel)
	}
	if merged.CheckedAt != "2026-10-02T00:00:00Z" {
		t.Fatalf("checked at = %q", merged.CheckedAt)
	}
}

func TestFilterPrunedForAuth(t *testing.T) {
	models := []*registry.ModelInfo{
		{ID: "alpha"}, {ID: "beta"}, {ID: "gamma"},
	}
	if got := FilterPrunedForAuth(nil, models); len(got) != 3 {
		t.Fatalf("absent section must passthrough: %d", len(got))
	}
	metadata := map[string]any{MetadataKey: &Section{Pruned: []string{"BETA"}}}
	filtered := FilterPrunedForAuth(metadata, models)
	if len(filtered) != 2 || filtered[0].ID != "alpha" || filtered[1].ID != "gamma" {
		t.Fatalf("filter = %+v", filtered)
	}
}

func TestStoreApplyOutcomePersistsAuthFile(t *testing.T) {
	dir := t.TempDir()
	name := "probe-alias.json"
	content := `{"type":"cursor","access_token":"at","refresh_token":"rt"}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := &Store{AuthDir: dir}
	auth := &cliproxyauth.Auth{
		ID:       name,
		FileName: name,
		Provider: "cursor",
		Metadata: map[string]any{"type": "cursor", "access_token": "at"},
	}
	section := &Section{
		CheckedAt: "2026-10-01T00:00:00Z",
		Usable:    []string{"composer-2"},
		Pruned:    []string{"gpt-4o", "claude-3.5-sonnet"},
		PerModel: map[string]*ModelOutcome{
			"composer-2":        {Status: StatusUsable},
			"gpt-4o":            {Status: StatusNotAvailable, Error: "requires pro"},
			"claude-3.5-sonnet": {Status: StatusNotAvailable},
		},
	}
	merged := store.ApplyOutcome(auth, section)
	if merged == nil {
		t.Fatal("merged nil")
	}
	if auth.Metadata[MetadataKey] == nil {
		t.Fatal("in-memory metadata not updated")
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted file invalid JSON: %v", err)
	}
	sec := ReadSection(persisted)
	if sec == nil || len(sec.Pruned) != 2 || len(sec.Usable) != 1 {
		t.Fatalf("persisted section = %+v", sec)
	}
	if persisted["type"] != "cursor" || persisted["access_token"] != "at" {
		t.Fatalf("auth content clobbered: %+v", persisted)
	}
}

type mockExec struct {
	mu       sync.Mutex
	calls    []string
	handlers map[string]error
}

func (m *mockExec) Execute(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req.Model)
	m.mu.Unlock()
	if err, ok := m.handlers[req.Model]; ok {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func TestEngineCredentialCycleWithMockExecutor(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{
		"bad-tier":  errors.New("model not enabled for this account"),
		"busy-429":  statusError{429},
		"forbidden": statusError{401},
	}}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	engine := NewEngine(nil, Options{
		MaxParallel: 1,
		NowFunc:     func() time.Time { return now },
		DriverOverrides: map[string]RequestExecutor{
			"cursor": mock,
		},
	})
	auth := &cliproxyauth.Auth{ID: "cursor-a.json", Provider: "cursor", Metadata: map[string]any{"access_token": "at"}}
	section := engine.CredentialCycle(context.Background(), auth, "cursor", []string{"composer-2", "bad-tier", "busy-429", "forbidden"})
	if section == nil {
		t.Fatal("no section")
	}
	if len(section.Usable) != 1 || section.Usable[0] != "composer-2" {
		t.Fatalf("usable = %+v", section.Usable)
	}
	if len(section.Pruned) != 1 || section.Pruned[0] != "bad-tier" {
		t.Fatalf("pruned = %+v", section.Pruned)
	}
	if section.PerModel["busy-429"].Status != StatusLimited {
		t.Fatalf("429 = %+v", section.PerModel["busy-429"])
	}
	if section.PerModel["forbidden"].Status != StatusAuthError {
		t.Fatalf("401 = %+v", section.PerModel["forbidden"])
	}
	if len(mock.calls) != 4 {
		t.Fatalf("executor calls = %v", mock.calls)
	}
}

func TestEngineMaxModelsPerCycleCap(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	engine := NewEngine(nil, Options{
		MaxModelsPerCredentialPerCycle: 2,
		DriverOverrides:                map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{ID: "x"}
	_ = engine.CredentialCycle(context.Background(), auth, "cursor", []string{"m1", "m2", "m3", "m4"})
	if len(mock.calls) != 2 {
		t.Fatalf("cap violated: %v", mock.calls)
	}
}

type panicExec struct{}

func (panicExec) Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	panic("boom")
}

func TestSchedulerCycleEndToEnd(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{
		"blocked": errors.New("model not enabled"),
	}}
	engine := NewEngine(nil, Options{
		MaxParallel: 1,
		DriverOverrides: map[string]RequestExecutor{
			"cursor": mock,
		},
	})
	auth := &cliproxyauth.Auth{ID: "cursor-a.json", Provider: "cursor", Metadata: map[string]any{"access_token": "at"}}
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"composer-2", "blocked"} },
		SchedulerOptions{},
	)
	probed := scheduler.RunCycle(context.Background())
	if probed != 1 {
		t.Fatalf("probed = %d, want 1", probed)
	}
	section := ReadSection(auth.Metadata)
	if section == nil || len(section.Usable) != 1 || len(section.Pruned) != 1 {
		t.Fatalf("section after cycle = %+v", section)
	}
}

func TestSchedulerSafeCycleNeverPanics(t *testing.T) {
	engine := NewEngine(nil, Options{
		MaxParallel: 1,
		DriverOverrides: map[string]RequestExecutor{
			"cursor": panicExec{},
		},
	})
	auth := &cliproxyauth.Auth{ID: "a.json", Provider: "cursor"}
	scheduler := NewScheduler(engine, nil,
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"m"} },
		SchedulerOptions{},
	)
	var reported = -1
	scheduler.OnCycleComplete = func(n int) { reported = n }
	scheduler.safeCycle(context.Background())
	if reported != 0 {
		t.Fatalf("panicking credential must report 0 probes: %d", reported)
	}
}

func TestSchedulerDoesNotProbeUnsupportedProvider(t *testing.T) {
	engine := NewEngine(nil, Options{})
	auth := &cliproxyauth.Auth{ID: "gemini-a.json", Provider: "gemini"}
	probedCalls := int32(0)
	scheduler := NewScheduler(engine, nil,
		func() []*cliproxyauth.Auth {
			atomic.AddInt32(&probedCalls, 1)
			return []*cliproxyauth.Auth{auth}
		},
		func(*cliproxyauth.Auth) []string { return []string{"gemini-2.5-pro"} },
		SchedulerOptions{},
	)
	if got := scheduler.RunCycle(context.Background()); got != 0 {
		t.Fatalf("gemini has no V1 probe driver: %d", got)
	}
	if atomic.LoadInt32(&probedCalls) != 1 {
		t.Fatal("auths consulted")
	}
}

func TestStartStopWithFakeTicker(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{}}
	engine := NewEngine(nil, Options{
		NowFunc:         func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) },
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{ID: "cursor-a.json", Provider: "cursor", Metadata: map[string]any{"access_token": "at"}}
	tick := make(chan time.Time)
	stopCalled := make(chan struct{}, 1)
	completed := make(chan struct{}, 4)
	scheduler := NewScheduler(engine, nil,
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"composer-2"} },
		SchedulerOptions{
			Interval: time.Minute,
			NewTicker: func(time.Duration) (<-chan time.Time, func()) {
				return tick, func() { close(stopCalled) }
			},
		},
	)
	scheduler.OnCycleComplete = func(int) { completed <- struct{}{} }
	scheduler.Start(context.Background())

	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("boot cycle never completed")
	}
	select {
	case <-stopCalled:
		t.Fatal("ticker stopped before tick")
	default:
	}

	tick <- time.Now()
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("interval cycle never completed")
	}
	scheduler.Stop()
	if len(mock.calls) != 2 {
		t.Fatalf("mock calls = %v, want 2 (boot + interval probe)", len(mock.calls))
	}
}

func TestSupportedProvidersList(t *testing.T) {
	providers := SupportedProviders()
	needle := map[string]bool{}
	for _, p := range providers {
		needle[p] = true
	}
	for _, want := range []string{"cursor", "kiro", "cline", "claude", "codex"} {
		if !needle[want] {
			t.Fatalf("supported providers %v missing %s", providers, want)
		}
	}
}

func TestSectionPersistenceThroughMetadata(t *testing.T) {
	metadata := map[string]any{"type": "cursor", "access_token": "at"}
	if ReadSection(metadata) != nil {
		t.Fatal("no section expected")
	}
	metadata = UpdateFileMetadata(metadata, synthesizeSection(time.Now(), map[string]ModelOutcome{
		"composer-2": {Status: StatusUsable},
	}))
	if ReadSection(metadata) == nil {
		t.Fatal("section must survive metadata write")
	}
}
