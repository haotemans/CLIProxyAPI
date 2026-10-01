package usagestats

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestMapLitellmPrices(t *testing.T) {
	payload := []byte(`{
		"gpt-5": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001},
		"claude-opus-4-1": {"input_cost_per_token": 0.000015, "output_cost_per_token": 0.000075, "max_tokens": 200000},
		"free-model": {"input_cost_per_token": 0, "output_cost_per_token": 0},
		"input-only": {"input_cost_per_token": 0.000001},
		"sample_spec": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000001}
	}`)

	prices, skipped, err := MapLitellmPrices(payload)
	if err != nil {
		t.Fatalf("MapLitellmPrices: %v", err)
	}
	if len(prices) != 2 {
		t.Fatalf("mapped %d prices, want 2: %+v", len(prices), prices)
	}
	if got := prices["gpt-5"]; got.Input != 1.25 || got.Output != 10 {
		t.Fatalf("gpt-5 = %+v, want {1.25 10}", got)
	}
	if got := prices["claude-opus-4-1"]; got.Input != 15 || got.Output != 75 {
		t.Fatalf("claude-opus-4-1 = %+v, want {15 75}", got)
	}
	// free-model and input-only lack both positive costs; sample_spec is ignored.
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}

	if _, _, err = MapLitellmPrices([]byte(`{not json`)); err == nil {
		t.Fatal("expected parse error for invalid payload")
	}
}

func TestMergeLitellmPrices_NeverClobbersUserOverrides(t *testing.T) {
	custom := map[string]config.UsageStatsPrice{
		"gpt-5":          {Input: 9, Output: 99},                            // user override
		"claude-haiku-3": {Input: 1, Output: 5, Source: PriceSourceLitellm}, // previous sync
	}
	prices := map[string]Price{
		"gpt-5":          {Input: 1.25, Output: 10},
		"claude-haiku-3": {Input: 0.8, Output: 4},
		"gemini-3-pro":   {Input: 2, Output: 12},
	}

	added, updated, skippedUser := MergeLitellmPrices(custom, prices)
	if added != 1 || updated != 1 || skippedUser != 1 {
		t.Fatalf("added=%d updated=%d skippedUser=%d, want 1/1/1", added, updated, skippedUser)
	}
	if got := custom["gpt-5"]; got.Input != 9 || got.Output != 99 || got.Source != "" {
		t.Fatalf("user override clobbered: %+v", got)
	}
	if got := custom["claude-haiku-3"]; got.Input != 0.8 || got.Output != 4 || got.Source != PriceSourceLitellm {
		t.Fatalf("litellm entry not refreshed: %+v", got)
	}
	if got := custom["gemini-3-pro"]; got.Input != 2 || got.Output != 12 || got.Source != PriceSourceLitellm {
		t.Fatalf("new entry not added: %+v", got)
	}
}
