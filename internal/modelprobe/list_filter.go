package modelprobe

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// HiddenModelIDs aggregates client-facing model-list visibility from every
// credential's probe section. A model is hidden iff at least one probed
// credential covers it (a PerModel row exists, or the credential sits in a
// provider-blocked phase hiding its whole set) AND no usable or recoverable
// server path remains anywhere:
//
//   - never probed (no rows anywhere): visible by default
//   - busy states — limited, unreachable, auth_error: visible (may recover)
//   - not_available rows: hidden for that credential
//   - provider_blocked rows: hidden for that credential (third-party
//     clampdown; the model cannot be served until a later probe succeeds)
//   - provider_blocked: the credential's whole set hidden while the phase lasts
//
// The revive path shares this same state source: a blocked credential that a
// later probe finds healthy stops contributing IsProviderBlocked, and its
// models reappear on the very next assembly (nothing is cached here).
func HiddenModelIDs(auths []*cliproxyauth.Auth) map[string]struct{} {
	covered := make(map[string]struct{})
	servable := make(map[string]struct{})
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		section := ReadSection(auth.Metadata)
		if section == nil || len(section.PerModel) == 0 {
			continue
		}
		if section.IsProviderBlocked() {
			for id := range section.PerModel {
				if key := normalizeListID(id); key != "" {
					covered[key] = struct{}{}
				}
			}
			continue
		}
		for id, outcome := range section.PerModel {
			key := normalizeListID(id)
			if key == "" {
				continue
			}
			covered[key] = struct{}{}
			if outcome == nil || (outcome.Status != StatusNotAvailable && outcome.Status != StatusProviderBlocked) {
				servable[key] = struct{}{}
			}
		}
	}
	hidden := make(map[string]struct{}, len(covered))
	for id := range covered {
		if _, ok := servable[id]; !ok {
			hidden[id] = struct{}{}
		}
	}
	return hidden
}

func normalizeListID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
