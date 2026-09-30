package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func claudeProbeTestAuth(id string, metadata map[string]any) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: "claude",
		Metadata: metadata,
		Attributes: map[string]string{
			coreauth.AttributeSource:        "auths/" + id,
			coreauth.AttributePath:          "auths/" + id,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	}
}

// TestModelProbePrunedModelsDropFromAdvertisedSet covers the router
// integration: a credential whose model_probe section marks models
// not_available stops advertising them while untouched models stay.
func TestModelProbePrunedModelsDropFromAdvertisedSet(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	authID := "probe-claude.json"

	live := claudeProbeTestAuth(authID, map[string]any{
		"type": "claude",
	})

	// Baseline: full claude catalog registers.
	s.applyCoreAuthAddOrUpdate(ctx, live)
	s.registerModelsForAuth(ctx, live)
	before := GlobalModelRegistry().GetModelsForClient(authID)
	if len(before) == 0 {
		t.Fatal("claude catalog not registered")
	}
	if !containsID(before, "claude-sonnet-4-6") {
		t.Fatalf("catalog fixture changed, sonnet-4-6 missing: %+v", before)
	}

	// Probe report: sonnet-4-6 is tier-blocked; other models stay.
	section := &modelprobe.Section{
		CheckedAt: "2026-10-01T00:00:00Z",
		Usable:    []string{"claude-opus-4-6", "claude-sonnet-4-5-20250929"},
		Pruned:    []string{"claude-sonnet-4-6"},
	}
	live.Metadata[modelprobe.MetadataKey] = section
	updated := live.Clone()
	s.handleAuthUpdates(ctx, nil) // no-op wiring sanity
	s.registerModelsForAuth(ctx, updated)
	after := GlobalModelRegistry().GetModelsForClient(authID)
	if containsID(after, "claude-sonnet-4-6") {
		t.Fatalf("pruned model still advertised: %+v", after)
	}
	if !containsID(after, "claude-opus-4-6") {
		t.Fatalf("usable models vanished: %+v", after)
	}

	// Re-add path: pruned list empties when the next probe marks the model usable.
	keepOnly := &modelprobe.Section{
		CheckedAt: "2026-10-02T00:00:00Z",
		Usable:    []string{"claude-sonnet-4-6"},
		PerModel: map[string]*modelprobe.ModelOutcome{
			"claude-sonnet-4-6": {Status: modelprobe.StatusUsable},
		},
	}
	merged := modelprobe.MergeSections(section, keepOnly)
	if len(merged.Pruned) != 0 {
		t.Fatalf("merge must re-add: %+v", merged)
	}
	live.Metadata[modelprobe.MetadataKey] = merged
	s.registerModelsForAuth(ctx, live.Clone())
	if !containsID(GlobalModelRegistry().GetModelsForClient(authID), "claude-sonnet-4-6") {
		t.Fatalf("re-added model not advertised again: %+v", GlobalModelRegistry().GetModelsForClient(authID))
	}
}

// TestModelProbeNoSectionPassthrough covers the zero-behavior-change rule:
// credentials without a model_probe section pass the full static catalog.
func TestModelProbeNoSectionPassthrough(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	authID := "no-probe-claude.json"
	live := claudeProbeTestAuth(authID, map[string]any{"type": "claude"})
	s.applyCoreAuthAddOrUpdate(ctx, live)
	s.registerModelsForAuth(ctx, live)
	got := GlobalModelRegistry().GetModelsForClient(authID)
	if len(got) == 0 {
		t.Fatal("no catalog registered on unprobed credential")
	}
	if !containsID(got, "claude-sonnet-4-6") {
		t.Fatalf("unprobed credentials must keep every model: %v", got)
	}
}

func containsID(models []*registry.ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}
