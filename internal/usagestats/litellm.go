package usagestats

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// LiteLLMPricesURL is the upstream price catalog synced into overrides.
// A var (not const) so tests can point the sync at a local fixture server.
var LiteLLMPricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// PriceSourceLitellm marks config pricing entries written by the LiteLLM
// sync; empty source means user-managed.
const PriceSourceLitellm = "litellm"

// litellmEntry mirrors the per-model fields we consume from the LiteLLM
// catalog (costs are USD per single token).
type litellmEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
}

// MapLitellmPrices parses the LiteLLM model_prices_and_context_window.json
// payload into USD-per-1M-token prices keyed by lowercase model id. Entries
// missing a positive input or output cost are skipped and counted.
func MapLitellmPrices(data []byte) (map[string]Price, int, error) {
	var raw map[string]litellmEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("usagestats: parse litellm catalog: %w", err)
	}
	prices := make(map[string]Price, len(raw))
	skipped := 0
	for model, entry := range raw {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" || key == "sample_spec" {
			continue
		}
		if entry.InputCostPerToken <= 0 || entry.OutputCostPerToken <= 0 {
			skipped++
			continue
		}
		prices[key] = Price{
			Input:  entry.InputCostPerToken * 1_000_000,
			Output: entry.OutputCostPerToken * 1_000_000,
		}
	}
	return prices, skipped, nil
}

// MergeLitellmPrices merges LiteLLM prices into the config pricing map as
// overrides sourced "litellm". User-managed entries (Source != litellm) are
// never clobbered; existing litellm entries are refreshed in place. The map
// is mutated; added counts brand-new keys, updated counts refreshed litellm
// keys, and skippedUser counts catalog entries blocked by a user override.
func MergeLitellmPrices(custom map[string]config.UsageStatsPrice, prices map[string]Price) (added, updated, skippedUser int) {
	for model, price := range prices {
		existing, ok := custom[model]
		if ok && existing.Source != PriceSourceLitellm {
			skippedUser++
			continue
		}
		custom[model] = config.UsageStatsPrice{
			Input:  price.Input,
			Output: price.Output,
			Source: PriceSourceLitellm,
		}
		if ok {
			updated++
		} else {
			added++
		}
	}
	return added, updated, skippedUser
}
