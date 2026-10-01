package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// nativeQuotaTimeout bounds the outbound credential-quota fetch (this is a
// credential-acquisition-adjacent operation; bounded per project rule).
const nativeQuotaTimeout = 15 * time.Second

// nativeQuotaProviders lists the builtin providers with a native quota fetcher.
var nativeQuotaProviders = []string{"kiro", "mirasim"}

// tryNativeQuotaFetch serves quota fetches for builtin providers that have no
// quota endpoint in the plugin system: kiro via AWS CodeWhisperer usage API,
// mirasim via the relay's /v1/limits. Returns handled=true when the provider
// is builtin-supported (irrespective of success), so callers never fall
// through to plugins/probes for these providers.
func (h *Handler) tryNativeQuotaFetch(c context.Context, auth *cliproxyauth.Auth) (pluginapi.QuotaFetchResponse, bool, error) {
	if h == nil || auth == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	switch provider {
	case "kiro":
		return h.fetchKiroQuota(c, auth)
	case "mirasim":
		return h.fetchMirasimQuota(c, auth)
	default:
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
}

// nativeQuotaSupported reports whether a provider has a builtin quota fetcher.
func nativeQuotaSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kiro", "mirasim":
		return true
	default:
		return false
	}
}

// fetchKiroQuota resolves the auth's kiro tokens and queries GetUsageLimits.
func (h *Handler) fetchKiroQuota(parentCtx context.Context, auth *cliproxyauth.Auth) (pluginapi.QuotaFetchResponse, bool, error) {
	tokenData := kiroTokenDataFromAuth(auth)
	if tokenData.AccessToken == "" {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("kiro: access token not found for credential")
	}
	ctx, cancel := context.WithTimeout(parentCtx, nativeQuotaTimeout)
	defer cancel()
	kAuth := kiroauth.NewKiroAuth(h.cfg)
	usage, err := kAuth.GetUsageLimits(ctx, tokenData)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("kiro usage limits: %w", err)
	}

	resp := pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{Plan: strings.TrimSpace(usage.SubscriptionTitle)},
	}
	if usage.UsageLimit > 0 {
		remaining := (usage.UsageLimit - usage.CurrentUsage) / usage.UsageLimit
		if remaining < 0 {
			remaining = 0
		}
		resp.Groups = []pluginapi.QuotaGroup{{
			DisplayName: "credits",
			Buckets: []pluginapi.QuotaBucket{{
				RemainingFraction: remaining,
				Description:       fmt.Sprintf("%.2f / %.2f credits", usage.UsageLimit-usage.CurrentUsage, usage.UsageLimit),
				ResetTime:         strings.TrimSpace(usage.NextReset),
			}},
		}}
	}
	resp.Summary = []pluginapi.QuotaMetric{}
	if usage.CurrentUsage > 0 || usage.UsageLimit > 0 {
		resp.Summary = append(resp.Summary,
			pluginapi.QuotaMetric{Key: "credits_used", Label: "Credits used", Value: usage.CurrentUsage, Unit: "credits"},
			pluginapi.QuotaMetric{Key: "credits_limit", Label: "Credit limit", Value: usage.UsageLimit, Unit: "credits"},
		)
	}
	return resp, true, nil
}

// kiroTokenDataFromAuth extracts the kiro token bundle the API needs.
func kiroTokenDataFromAuth(auth *cliproxyauth.Auth) *kiroauth.KiroTokenData {
	data := &kiroauth.KiroTokenData{}
	if auth == nil || auth.Metadata == nil {
		return data
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		data.AccessToken = strings.TrimSpace(v)
	}
	if v, ok := auth.Metadata["profile_arn"].(string); ok {
		data.ProfileArn = strings.TrimSpace(v)
	}
	if v, ok := auth.Metadata["refresh_token"].(string); ok {
		data.RefreshToken = strings.TrimSpace(v)
	}
	return data
}

// fetchMirasimQuota calls {base-url}/v1/limits with the mirasim Bearer key and
// maps the response onto the normalized quota payload. The relay response
// shape is vendor-specific, so the mapping tolerates the common variants
// (remaining/total usage/limits at root or under "summary"). Mirasim OAuth
// auth files take the signed relay control call instead of the plain Bearer.
func (h *Handler) fetchMirasimQuota(parentCtx context.Context, auth *cliproxyauth.Auth) (pluginapi.QuotaFetchResponse, bool, error) {
	ctx, cancel := context.WithTimeout(parentCtx, nativeQuotaTimeout)
	defer cancel()

	// OAuth auth file: signed control-plane GET /v1/limits (the relay's
	// protocol for OAuth-issued tokens); api-key entries keep the plain path.
	if isMirasimOAuthAuthFile(auth) {
		storage := mirasimauth.StorageFromMetadata(auth.Metadata)
		storage.NormalizeEndpoints()
		proxyURL := ""
		if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
			proxyURL = strings.TrimSpace(auth.ProxyURL)
		} else if h != nil && h.cfg != nil {
			proxyURL = strings.TrimSpace(h.cfg.ProxyURL)
		}
		client, errClient := mirasimauth.NewRelayClient(&storage, proxyURL)
		if errClient != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim quota probe: %w", errClient)
		}
		body, err := client.FetchLimits(ctx)
		if err != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim quota probe failed: %w", err)
		}
		return mapMirasimLimits(body), true, nil
	}

	var apiKey, baseURL string
	if auth != nil && auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if apiKey == "" && auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	if baseURL == "" && auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	if apiKey == "" {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim: api key not found for credential")
	}
	if baseURL == "" {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim: base-url is required for the quota probe; check the credential's api-keys entry")
	}

	url := strings.TrimSuffix(baseURL, "/") + "/v1/limits"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, bytes.NewReader(nil))
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("build mirasim quota request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim quota probe failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("mirasim quota probe: close response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim quota probe: read body: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("mirasim quota probe returned status %d: %s", resp.StatusCode, string(body))
	}
	return mapMirasimLimits(body), true, nil
}

// isMirasimOAuthAuthFile reports whether the credential is a mirasim OAuth
// auth file (mirasim provider with the oauth kind and a device identity in
// metadata), as opposed to an api-keys.mirasim entry.
func isMirasimOAuthAuthFile(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), mirasimauth.Provider) {
		return false
	}
	kind, _ := auth.Metadata["auth_kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "oauth") {
		return false
	}
	deviceKey, _ := auth.Metadata["device_private_key"].(string)
	return strings.TrimSpace(deviceKey) != ""
}

// mirasimLimitsPayload tolerates relay response variants for /v1/limits.
type mirasimLimitsPayload struct {
	Plan              string                  `json:"plan"`
	PlanType          string                  `json:"plan_type"`
	TierName          string                  `json:"tier_name"`
	TotalCredits      float64                 `json:"total_credits"`
	UsedCredits       float64                 `json:"used_credits"`
	Remaining         float64                 `json:"remaining"`
	RemainingFraction float64                 `json:"remaining_fraction"`
	Limit             float64                 `json:"limit"`
	Used              float64                 `json:"used"`
	ResetAt           string                  `json:"reset_at"`
	ResetTime         string                  `json:"reset_time"`
	NextReset         string                  `json:"next_reset"`
	Summary           []pluginapi.QuotaMetric `json:"summary"`
	Groups            []pluginapi.QuotaGroup  `json:"groups"`
}

// mapMirasimLimits converts the relay payload to the normalized response.
func mapMirasimLimits(body []byte) pluginapi.QuotaFetchResponse {
	resp := pluginapi.QuotaFetchResponse{}
	var raw mirasimLimitsPayload
	if err := json.Unmarshal(body, &raw); err != nil {
		// Relay returned a non-JSON body; surface it as a generic bucket-free response.
		return resp
	}

	plan := strings.TrimSpace(raw.Plan)
	if plan == "" {
		plan = strings.TrimSpace(raw.PlanType)
	}
	if plan == "" {
		plan = strings.TrimSpace(raw.TierName)
	}
	if plan != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{Plan: plan}
	}

	resp.Summary = raw.Summary
	if len(raw.Groups) > 0 {
		resp.Groups = raw.Groups
	}

	limit := raw.Limit
	if limit <= 0 {
		limit = raw.TotalCredits
	}
	used := raw.Used
	if used <= 0 {
		used = raw.UsedCredits
	}
	if limit > 0 {
		remaining := (limit - used) / limit
		if remaining < 0 {
			remaining = 0
		}
		resp.Groups = append(resp.Groups, pluginapi.QuotaGroup{
			DisplayName: "credits",
			Buckets: []pluginapi.QuotaBucket{{
				RemainingFraction: remaining,
				Description:       fmt.Sprintf("%.2f / %.2f credits", limit-used, limit),
				ResetTime:         chooseString(raw.ResetAt, raw.ResetTime, raw.NextReset),
			}},
		})
		resp.Summary = append(resp.Summary,
			pluginapi.QuotaMetric{Key: "credits_used", Label: "Credits used", Value: used, Unit: "credits"},
			pluginapi.QuotaMetric{Key: "credits_limit", Label: "Credit limit", Value: limit, Unit: "credits"},
		)
	} else if raw.RemainingFraction > 0 {
		resp.Groups = append(resp.Groups, pluginapi.QuotaGroup{
			DisplayName: "credits",
			Buckets: []pluginapi.QuotaBucket{{
				RemainingFraction: raw.RemainingFraction,
				ResetTime:         chooseString(raw.ResetAt, raw.ResetTime, raw.NextReset),
			}},
		})
	}
	return resp
}

func chooseString(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
