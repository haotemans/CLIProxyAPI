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
	// StatusProviderBlocked means the provider is blocking ALL third-party
	// access for the account tier right now (e.g. Cline's periodic third-party
	// clampdown answering 401 "latest version of Cline" for every model). It is
	// NOT a credential failure and NOT "model unavailable": nothing prunes, the
	// credential is flagged instead, and the whole advertised set is hidden
	// from routing until a later probe succeeds.
	StatusProviderBlocked Status = "provider_blocked"
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
	// Failures counts consecutive non-usable outcomes since the last usable
	// probe (limited keeps the streak without growing it). The scheduler uses
	// it for per-model exponential backoff; manual runs don't reset it.
	Failures int `json:"failures,omitempty"`
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
	// CatalogSize records how many candidates the cycle probed; status UIs
	// display it as the curated catalog size for the credential.
	CatalogSize int `json:"catalog_size,omitempty"`

	// Skipped marks a whole-credential skip for the cycle (e.g. auth_error
	// backoff); the section then records when/why instead of probing.
	Skipped bool `json:"skipped,omitempty"`
	// SkipReason humanizes Skipped (e.g. "auth_error backoff (every 4th cycle)").
	SkipReason string `json:"skip_reason,omitempty"`
	// SkipCycle indexes the cycle counter the skip happened at.
	SkipCycle uint64 `json:"skip_cycle,omitempty"`
	// Skips explains per-model skips (limited backoff), keyed by model ID.
	Skips map[string]string `json:"skips,omitempty"`

	// PruneRun is set by a manual aggressive "probe and prune" run
	// (model-probe/run with prune_unused): every probed model whose outcome
	// was not usable is dropped from the credential's effective catalog on the
	// spot. Scheduled cycles never set this; their conservative rules stay
	// unchanged, and the run marker survives merges as audit.
	//
	// Recovery: the removal never poisons the upstream discovery list or the
	// static registry catalog, it only filters this credential's effective
	// set. A model refresh may rediscover ids upstream, and when the whole
	// catalog was removed the scheduler re-probes the section rows on the
	// next cycle — any model whose latest outcome turns usable re-enters the
	// catalog at the next registration, while still-broken ids stay hidden.
	// A fully removed catalog does NOT disable the credential.
	PruneRun *PruneRun `json:"prune_run,omitempty"`
}

// PruneRun audits one manual aggressive prune: when it ran and which models
// stopped being advertised (usable outcomes were kept, all others removed).
type PruneRun struct {
	// At is the RFC3339 moment the aggressive run finished.
	At string `json:"at"`
	// Removed lists every model id deleted from the effective catalog
	// (lowercase, deduped, sorted). Empty means everything probed usable.
	Removed []string `json:"removed"`
}

// IsProviderBlocked reports whether EVERY recorded outcome is provider_blocked:
// the credential is in a full third-party block phase (all ads drop out of
// routing until a next probe succeeds). Empty sections are never blocked.
func (s *Section) IsProviderBlocked() bool {
	if s == nil || len(s.PerModel) == 0 {
		return false
	}
	for _, outcome := range s.PerModel {
		if outcome == nil || outcome.Status != StatusProviderBlocked {
			return false
		}
	}
	return true
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
// removed. It is a passthrough when no probe section exists or when neither
// the conservative pruned list nor an aggressive prune_run marker applies.
// Ids dropped by a manual prune_run stay hidden only while their latest
// probed outcome is not usable — a model re-probed usable re-enters the
// catalog (self-healing), the marker stays as audit.
func FilterPrunedForAuth(metadata map[string]any, models []*registry.ModelInfo) []*registry.ModelInfo {
	return FilterPrunedBySection(ReadSection(metadata), models)
}

// FilterPrunedBySection is the section-taking form of FilterPrunedForAuth,
// for callers that resolve sections through SectionForAuth (config API-key
// credentials carry their section in the live overlay, not the metadata).
func FilterPrunedBySection(section *Section, models []*registry.ModelInfo) []*registry.ModelInfo {
	if section == nil || (len(section.Pruned) == 0 && len(section.pruneRemoved()) == 0) {
		return models
	}
	pruned := make(map[string]struct{}, len(section.Pruned))
	for _, id := range section.Pruned {
		pruned[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	removed := section.pruneRemoved()
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		if _, blocked := pruned[key]; blocked {
			continue
		}
		if _, gone := removed[key]; gone && !section.usableNow(key) {
			continue
		}
		out = append(out, model)
	}
	return out
}

// pruneRemoved returns the aggressively removed id set (empty without a
// prune_run marker).
func (s *Section) pruneRemoved() map[string]struct{} {
	out := map[string]struct{}{}
	if s == nil || s.PruneRun == nil {
		return out
	}
	for _, id := range s.PruneRun.Removed {
		if key := strings.ToLower(strings.TrimSpace(id)); key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

// usableNow reports whether the credential's latest recorded outcome for the
// model is usable.
func (s *Section) usableNow(key string) bool {
	if s == nil || s.PerModel == nil {
		return false
	}
	outcome := s.PerModel[key]
	return outcome != nil && outcome.Status == StatusUsable
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
