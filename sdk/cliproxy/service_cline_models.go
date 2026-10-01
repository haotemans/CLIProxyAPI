package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	clineauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// clinePaidTiersEnabled reports whether subscription-gated families
// (clinePass + clineCloud) join a credential's catalog. api-keys.cline
// credentials (auth kind apikey — the subscription keys those families exist
// for) always see them; OAuth auth files see them when the per-credential
// include_paid_tiers flag or the global cline.include-paid-tiers knob is set.
func clinePaidTiersEnabled(cfg *config.Config, auth *coreauth.Auth) bool {
	if auth != nil && auth.AuthKind() == coreauth.AuthKindAPIKey {
		return true
	}
	if auth != nil {
		if auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes["include_paid_tiers"]), "true") {
			return true
		}
		if auth.Metadata != nil {
			if flag, ok := auth.Metadata["include_paid_tiers"].(bool); ok && flag {
				return true
			}
		}
	}
	if cfg != nil && cfg.Cline.IncludePaidTiers != nil {
		return *cfg.Cline.IncludePaidTiers
	}
	return false
}

// clineModelBucketMarkers are persisted next to the detected catalog so the
// probe engine and the panel can read the curated tier composition without
// refetching: {"recommended": n, "free": n, "paid": n}.
const ModelsTiersMetadataKey = "models_tiers"

// clineCatalogForAccount builds the curated per-account catalog: the curated
// recommended-models feed is the primary source (recommended + free always,
// paid tiers per credential/knob), with the full /ai/cline/models catalog as
// fallback when the feed fails. Returns (models, tier counts, error).
func (s *Service) clineCatalogForAccount(ctx context.Context, svc *clineauth.ClineAuth, auth *coreauth.Auth, token string) ([]clineauth.ClineModelInfo, map[string]int, error) {
	catalog, errFeed := svc.FetchRecommendedModels(ctx, token)
	if errFeed != nil {
		// The full catalog is the imperative fallback: some deployments mirror
		// old versions without the curated feed, and accounts that require the
		// complete list still need a working detection.
		models, errFull := svc.FetchAvailableModels(ctx, token)
		if errFull != nil {
			return nil, nil, errFeed
		}
		return models, nil, nil
	}
	includePaid := clinePaidTiersEnabled(s.cfg, auth)
	return catalog.Flat(includePaid), catalog.TierCounts(), nil
}

// maybeDetectClineModels lazily detects the per-account model catalog for a
// cline credential that has no stored models yet (typical case: a manually
// imported auth file). It runs detached from the auth-update pipeline: the
// fetch is serialized per credential by clineauth.DetectAvailableModelsOnce,
// and a successful detection is persisted to the auth file; the watcher WRITE
// event then re-registers the auth, so /v1/models picks up the detected
// catalog. Detection failure is intentionally not persisted, so the next
// auth lifecycle event may retry.
//
// The metadata "models" key is the durable "detected" marker: once present
// (even with an empty list for a model-less account), no further fetch occurs.
func (s *Service) maybeDetectClineModels(auth *coreauth.Auth) {
	if s == nil || s.coreManager == nil || auth == nil {
		return
	}
	if s.cfg != nil && s.cfg.Home.Enabled {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "cline") {
		return
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return
	}
	if _, detected := auth.Metadata[clineauth.ModelsMetadataKey]; detected {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	go func() {
		ctx := context.Background()
		err := clineauth.DetectAvailableModelsOnce(authID, func() error {
			current, ok := s.coreManager.GetByID(authID)
			if !ok || current == nil {
				return nil
			}
			if _, detected := current.Metadata[clineauth.ModelsMetadataKey]; detected {
				return nil
			}
			token, baseURL := clineCredentialDetails(current)
			svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, current.ProxyURL, baseURL)
			models, tiers, errFetch := s.clineCatalogForAccount(ctx, svc, current, token)
			if errFetch != nil {
				log.Warnf("cline: per-account model detection failed for %s: %v", authID, errFetch)
				return nil // Not persisted: the next auth lifecycle event may retry.
			}
			latest, okLatest := s.coreManager.GetByID(authID)
			if !okLatest || latest == nil {
				return nil
			}
			updated := latest.Clone()
			if updated.Metadata == nil {
				updated.Metadata = make(map[string]any)
			}
			updated.Metadata[clineauth.ModelsMetadataKey] = models
			if tiers != nil {
				updated.Metadata[ModelsTiersMetadataKey] = tiers
			} else {
				delete(updated.Metadata, ModelsTiersMetadataKey)
			}
			if _, errSave := sdkAuth.GetTokenStore().Save(ctx, updated); errSave != nil {
				return errSave
			}
			label := strings.TrimSpace(current.Label)
			if label == "" {
				label = "cline"
			}
			log.Infof("cline: detected %d model(s) for account %s", len(models), label)
			return nil
		})
		if err != nil {
			log.Warnf("cline: persisting detected models for %s failed: %v", authID, err)
		}
	}()
}

// clineCredentialDetails extracts the access token and API base URL from a
// cline auth record (attributes take precedence, then metadata).
func clineCredentialDetails(auth *coreauth.Auth) (token, baseURL string) {
	if auth == nil {
		return "", clineauth.DefaultAPIBaseURL
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if baseURL == "" {
		baseURL = metadataValue(auth, "base_url")
	}
	if token == "" {
		token = metadataValue(auth, "access_token")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = clineauth.DefaultAPIBaseURL
	}
	return token, strings.TrimSpace(baseURL)
}

func metadataValue(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// clineModelFetchTimeout bounds the per-key model discovery request.
const clineModelFetchTimeout = 15 * time.Second

// discoverClineModels fetches the models available to an api-keys.cline
// credential through {base-url}/ai/cline/models (Bearer API key). It returns
// nil when the query is unreachable or empty; callers then fall back to the
// configured models or the static registry catalog.
func (s *Service) discoverClineModels(ctx context.Context, a *coreauth.Auth) []clineauth.ClineModelInfo {
	if s == nil || a == nil {
		return nil
	}
	baseURL := strings.TrimSpace(a.Attributes["base_url"])
	apiKey := strings.TrimSpace(a.Attributes["api_key"])
	if baseURL == "" || apiKey == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, clineModelFetchTimeout)
	defer cancel()

	proxyURL := strings.TrimSpace(a.ProxyURL)
	if proxyURL == "" && s.cfg != nil {
		proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
	}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		log.Warnf("cline: failed to build model discovery transport: %v", err)
		return nil
	}
	client := &http.Client{}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}

	endpoint := strings.TrimSuffix(baseURL, "/") + "/ai/cline/recommended-models"
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		log.Warnf("cline: failed to build model discovery request: %v", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	clineauth.ApplyClientHeaders(req, nil)

	resp, err := client.Do(req)
	if err != nil {
		log.Warnf("cline: model discovery request failed: %v", err)
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cline: model discovery close body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		log.Debugf("cline: recommended-models feed returned status %d, falling back to the full catalog", resp.StatusCode)
		return s.discoverClineModelsFull(fetchCtx, client, baseURL, apiKey)
	}
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil
	}
	catalog, errParse := clineauth.ParseRecommendedModels(raw)
	if errParse != nil {
		return nil
	}
	// api-keys.cline credentials are the subscription keys the paid families
	// exist for; curated feed wins over the full 463-entry import spam.
	models := catalog.Flat(true)
	if len(models) == 0 {
		return nil
	}
	log.Infof("cline: discovered %d curated models for api key via %s", len(models), baseURL)
	return models
}

// discoverClineModelsFull is the /ai/cline/models fallback path used when the
// curated feed is unavailable (old deployments, mirrors without the feed).
func (s *Service) discoverClineModelsFull(ctx context.Context, client *http.Client, baseURL, apiKey string) []clineauth.ClineModelInfo {
	endpoint := strings.TrimSuffix(baseURL, "/") + "/ai/cline/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	clineauth.ApplyClientHeaders(req, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cline: model discovery close body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil
	}
	models, errParse := clineauth.ParseClineModels(raw)
	if errParse != nil || len(models) == 0 {
		return nil
	}
	log.Infof("cline: discovered %d models for api key via full catalog at %s (curated feed unavailable)", len(models), baseURL)
	return models
}
