package modelprobe

import (
	"strings"
	"sync"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// liveSections is the process-local overlay holding the latest merged probe
// section per credential ID.
//
// File-backed credentials persist their section into the auth JSON and the
// watcher pipeline reloads it into the auth metadata, so every reader sees it
// through the credential itself. Config API-key credentials (api-keys.*
// families such as opencode-go) have no durable file, and the auth manager
// only hands out deep clones (Clone copies the metadata map), so a section
// written onto a probe-cycle clone is discarded with the clone: nothing ever
// reaches the stored auth or any later reader. This overlay is the carrier
// for those credentials. It is consulted only when the credential has no file
// backend, keeping file-backed credentials on their established metadata
// path; it survives manager rebuilds (config reloads re-synthesize config
// auths without probe metadata) but intentionally not process restarts.
var liveSections = struct {
	sync.RWMutex
	byAuth map[string]*Section
}{byAuth: make(map[string]*Section)}

// recordLiveSection stores the latest merged section for a credential.
func recordLiveSection(authID string, section *Section) {
	authID = strings.TrimSpace(authID)
	if authID == "" || section == nil {
		return
	}
	if copied := copySection(section); copied != nil {
		section = copied
	}
	liveSections.Lock()
	liveSections.byAuth[authID] = section
	liveSections.Unlock()
}

// liveSectionFor returns a deep copy of the recorded live section, if any.
func liveSectionFor(authID string) *Section {
	liveSections.RLock()
	section := liveSections.byAuth[strings.TrimSpace(authID)]
	liveSections.RUnlock()
	return copySection(section)
}

// pruneLiveSections drops overlay entries for credentials no longer present,
// keeping the overlay bounded as credentials are removed.
func pruneLiveSections(keep map[string]struct{}) {
	liveSections.Lock()
	for id := range liveSections.byAuth {
		if _, ok := keep[id]; !ok {
			delete(liveSections.byAuth, id)
		}
	}
	liveSections.Unlock()
}

// SectionForAuth resolves the freshest probe section for a credential.
// File-backed credentials read from their metadata (kept in sync with the
// persisted auth JSON by the watcher); credentials without a file backend
// fall back to the process-local live overlay written by Store.ApplyOutcome.
// Absence still means "never probed" and must not change routing behavior.
func SectionForAuth(auth *cliproxyauth.Auth) *Section {
	if auth == nil {
		return nil
	}
	if strings.TrimSpace(auth.FileName) == "" {
		if live := liveSectionFor(auth.ID); live != nil {
			return live
		}
	}
	return ReadSection(auth.Metadata)
}

// copySection deep-copies a section through the JSON codec so overlay entries
// never alias scheduler- or manager-owned rows.
func copySection(section *Section) *Section {
	if section == nil {
		return nil
	}
	copied, err := decodeSection(section)
	if err != nil {
		return section
	}
	return copied
}
