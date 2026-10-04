package mirasim

import (
	"encoding/json"
	"math"
	"strings"
)

// ModelLimitWindow is one budget window the relay's /v1/limits lane reports.
type ModelLimitWindow struct {
	Name        string
	Budget      float64
	Used        float64
	ModelScoped bool
}

// ParseModelLimitWindows decodes the vendor limits body into its budget
// windows. Malformed payloads and windows without budget/used values are
// dropped (worst case: no preflight signal, the inference probe decides).
func ParseModelLimitWindows(raw []byte) []ModelLimitWindow {
	var payload struct {
		Windows []struct {
			Name        string   `json:"name"`
			Budget      *float64 `json:"budget"`
			Used        *float64 `json:"used"`
			ModelScoped bool     `json:"model_scoped"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	out := make([]ModelLimitWindow, 0, len(payload.Windows))
	for _, window := range payload.Windows {
		name := strings.TrimSpace(window.Name)
		if name == "" || window.Budget == nil || window.Used == nil {
			continue
		}
		budget, used := *window.Budget, *window.Used
		if math.IsNaN(budget) || math.IsInf(budget, 0) || budget < 0 ||
			math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
			continue
		}
		out = append(out, ModelLimitWindow{
			Name:        strings.ToLower(name),
			Budget:      budget,
			Used:        used,
			ModelScoped: window.ModelScoped,
		})
	}
	return out
}

// ModelLimitExhausted reports whether the relay's current limits mark the
// model as out of budget right now: a model-scoped window named after the
// model whose use has reached its budget. Windows are optional signals — only
// an explicit exhausted one trips, so missing data never blocks a probe.
func ModelLimitExhausted(windows []ModelLimitWindow, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, window := range windows {
		if !window.ModelScoped || window.Name != model {
			continue
		}
		if window.Budget > 0 && window.Used >= window.Budget {
			return true
		}
	}
	return false
}
