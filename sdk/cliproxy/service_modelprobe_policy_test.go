package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestModelProbePolicyFiltersPrefixedModelsAndRestoresRecovery(t *testing.T) {
	cfg := &config.Config{}
	cfg.ModelProbe.PruneUnusable = true
	s := &Service{cfg: cfg}
	section := &modelprobe.Section{PerModel: map[string]*modelprobe.ModelOutcome{
		"devin/good": {Status: modelprobe.StatusUsable},
		"devin/busy": {Status: modelprobe.StatusLimited},
	}}
	auth := &coreauth.Auth{ID: "probe-policy-devin", Provider: "devin", Prefix: "devin", Metadata: map[string]any{modelprobe.MetadataKey: section}}
	models := []*ModelInfo{{ID: "devin/good"}, {ID: "devin/busy"}}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	if !s.registerResolvedModelsForAuthFenced(auth, newModelRegistrationFence(auth, cfg), "devin", models) {
		t.Fatal("initial publication failed")
	}
	if got := GlobalModelRegistry().GetModelsForClient(auth.ID); len(got) != 1 || got[0].ID != "devin/good" {
		t.Fatalf("prefixed model restriction was bypassed: %v", got)
	}
	section.PerModel["devin/busy"] = &modelprobe.ModelOutcome{Status: modelprobe.StatusUsable}
	if !s.registerResolvedModelsForAuthFenced(auth, newModelRegistrationFence(auth, cfg), "devin", models) {
		t.Fatal("recovery publication failed")
	}
	if got := GlobalModelRegistry().GetModelsForClient(auth.ID); len(got) != 2 {
		t.Fatal("recovered model did not re-enter routing")
	}
}

func TestModelProbePolicyAlsoAppliesToAntigravityDiscovery(t *testing.T) {
	catalog := registry.GetAntigravityModels()
	if len(catalog) < 2 {
		t.Fatal("Antigravity fixture catalog is missing")
	}
	cfg := &config.Config{}
	cfg.ForceModelPrefix = true
	cfg.ModelProbe.PruneUnusable = true
	good, bad := catalog[0].ID, catalog[1].ID
	auth := &coreauth.Auth{ID: "probe-policy-ag", Provider: "antigravity", Prefix: "ag", Metadata: map[string]any{
		modelprobe.MetadataKey: &modelprobe.Section{PerModel: map[string]*modelprobe.ModelOutcome{
			"ag/" + good: {Status: modelprobe.StatusUsable},
			"ag/" + bad:  {Status: modelprobe.StatusUnreachable},
		}},
	}}
	s := &Service{cfg: cfg}
	models := s.antigravityModelsForHintsWithConfig(auth, antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{good: {}, bad: {}}}, cfg)
	if len(models) != 1 || models[0].ID != "ag/"+good {
		t.Fatalf("Antigravity discovery restored a failed model: %v", models)
	}
}
