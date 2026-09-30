// Package modelprobe probes real model capability per credential: it sends
// one minimal live request per advertised model through the provider's normal
// executor path, keeps usable models, prunes not-available ones, and persists
// the result into the credential file's `model_probe` section. Routing and
// the model catalog degrade gracefully when probing is absent or disabled.
package modelprobe

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// MetadataKey is the credential-file metadata section written by probes.
const MetadataKey = "model_probe"

// Status classifies one probed model.
type Status string

const (
	// StatusUsable means the provider accepted the probe request.
	StatusUsable Status = "usable"
	// StatusNotAvailable means the provider explicitly denied the model for
	// this account (tier block, model not enabled, unknown model).
	StatusNotAvailable Status = "not_available"
	// StatusLimited means the account hit a quota/rate cap during probing; the
	// model stays advertised and gets rechecked next cycle.
	StatusLimited Status = "limited"
	// StatusAuthError means the credential itself was rejected; models keep
	// being advertised and the section records the credential problem.
	StatusAuthError Status = "auth_error"
	// StatusUnreachable means the upstream could not be reached; keep the
	// model and retry next cycle.
	StatusUnreachable Status = "unreachable"
)

// ModelOutcome is the per-model result of one probe attempt.
type ModelOutcome struct {
	Status    Status    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Checked   time.Time `json:"-"`
	CheckedMS int64     `json:"checked_ms,omitempty"`
}

// Section persists probe results into the credential metadata file.
type Section struct {
	// CheckedAt is the RFC3339 moment the last cycle finished.
	CheckedAt string `json:"checked_at"`
	// Usable lists model IDs verified usable by the probe.
	Usable []string `json:"usable,omitempty"`
	// Pruned lists model IDs currently hidden from this credential because
	// the probe classified them not_available.
	Pruned []string `json:"pruned,omitempty"`
	// PerModel records every model outcome (usable included) keyed by model ID.
	PerModel map[string]*ModelOutcome `json:"models,omitempty"`
}

// ReadSection extracts the probe section from credential metadata. Absence
// means "never probed" and the router must not change behavior.
func ReadSection(metadata map[string]any) *Section {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[MetadataKey]
	if !ok || raw == nil {
		return nil
	}
	decoded, err := decodeSection(raw)
	if err != nil {
		return nil
	}
	return decoded
}

// FilterPrunedForAuth returns models with the credential's pruned list
// removed. It is a passthrough when no probe section exists or when the
// pruned list is empty.
func FilterPrunedForAuth(metadata map[string]any, models []*registry.ModelInfo) []*registry.ModelInfo {
	section := ReadSection(metadata)
	if section == nil || len(section.Pruned) == 0 {
		return models
	}
	pruned := make(map[string]struct{}, len(section.Pruned))
	for _, id := range section.Pruned {
		pruned[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, blocked := pruned[strings.ToLower(strings.TrimSpace(model.ID))]; blocked {
			continue
		}
		out = append(out, model)
	}
	return out
}

// FromOutcomes flattens per-model outcomes into a persisted Section.
func FromOutcomes(checked time.Time, outcomes map[string]ModelOutcome, pruneIDs ...string) *Section {
	section := &Section{
		CheckedAt: checked.UTC().Format(time.RFC3339),
		PerModel:  make(map[string]*ModelOutcome, len(outcomes)),
	}
	blocked := make(map[string]struct{}, len(pruneIDs))
	for _, id := range pruneIDs {
		blocked[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	for id, outcome := range outcomes {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		processed := outcome
		processed.CheckedMS = checked.UnixMilli()
		section.PerModel[key] = &processed
		switch outcome.Status {
		case StatusUsable:
			section.Usable = append(section.Usable, key)
		case StatusNotAvailable:
			if _, prune := blocked[key]; prune {
				section.Pruned = append(section.Pruned, key)
			}
		}
	}
	return section
}

// MergeCheckedAt confirms checked_ms tracking when outcomes lack timestamps.
func (s *Section) MergeCheckedAt(checked time.Time) {
	if s == nil {
		return
	}
	ms := checked.UnixMilli()
	for _, outcome := range s.PerModel {
		if outcome != nil && outcome.CheckedMS == 0 {
			outcome.CheckedMS = ms
		}
	}
}
