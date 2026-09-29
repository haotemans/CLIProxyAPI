package cliproxy

import (
	"context"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kiroModelFetchTimeout bounds the dynamic model lookup at auth registration.
const kiroModelFetchTimeout = 15 * time.Second

// fetchKiroModels attempts to dynamically fetch Kiro models from the API.
// If dynamic fetch fails, it falls back to the static registry catalog.
// The catalog combines static kiro models with amazonq entries.
func (s *Service) fetchKiroModels(ctx context.Context, a *coreauth.Auth) []*registry.ModelInfo {
	fallback := func() []*registry.ModelInfo {
		return append(registry.GetKiroModels(), registry.GetAmazonQModels()...)
	}

	if a == nil {
		log.Debug("kiro: auth is nil, using static models")
		return fallback()
	}

	tokenData := extractKiroTokenData(a)
	if tokenData == nil || tokenData.AccessToken == "" {
		log.Debug("kiro: no valid token data in auth, using static models")
		return fallback()
	}

	kAuth := kiroauth.NewKiroAuth(s.cfg)
	if kAuth == nil {
		log.Warn("kiro: failed to create KiroAuth instance, using static models")
		return fallback()
	}

	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, kiroModelFetchTimeout)
	defer cancel()

	apiModels, err := kAuth.ListAvailableModels(fetchCtx, tokenData)
	if err != nil {
		log.Warnf("kiro: failed to fetch dynamic models: %v, using static models", err)
		return fallback()
	}
	if len(apiModels) == 0 {
		log.Debug("kiro: API returned no models, using static models")
		return fallback()
	}

	models := registry.ConvertKiroAPIModels(kiroAPIModelsToRegistry(apiModels))

	// Generate agentic variants
	models = registry.GenerateAgenticVariants(models)

	// Keep the Amazon Q aliases available alongside the Kiro catalog.
	models = append(models, registry.GetAmazonQModels()...)

	log.Infof("kiro: successfully fetched %d models from API (including agentic variants)", len(models))
	return models
}

// extractKiroTokenData extracts KiroTokenData from auth attributes and metadata.
// It supports both config-based tokens (stored in Attributes) and file-based tokens (stored in Metadata).
func extractKiroTokenData(a *coreauth.Auth) *kiroauth.KiroTokenData {
	if a == nil {
		return nil
	}

	var accessToken, profileArn, refreshToken string

	// Priority 1: Try to get from Attributes (config.yaml source)
	if a.Attributes != nil {
		accessToken = strings.TrimSpace(a.Attributes["access_token"])
		profileArn = strings.TrimSpace(a.Attributes["profile_arn"])
		refreshToken = strings.TrimSpace(a.Attributes["refresh_token"])
	}

	// Priority 2: If not found in Attributes, try Metadata (JSON file source)
	if accessToken == "" && a.Metadata != nil {
		if at, ok := a.Metadata["access_token"].(string); ok {
			accessToken = strings.TrimSpace(at)
		}
		if pa, ok := a.Metadata["profile_arn"].(string); ok {
			profileArn = strings.TrimSpace(pa)
		}
		if rt, ok := a.Metadata["refresh_token"].(string); ok {
			refreshToken = strings.TrimSpace(rt)
		}
	}

	// access_token is required
	if accessToken == "" {
		return nil
	}

	return &kiroauth.KiroTokenData{
		AccessToken:  accessToken,
		ProfileArn:   profileArn,
		RefreshToken: refreshToken,
	}
}

// kiroAPIModelsToRegistry converts auth-package model entries to the registry
// conversion struct (identical shape, separate types to avoid import cycles).
func kiroAPIModelsToRegistry(apiModels []*kiroauth.KiroModel) []*registry.KiroAPIModel {
	out := make([]*registry.KiroAPIModel, 0, len(apiModels))
	for _, m := range apiModels {
		if m == nil {
			continue
		}
		out = append(out, &registry.KiroAPIModel{
			ModelID:        m.ModelID,
			ModelName:      m.ModelName,
			Description:    m.Description,
			RateMultiplier: m.RateMultiplier,
			RateUnit:       m.RateUnit,
			MaxInputTokens: m.MaxInputTokens,
		})
	}
	return out
}
