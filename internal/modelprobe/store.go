package modelprobe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Store applies probe outcomes to credential metadata and (where reachable)
// to the credential's JSON auth file for durable persistence.
type Store struct {
	// AuthDir is the credential directory (enabled writes go <AuthDir>/<name>.json).
	AuthDir string
}

// ApplyOutcome merges the cycle's Section into the credential metadata
// (non-destructive across cycles: prior per-model rows for unchecked models
// stay; newly usable models clear prior pruning), then best-effort writes
// the auth file. Returns the merged Section.
func (s *Store) ApplyOutcome(auth *cliproxyauth.Auth, section *Section) *Section {
	if auth == nil || section == nil {
		return section
	}
	merged := MergeSections(ReadSection(auth.Metadata), section)
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata[MetadataKey] = merged
	if err := s.persistToFile(auth, merged); err != nil {
		log.Debugf("modelprobe: durable write skipped for %s: %v", auth.ID, err)
	}
	return merged
}

// MergeSections folds a fresh cycle into the previous section in-place:
// fresh outcomes overwrite their own rows; rows for models not re-probed stay;
// Pruned = only models whose latest status is not_available.
func MergeSections(previous, fresh *Section) *Section {
	if fresh == nil {
		return previous
	}
	if previous == nil {
		return fresh
	}
	combined := &Section{
		CheckedAt:  fresh.CheckedAt,
		Usable:     append([]string(nil), fresh.Usable...),
		PerModel:   make(map[string]*ModelOutcome, len(previous.PerModel)+len(fresh.PerModel)),
		Skipped:    fresh.Skipped,
		SkipReason: fresh.SkipReason,
		SkipCycle:  fresh.SkipCycle,
		Skips:      fresh.Skips,
	}
	stillUnavailable := make(map[string]struct{}, len(previous.Pruned))
	for id, outcome := range previous.PerModel {
		combined.PerModel[id] = outcome
	}
	for id, outcome := range fresh.PerModel {
		combined.PerModel[id] = outcome
	}
	for id, outcome := range combined.PerModel {
		if outcome == nil || outcome.Status != StatusNotAvailable {
			continue
		}
		stillUnavailable[id] = struct{}{}
		combined.Pruned = append(combined.Pruned, id)
	}
	combined.Pruned = dedupeSorted(append([]string(nil), combined.Pruned...))
	combined.Usable = dedupeSorted(combined.Usable)
	return combined
}

// persistToFile rewrites the credential's JSON file with the updated probe
// section. It only runs when the credential maps to a readable file inside
// AuthDir; non-file backends simply skip (in-memory metadata still updated).
func (s *Store) persistToFile(auth *cliproxyauth.Auth, section *Section) error {
	if s == nil || s.AuthDir == "" || auth == nil || auth.FileName == "" {
		return fmt.Errorf("no file backend available")
	}
	path := filepath.Join(s.AuthDir, filepath.Base(auth.FileName))
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}
	metadata[MetadataKey] = section
	out, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth file: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write temp auth file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	return nil
}

// SummaryForAuth renders the status snapshot for management output.
func SummaryForAuth(auth *cliproxyauth.Auth) map[string]any {
	section := ReadSection(auth.Metadata)
	if section == nil {
		return map[string]any{"probed": false}
	}
	out := map[string]any{
		"probed":     true,
		"checked_at": section.CheckedAt,
		"usable":     len(section.Usable),
		"pruned":     len(section.Pruned),
	}
	if len(section.Pruned) > 0 {
		out["pruned_models"] = section.Pruned
	}
	return out
}

// DetailRows renders per-model rows for inspection responses.
func DetailRows(section *Section) []map[string]any {
	if section == nil {
		return nil
	}
	rows := make([]map[string]any, 0, len(section.PerModel))
	for id, outcome := range section.PerModel {
		if outcome == nil {
			continue
		}
		rows = append(rows, map[string]any{
			"model":      id,
			"status":     string(outcome.Status),
			"error":      outcome.Error,
			"checked_ms": outcome.CheckedMS,
		})
	}
	return rows
}
