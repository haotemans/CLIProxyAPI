package cliproxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// probeStubExecutor fails per model with classify-friendly errors; nil map
// entries probe usable.
type probeStubExecutor struct {
	mu       sync.Mutex
	failures map[string]error
	calls    []string
}

func (e *probeStubExecutor) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, req.Model)
	err := e.failures[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *probeStubExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func synthesizeOpencodeConfigAuth(t *testing.T, cfg *config.Config) *coreauth.Auth {
	t.Helper()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("synthesize config auths: %v", err)
	}
	for _, auth := range auths {
		if auth != nil && auth.Provider == "opencode-go" {
			if auth.FileName != "" {
				t.Fatalf("config API-key auth must have no file backend, got FileName=%q", auth.FileName)
			}
			return auth
		}
	}
	t.Fatal("no opencode-go auth synthesized")
	return nil
}

// TestConfigAPIKeyProbeDrivesListFilterAndRegistration reproduces the
// production bug end to end: a config API-key credential (no auth file) is
// probed through the manager's clone-handing List(), then
//   - HiddenModelIDs over fresh manager clones must hide not_available models
//     (the /v1/models filter) while busy/transient states stay visible,
//   - re-registration must drop pruned models from the advertised set,
//   - a revive probe must restore both on the next assembly,
//   - and the merged section must survive across cycles (backoff/streak
//     state) even though nothing ever lands in the credential metadata.
func TestConfigAPIKeyProbeDrivesListFilterAndRegistration(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.OpencodeGoKey = []config.OpencodeGoKey{{APIKey: "testkey", BaseURL: "https://opencode.invalid/v1"}}
	s := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}

	auth := synthesizeOpencodeConfigAuth(t, cfg)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	s.applyCoreAuthAddOrUpdate(ctx, auth)

	before := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(before) == 0 {
		t.Fatal("opencode-go catalog not registered")
	}
	catalog := make([]string, 0, len(before))
	for _, model := range before {
		catalog = append(catalog, model.ID)
	}
	if !containsID(before, "kimi-k3") || !containsID(before, "glm-5.3") {
		t.Fatalf("static catalog fixture changed: %+v", catalog)
	}

	tierDenied := errors.New("model not enabled for this account")
	mock := &probeStubExecutor{failures: map[string]error{
		"glm-5.3":          tierDenied,
		"big-pickle":       tierDenied,
		"grok-code-fast-1": tierDenied,
	}}
	engine := modelprobe.NewEngine(nil, modelprobe.Options{
		MaxParallel:     1,
		DriverOverrides: map[string]modelprobe.RequestExecutor{"opencode-go": mock},
	})
	scheduler := modelprobe.NewScheduler(engine, &modelprobe.Store{AuthDir: t.TempDir()},
		func() []*coreauth.Auth { return s.coreManager.List() }, // deep clones, like production
		func(*coreauth.Auth) []string { return catalog },
		modelprobe.SchedulerOptions{Interval: 24 * time.Hour},
	)
	if probed := scheduler.RunCycle(ctx); probed != 1 {
		t.Fatalf("probe cycle ran %d credentials, want 1", probed)
	}
	if got := mock.callCount(); got != len(catalog) {
		t.Fatalf("probed %d models, want %d", got, len(catalog))
	}

	// Production parity: nothing lands in the stored credential's metadata —
	// the live overlay is the only carrier.
	stored, ok := s.coreManager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatal("auth vanished from manager")
	}
	if section := modelprobe.ReadSection(stored.Metadata); section != nil {
		t.Fatalf("config-key metadata unexpectedly holds a probe section: %+v", section)
	}

	hidden := modelprobe.HiddenModelIDs(s.coreManager.List())
	for _, id := range []string{"glm-5.3", "big-pickle", "grok-code-fast-1"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("%s must be hidden from /v1/models: %v", id, hidden)
		}
	}
	if _, ok := hidden["kimi-k3"]; ok {
		t.Fatalf("usable model must stay visible: %v", hidden)
	}

	// Re-registration (config reload equivalent) drops pruned models from the
	// advertised set.
	s.applyCoreAuthAddOrUpdate(ctx, stored.Clone())
	after := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(after) != 1 || after[0].ID != "kimi-k3" {
		t.Fatalf("pruned models still advertised: %+v", after)
	}

	// Next scheduled cycle honors overlay state: usable kimi-k3 is inside its
	// recheck window, only the not_available revival checks probe again.
	mock.calls = nil
	if probed := scheduler.RunCycle(ctx); probed != 1 {
		t.Fatalf("revival cycle ran %d credentials, want 1", probed)
	}
	if got := mock.callCount(); got != 3 {
		t.Fatalf("only the 3 not_available revival checks must probe, got %d", got)
	}

	// Revive: the upstream starts accepting the tier; the cycle’s usable
	// outcomes un-hide everything at the next assembly.
	mock.failures = map[string]error{}
	if probed := scheduler.RunCycle(ctx); probed != 1 {
		t.Fatalf("recovery cycle ran %d credentials, want 1", probed)
	}
	if hiddenAfter := modelprobe.HiddenModelIDs(s.coreManager.List()); len(hiddenAfter) != 0 {
		t.Fatalf("revived models must reappear in /v1/models: %v", hiddenAfter)
	}
	latest, ok := s.coreManager.GetByID(auth.ID)
	if !ok || latest == nil {
		t.Fatal("auth vanished from manager")
	}
	s.applyCoreAuthAddOrUpdate(ctx, latest.Clone())
	if restored := GlobalModelRegistry().GetModelsForClient(auth.ID); len(restored) != len(catalog) {
		t.Fatalf("full catalog must be re-advertised after revive, got %+v", restored)
	}
}
