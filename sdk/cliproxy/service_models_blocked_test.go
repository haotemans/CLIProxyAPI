package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// blockedPhaseAuth mirrors a cline OAuth credential whose probe sections
// record the given outcome mix; registration follows the metadata snapshot.
func blockedPhaseAuth(id string, outcomes map[string]string) *coreauth.Auth {
	perModel := make(map[string]*modelprobe.ModelOutcome, len(outcomes))
	for id, status := range outcomes {
		perModel[id] = &modelprobe.ModelOutcome{Status: modelprobe.Status(status)}
	}
	return &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: "cline",
		Label:    "user@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"auth_kind":    "oauth",
			"access_token": "tok",
			modelprobe.MetadataKey: &modelprobe.Section{
				CheckedAt:   "2026-10-01T00:00:00Z",
				CatalogSize: len(outcomes),
				PerModel:    perModel,
			},
		},
	}
}

func TestRegisterModels_ProviderBlockedHidesAndAutoRevives(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	authID := "cline-blocked-test.json"

	blocked := blockedPhaseAuth(authID, map[string]string{
		"cline-pass/claude-sonnet-4-6": "provider_blocked",
		"cline-free/glm-5":             "provider_blocked",
	})
	s.applyCoreAuthAddOrUpdate(ctx, blocked)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(authID)) > 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	if got := len(GlobalModelRegistry().GetModelsForClient(authID)); got != 0 {
		t.Fatalf("fully-blocked credential must advertise EMPTY set, got %d models", got)
	}

	// Recovery: next cycle's outcomes contain at least one success; the
	// merged section no longer blocks, so the catalog comes back.
	revived := blockedPhaseAuth(authID, map[string]string{
		"cline-pass/claude-sonnet-4-6": "usable",
		"cline-free/glm-5":             "provider_blocked",
	})
	s.applyCoreAuthAddOrUpdate(ctx, revived)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(authID)) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(GlobalModelRegistry().GetModelsForClient(authID)); got == 0 {
		t.Fatal("recovered credential must advertise the catalog again (auto-revive)")
	}
}

func TestRegisterModels_MixedBlockedAndPrunedKeepsPruningOnly(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	authID := "cline-mixed-test.json"

	// A non-fully-blocked mix: usable succeeds locally is fine; pruned hides
	// individually; provider_blocked alone does NOT hide the set anymore.
	mixed := blockedPhaseAuth(authID, map[string]string{
		"cline-pass/claude-sonnet-4-6": "not_available",
		"cline-free/glm-5":             "provider_blocked",
		"cline-free/kimi-k3":           "usable",
	})
	s.applyCoreAuthAddOrUpdate(ctx, mixed)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(authID)) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	models := GlobalModelRegistry().GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("mixed section must still advertise (block phase hides only when ALL are blocked)")
	}
	for _, model := range models {
		if model.ID == "cline-pass/claude-sonnet-4-6" {
			t.Fatal("pruned model must stay hidden")
		}
	}
}
