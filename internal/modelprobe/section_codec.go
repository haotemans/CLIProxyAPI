package modelprobe

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UpdateFileMetadata merges the probe section into a credential metadata map
// in-place. The metadata persists via the auth-file JSON and gets re-read by
// the watcher pipeline, so published sections pick up on the next registration.
func UpdateFileMetadata(metadata map[string]any, section *Section) map[string]any {
	if section == nil {
		return metadata
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[MetadataKey] = section
	return metadata
}

// decodeSection normalizes any stored representation of the section (in-memory
// struct, JSON-decoded map) back into a Section.
func decodeSection(raw any) (*Section, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("modelprobe: marshal section: %w", err)
	}
	var section Section
	if err := json.Unmarshal(data, &section); err != nil {
		return nil, fmt.Errorf("modelprobe: parse section: %w", err)
	}
	for i := range section.Usable {
		section.Usable[i] = strings.ToLower(strings.TrimSpace(section.Usable[i]))
	}
	section.Usable = dedupeSorted(section.Usable)
	for i := range section.Pruned {
		section.Pruned[i] = strings.ToLower(strings.TrimSpace(section.Pruned[i]))
	}
	section.Pruned = dedupeSorted(section.Pruned)
	for key := range section.PerModel {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized != key {
			section.PerModel[normalized] = section.PerModel[key]
			delete(section.PerModel, key)
		}
	}
	return &section, nil
}

// LastChecked parses section.CheckedAt if present.
func (s *Section) LastChecked() (time.Time, bool) {
	if s == nil || strings.TrimSpace(s.CheckedAt) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(s.CheckedAt))
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func dedupeSorted(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
