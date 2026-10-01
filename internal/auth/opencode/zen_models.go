// Package opencode holds helpers for the OpenCode Zen relay family.
package opencode

import (
	"encoding/json"
	"sort"
	"strings"
)

// ZenFreeAPIKey is the literal key that switches an api-keys.opencode-go entry
// into the anonymous OpenCode Zen free tier. The match is exact and
// case-sensitive: "PUBLIC" or whitespace-padded variants stay paid keys.
const ZenFreeAPIKey = "public"

// ZenFreeDefaultBaseURL is the anonymous Zen upstream root (paid Zen Go lives
// under /zen/go/v1).
const ZenFreeDefaultBaseURL = "https://opencode.ai/zen/v1"

// zenFreeModelSuffix marks ids the anonymous tier may call.
const zenFreeModelSuffix = "-free"

// IsZenFreeAPIKey reports whether the raw (untrimmed) key value selects the
// anonymous free tier.
func IsZenFreeAPIKey(raw string) bool {
	return raw == ZenFreeAPIKey
}

// IsZenFreeModelID reports whether a zen feed id belongs to the free tier.
func IsZenFreeModelID(id string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(id)), zenFreeModelSuffix)
}

// ZenFreeCatalogURL joins the model listing endpoint against a zen base URL.
func ZenFreeCatalogURL(baseURL string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = ZenFreeDefaultBaseURL
	}
	return strings.TrimSuffix(base, "/") + "/models"
}

// ParseZenFreeModelIDs parses the zen /v1/models payload (OpenAI list shape,
// {"data": [{"id": ...}]}; a bare array is tolerated) and returns the sorted,
// deduplicated free-tier ids.
func ParseZenFreeModelIDs(raw []byte) []string {
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil || len(listed.Data) == 0 {
		var flat []struct {
			ID string `json:"id"`
		}
		if errArr := json.Unmarshal(raw, &flat); errArr != nil {
			return nil
		}
		listed.Data = flat
	}
	seen := make(map[string]struct{}, len(listed.Data))
	out := make([]string, 0, len(listed.Data))
	for _, entry := range listed.Data {
		id := strings.TrimSpace(entry.ID)
		if !IsZenFreeModelID(id) {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
