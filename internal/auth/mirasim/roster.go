package mirasim

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ParseModelRosterIDs parses the relay's account model catalog (GET
// /v1/models) into the servable ids the official client would offer, the same
// way the donor plugin narrows it: bare-id or object entries under "data" (or
// "models"), reserved catalog ids dropped, namespaced ids ("" vendor" paths)
// dropped, and a dated twin (claude-haiku-4-5-20251001) dropped when the
// plain id it duplicates is listed beside it. Registration and probing use
// these ids verbatim.
//
// Failure modes: invalid JSON or zero servable ids → error, so callers fall
// back to the credential's last-good roster instead of wiping it.
func ParseModelRosterIDs(raw []byte) ([]string, error) {
	var payload struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode Mirasim model catalog: %w", err)
	}
	items := payload.Data
	if len(items) == 0 {
		items = payload.Models
	}
	parsed := make([]string, 0, len(items))
	for _, item := range items {
		var entry struct {
			ID string `json:"id"`
		}
		if errObject := json.Unmarshal(item, &entry); errObject != nil || strings.TrimSpace(entry.ID) == "" {
			var id string
			if errString := json.Unmarshal(item, &id); errString != nil {
				continue
			}
			entry.ID = id
		}
		if id := strings.TrimSpace(entry.ID); id != "" {
			parsed = append(parsed, id)
		}
	}
	ids := servableModelIDs(parsed)
	if len(ids) == 0 {
		return nil, fmt.Errorf("Mirasim model catalog contains no servable models")
	}
	return ids, nil
}

// datedModelSuffix matches the release date a catalog appends to a model's
// dated twin, as in claude-haiku-4-5-20251001.
var datedModelSuffix = regexp.MustCompile(`-20\d{6}$`)

// reservedCatalogIDs are entries the catalog lists that name no servable model.
var reservedCatalogIDs = map[string]struct{}{
	"*":                      {},
	"gpt-4o-mini":            {},
	"gpt-4o-mini-openrouter": {},
}

func servableModelIDs(parsed []string) []string {
	undated := make(map[string]struct{}, len(parsed))
	for _, id := range parsed {
		if !strings.Contains(id, "/") && !datedModelSuffix.MatchString(id) {
			undated[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(parsed))
	seen := make(map[string]struct{}, len(parsed))
	for _, id := range parsed {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		if _, reserved := reservedCatalogIDs[id]; reserved || strings.Contains(id, "/") {
			continue
		}
		if datedModelSuffix.MatchString(id) {
			if _, twin := undated[datedModelSuffix.ReplaceAllString(id, "")]; twin {
				continue
			}
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ModelsMetadataKey is the auth metadata field persisting the last-good
// relay-fetched roster for one OAuth credential (used as the fallback when a
// roster refresh fails).
const ModelsMetadataKey = "mirasim_models"

// ModelsFromMetadata reads the persisted roster ids from auth metadata.
func ModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[ModelsMetadataKey]
	if !ok || raw == nil {
		return nil
	}
	ids := make([]string, 0, 8)
	add := func(value string) {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	switch entries := raw.(type) {
	case []string:
		for _, entry := range entries {
			add(entry)
		}
	case []any:
		for _, entry := range entries {
			if value, ok := entry.(string); ok {
				add(value)
			}
		}
	}
	return ids
}
