package modelprobe

import (
	"context"
	"strings"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// mirasimPreflightTimeout bounds the free, not rate-limited limits call the
// probe makes before probing a mirasim model.
const mirasimPreflightTimeout = 10 * time.Second

// preflightOutcome consults provider-specific free signals before an
// inference probe. For mirasim the relay itself directs probes at GET
// /v1/limits ("costs no upstream call and is not rate limited"): a model
// whose model-scoped budget window reads exhausted is already known unusable
// for now, so the cycle records limited without spending a model call.
// Signals are advisory-only — fetch/parse failures fall through to the normal
// executor probe.
func (e *Engine) preflightOutcome(ctx context.Context, auth *cliproxyauth.Auth, provider, model string) *ModelOutcome {
	if normalizeProvider(provider) != "mirasim" || auth == nil {
		return nil
	}
	storage := mirasimauth.StorageFromMetadata(auth.Metadata)
	if strings.TrimSpace(storage.AccessToken) == "" || strings.TrimSpace(storage.DevicePrivateKey) == "" {
		return nil
	}
	storage.NormalizeEndpoints()
	proxyURL := strings.TrimSpace(auth.ProxyURL)
	if proxyURL == "" && e.cfg != nil {
		proxyURL = strings.TrimSpace(e.cfg.ProxyURL)
	}
	client, err := mirasimauth.NewRelayClient(&storage, proxyURL)
	if err != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	preflightCtx, cancel := context.WithTimeout(ctx, mirasimPreflightTimeout)
	defer cancel()
	body, err := client.FetchLimits(preflightCtx)
	if err != nil {
		return nil
	}
	if mirasimauth.ModelLimitExhausted(mirasimauth.ParseModelLimitWindows(body), model) {
		return &ModelOutcome{
			Status:    StatusLimited,
			Error:     "model-scoped window exhausted (reported by /v1/limits)",
			CheckedMS: e.Now().UnixMilli(),
		}
	}
	return nil
}
