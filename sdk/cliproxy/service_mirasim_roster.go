package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// mirasimRosterFetchTimeout bounds the per-credential roster fetch; this is a
// credential-acquisition call (signed control GET), not an upstream inference
// request, so a deadline is policy-compliant.
const mirasimRosterFetchTimeout = 15 * time.Second

// rosterPersistMu serializes best-effort roster saves so parallel
// registrations of the same credential cannot write interleaved files.
var rosterPersistMu sync.Mutex

// mirasimRosterForAuth fetches the account model catalog for one Mirasim
// OAuth credential through the signed relay control call. Any failure returns
// nil (callers fall back to the credential's last-good roster, then to the
// static Claude catalog): a relay blip must never wipe an advertised set.
func (s *Service) mirasimRosterForAuth(ctx context.Context, a *coreauth.Auth) []string {
	if s == nil || a == nil {
		return nil
	}
	storage := mirasimauth.StorageFromMetadata(a.Metadata)
	if strings.TrimSpace(storage.AccessToken) == "" || strings.TrimSpace(storage.DevicePrivateKey) == "" {
		return nil
	}
	storage.NormalizeEndpoints()
	proxyURL := strings.TrimSpace(a.ProxyURL)
	if proxyURL == "" && s.cfg != nil {
		proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
	}
	client, errClient := mirasimauth.NewRelayClient(&storage, proxyURL)
	if errClient != nil {
		log.Warnf("mirasim: roster client build failed for %s: %v", a.ID, errClient)
		return nil
	}
	fetchCtx := ctx
	if fetchCtx == nil {
		fetchCtx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(fetchCtx, mirasimRosterFetchTimeout)
	defer cancel()
	body, _, err := client.FetchModels(fetchCtx)
	if err != nil {
		log.Warnf("mirasim: roster fetch failed for %s: %v", a.ID, err)
		return nil
	}
	ids, errParse := mirasimauth.ParseModelRosterIDs(body)
	if errParse != nil {
		log.Warnf("mirasim: roster parse failed for %s: %v", a.ID, errParse)
		return nil
	}
	return ids
}

// maybePersistMirasimRoster writes the fetched roster into the credential's
// auth file (best-effort) so an offline later fetch falls back to last-good.
// No-op for non-file credentials and when nothing changed — the watcher
// re-register that a save induces must not start a save loop.
func (s *Service) maybePersistMirasimRoster(ctx context.Context, a *coreauth.Auth, roster []string) {
	if s == nil || a == nil || strings.TrimSpace(a.FileName) == "" || len(roster) == 0 {
		return
	}
	if sameStringSet(mirasimauth.ModelsFromMetadata(a.Metadata), roster) {
		return
	}
	updated := a.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	updated.Metadata[mirasimauth.ModelsMetadataKey] = append([]string(nil), roster...)
	rosterPersistMu.Lock()
	defer rosterPersistMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, errSave := sdkAuth.GetTokenStore().Save(ctx, updated); errSave != nil {
		log.Debugf("mirasim: roster persist skipped for %s: %v", a.ID, errSave)
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, v := range a {
		seen[strings.TrimSpace(v)] = struct{}{}
	}
	for _, v := range b {
		key := strings.TrimSpace(v)
		if _, ok := seen[key]; !ok {
			return false
		}
	}
	return true
}

// buildMirasimRosterModels converts relay catalog ids into registry model
// infos; ids present in the static Claude catalog keep their metadata
// (context length, capabilities), relay-only ids get a minimal mirasim shape.
func buildMirasimRosterModels(ids []string) []*ModelInfo {
	now := time.Now().Unix()
	staticByID := make(map[string]*ModelInfo, 32)
	for _, staticModel := range registry.GetClaudeModels() {
		if staticModel == nil {
			continue
		}
		staticByID[strings.ToLower(strings.TrimSpace(staticModel.ID))] = staticModel
	}
	out := make([]*ModelInfo, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if staticModel, ok := staticByID[key]; ok && staticModel != nil {
			clone := *staticModel
			clone.OwnedBy = mirasimauth.Provider
			out = append(out, &clone)
			continue
		}
		out = append(out, &ModelInfo{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: mirasimauth.Provider,
			Type:    "claude",
		})
	}
	return out
}
