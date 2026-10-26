package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRegisterModels_MirasimClientRestrictionsWithoutPrunedList(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID: "mirasim-client-restriction-test", Provider: "mirasim",
		Metadata: map[string]any{
			"mirasim_models": []string{"kimi-k3", "gpt-6-astra"},
			modelprobe.MetadataKey: &modelprobe.Section{PerModel: map[string]*modelprobe.ModelOutcome{
				"kimi-k3":     {Status: modelprobe.StatusUsable},
				"gpt-6-astra": {Status: modelprobe.StatusAuthError, Error: "this client is not supported; use the mirasim client or Claude Code"},
			}},
		},
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	s.registerModelsForAuth(context.Background(), auth)
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) != 1 || models[0].ID != "kimi-k3" {
		t.Fatalf("effective roster must contain only the usable model: %v", models)
	}
}
