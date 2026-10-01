package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	opencode "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/opencode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// zenFreeModelFetchTimeout bounds the anonymous-tier model listing fetch, the
// same credential-acquisition budget the cline per-key discovery uses.
const zenFreeModelFetchTimeout = 15 * time.Second

// zenFreeCatalogCache keeps the last good *-free id set per zen base URL so a
// transient feed outage does not strip the advertised models.
var zenFreeCatalogCache = struct {
	sync.Mutex
	byBase map[string][]string
}{byBase: make(map[string][]string)}

// isZenFreeAuth reports whether the credential is the anonymous Zen free tier
// (the literal "public" API key).
func isZenFreeAuth(a *coreauth.Auth) bool {
	if a == nil || a.Attributes == nil {
		return false
	}
	return opencode.IsZenFreeAPIKey(strings.TrimSpace(a.Attributes["api_key"]))
}

// zenFreeModelsForAuth computes the anonymous credential's effective set:
// every model refresh cycle fetches the zen feed and intersects the current
// *-free ids with the key's enabled models (config-listed names, or the whole
// free set when nothing is listed). A failed fetch keeps the last good set;
// with no known set nothing is advertised (the paid static catalog must never
// leak onto the anonymous credential).
func (s *Service) zenFreeModelsForAuth(ctx context.Context, a *coreauth.Auth, entry *config.OpencodeGoKey) []*ModelInfo {
	freeIDs := s.zenFreeModelIDs(ctx, a)
	if len(freeIDs) == 0 {
		return nil
	}
	if entry != nil && len(entry.Models) > 0 {
		free := make(map[string]struct{}, len(freeIDs))
		for _, id := range freeIDs {
			free[strings.ToLower(id)] = struct{}{}
		}
		filtered := make([]config.OpencodeGoModel, 0, len(entry.Models))
		for _, model := range entry.Models {
			if _, ok := free[strings.ToLower(strings.TrimSpace(model.Name))]; ok {
				filtered = append(filtered, model)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		return buildConfigModels(filtered, "opencode-go", "codex", "opencode-go")
	}
	return buildZenFreeModelInfos(freeIDs)
}

// zenFreeModelIDs fetches the anonymous zen model listing for the credential's
// base URL on every call; successful responses refresh the zero-price table
// and the per-base cache, failures fall back to the cached snapshot.
func (s *Service) zenFreeModelIDs(ctx context.Context, a *coreauth.Auth) []string {
	base := opencode.ZenFreeDefaultBaseURL
	if a != nil && a.Attributes != nil {
		if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
			base = b
		}
	}
	ids, err := s.fetchZenFreeModelIDs(ctx, a, base)
	if err == nil {
		zenFreeCatalogCache.Lock()
		zenFreeCatalogCache.byBase[base] = ids
		zenFreeCatalogCache.Unlock()
		usagestats.RegisterZenFreeModelPrices(ids)
		return ids
	}
	zenFreeCatalogCache.Lock()
	cached := zenFreeCatalogCache.byBase[base]
	zenFreeCatalogCache.Unlock()
	if len(cached) > 0 {
		log.Warnf("opencode-go: zen free catalog fetch failed (%v); keeping %d cached free model(s)", err, len(cached))
		return cached
	}
	log.Warnf("opencode-go: zen free catalog fetch failed and no cache available: %v", err)
	return nil
}

// fetchZenFreeModelIDs performs one GET {base}/models (anonymous endpoint, no
// credential needed) and parses the *-free ids.
func (s *Service) fetchZenFreeModelIDs(ctx context.Context, a *coreauth.Auth, base string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, zenFreeModelFetchTimeout)
	defer cancel()

	proxyURL := ""
	if a != nil {
		proxyURL = strings.TrimSpace(a.ProxyURL)
	}
	if proxyURL == "" && s.cfg != nil {
		proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
	}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, opencode.ZenFreeCatalogURL(base), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("opencode-go: zen model listing close body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &zenFreeFetchStatusError{status: resp.StatusCode}
	}
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil, errRead
	}
	ids := opencode.ParseZenFreeModelIDs(raw)
	if len(ids) == 0 {
		return nil, &zenFreeFetchStatusError{status: resp.StatusCode}
	}
	log.Infof("opencode-go: zen free catalog at %s exposes %d anonymous model(s)", base, len(ids))
	return ids, nil
}

type zenFreeFetchStatusError struct{ status int }

func (e *zenFreeFetchStatusError) Error() string {
	return fmt.Sprintf("zen model listing returned no usable result (status %d)", e.status)
}

// buildZenFreeModelInfos turns discovered free ids into registry model infos;
// ids present in the static catalog keep their static metadata.
func buildZenFreeModelInfos(ids []string) []*ModelInfo {
	now := time.Now().Unix()
	staticByID := make(map[string]*ModelInfo, 8)
	for _, staticModel := range registry.GetOpencodeGoModels() {
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
			out = append(out, &clone)
			continue
		}
		out = append(out, &ModelInfo{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: "opencode-go",
			Type:    "opencode-go",
		})
	}
	return out
}
