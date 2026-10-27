package modelprobe

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// FilterUnusableBySection temporarily hides failed, already-probed models.
// Discovery remains intact, so the scheduler can retry and restore them.
// Unknown and never-probed models are not falsely declared unavailable.
func FilterUnusableBySection(section *Section, models []*registry.ModelInfo) []*registry.ModelInfo {
	if section == nil {
		return models
	}
	filtered := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		outcome := section.PerModel[strings.ToLower(strings.TrimSpace(model.ID))]
		if outcome == nil || outcome.Status == StatusUsable || outcome.Status == StatusUnknown {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// Media endpoints cannot be tested through chat without false denials or
// starting billable generation jobs. Leave them unverified instead.
func requiresMediaProbe(provider, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	switch normalizeProvider(provider) {
	case "xai":
		return strings.HasPrefix(model, "grok-imagine") || strings.HasPrefix(model, "grok-composer") ||
			strings.HasPrefix(model, "grok-voice") || model == "grok-tts"
	case "antigravity":
		return strings.Contains(model, "-image")
	}
	return false
}
