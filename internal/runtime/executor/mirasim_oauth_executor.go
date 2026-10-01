// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): the signed relay
// transport (mrs-sig-v2 + mrs-seal-v1 + device tickets) for mirasim OAuth
// credentials, layered on the Claude executor's Anthropic Messages pipeline.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// MirasimOAuthExecutor serves mirasim OAuth auth files over the same
// Anthropic Messages pipeline as the api-key variant, except the outbound
// round trip is intercepted to mint/attach the device ticket, sign the
// request, and seal the x-mirasim-* metadata headers — the protocol the
// relay requires for OAuth-issued access tokens.
type MirasimOAuthExecutor struct {
	inner *ClaudeExecutor
	cfg   *config.Config
	mu    sync.Mutex
	// relayState caches one RelayClient per (access token, device key) pair.
	relayState map[string]*mirasimOAuthRelayState
}

// mirasimOAuthRelayState is the per-credential relay client cache entry.
type mirasimOAuthRelayState struct {
	storage *mirasimauth.Storage
	client  *mirasimauth.RelayClient
}

// NewMirasimOAuthExecutor creates the OAuth half of the mirasim executor.
// The embedded Claude executor runs with a proxy-muted config clone so the
// embedded client's utls builder falls through to the context round tripper
// the sign wrapper installs (the wrapper itself carries the proxy-aware base).
func NewMirasimOAuthExecutor(cfg *config.Config) *MirasimOAuthExecutor {
	var muted *config.Config
	if cfg != nil {
		muted = cfg.CloneForRuntime()
	}
	if muted == nil {
		muted = &config.Config{}
	}
	muted.ProxyURL = ""
	return &MirasimOAuthExecutor{
		inner: &ClaudeExecutor{
			cfg:                muted,
			requestLogProvider: "mirasim",
		},
		cfg:        cfg,
		relayState: make(map[string]*mirasimOAuthRelayState),
	}
}

// Identifier returns the executor identifier.
func (e *MirasimOAuthExecutor) Identifier() string { return "mirasim" }

// isMirasimOAuthCredential reports whether the auth is a mirasim OAuth auth
// file (as opposed to an api-keys.mirasim entry, which stays on the plain
// Bearer path).
func isMirasimOAuthCredential(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "mirasim") {
		return false
	}
	kind, _ := auth.Metadata["auth_kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "oauth") {
		return false
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	deviceKey, _ := auth.Metadata["device_private_key"].(string)
	return strings.TrimSpace(accessToken) != "" && strings.TrimSpace(deviceKey) != ""
}

// relayStateFor resolves (and caches) the RelayClient for one credential.
func (e *MirasimOAuthExecutor) relayStateFor(auth *cliproxyauth.Auth) (*mirasimOAuthRelayState, error) {
	storage := mirasimauth.StorageFromMetadata(auth.Metadata)
	storage.NormalizeEndpoints()
	if err := validateMirasimOAuthStorage(storage); err != nil {
		return nil, err
	}
	key := storage.AccessToken + "\x00" + storage.DevicePrivateKey
	e.mu.Lock()
	defer e.mu.Unlock()
	if state := e.relayState[key]; state != nil {
		return state, nil
	}
	proxyURL := e.oauthProxyURL(auth)
	held := storage
	client, err := mirasimauth.NewRelayClient(&held, proxyURL)
	if err != nil {
		return nil, err
	}
	state := &mirasimOAuthRelayState{storage: &held, client: client}
	e.relayState[key] = state
	return state, nil
}

func validateMirasimOAuthStorage(storage mirasimauth.Storage) error {
	if strings.TrimSpace(storage.AccessToken) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "mirasim oauth: access token not found"}
	}
	if strings.TrimSpace(storage.DevicePrivateKey) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "mirasim oauth: device identity not found"}
	}
	return nil
}

// oauthProxyURL resolves the transport for sign/mint/refresh calls: the
// credential's own proxy wins, then the global requests.proxy-url.
func (e *MirasimOAuthExecutor) oauthProxyURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if e != nil && e.cfg != nil {
		return strings.TrimSpace(e.cfg.ProxyURL)
	}
	return ""
}

// signedClaudeAuth clones the credential with the pipeline-facing shape the
// Claude layer expects: relay base URL, empty api key (the sign wrapper owns
// Authorization), and no per-credential proxy (the sign wrapper's base
// transport handles the network path; the embedded utls builder falls
// through to the wrapper's context round tripper).
func signedClaudeAuth(auth *cliproxyauth.Auth, relayURL string) *cliproxyauth.Auth {
	clone := auth.Clone()
	if clone.Attributes == nil {
		clone.Attributes = make(map[string]string)
	}
	clone.Attributes["base_url"] = relayURL
	clone.Attributes["api_key"] = ""
	clone.ProxyURL = ""
	return clone
}

// signedContext installs the sign wrapper as the context round tripper. A
// test-injected round tripper is wrapped rather than replaced.
func (e *MirasimOAuthExecutor) signedContext(ctx context.Context, state *mirasimOAuthRelayState, auth *cliproxyauth.Auth) context.Context {
	var base http.RoundTripper
	if existing, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && existing != nil {
		base = existing
	} else {
		base = mirasimProxyAwareTransport(e.oauthProxyURL(auth))
	}
	proxyClient := &http.Client{Transport: mirasimProxyAwareTransport(e.oauthProxyURL(auth))}
	return context.WithValue(ctx, "cliproxy.roundtripper", &mirasimSignTransport{base: base, state: state, proxyClient: proxyClient})
}

// mirasimProxyAwareTransport builds the wrapper's underlying transport.
func mirasimProxyAwareTransport(proxyURL string) http.RoundTripper {
	if strings.TrimSpace(proxyURL) == "" {
		return http.DefaultTransport
	}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil || transport == nil {
		return http.DefaultTransport
	}
	return transport
}

// Execute performs a non-streaming request with the signed relay protocol.
func (e *MirasimOAuthExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	state, err := e.relayStateFor(auth)
	if err != nil {
		return resp, err
	}
	return e.inner.Execute(e.signedContext(ctx, state, auth), signedClaudeAuth(auth, state.storage.RelayURL), req, opts)
}

// ExecuteStream performs a streaming request with the signed relay protocol.
func (e *MirasimOAuthExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	state, err := e.relayStateFor(auth)
	if err != nil {
		return nil, err
	}
	return e.inner.ExecuteStream(e.signedContext(ctx, state, auth), signedClaudeAuth(auth, state.storage.RelayURL), req, opts)
}

// CountTokens estimates the token count through the same signed pipeline.
func (e *MirasimOAuthExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	state, err := e.relayStateFor(auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.inner.CountTokens(e.signedContext(ctx, state, auth), signedClaudeAuth(auth, state.storage.RelayURL), req, opts)
}

// Refresh rotates the mirasim OAuth tokens (and runs the periodic profile
// check) exactly like the plugin's RefreshForHost: refresh due on schedule,
// on 401 recovery, or when the account profile reports a changed plan.
func (e *MirasimOAuthExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("mirasim oauth executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil || auth.Metadata == nil {
		return auth, nil
	}
	if !isMirasimOAuthCredential(auth) {
		return auth, nil
	}
	storage := mirasimauth.StorageFromMetadata(auth.Metadata)
	storage.NormalizeEndpoints()
	if err := mirasimauth.RefreshForSchedule(ctx, &storage, e.oauthProxyURL(auth), time.Now().UTC()); err != nil {
		return nil, err
	}
	newAuth := auth.Clone()
	if newAuth.Metadata == nil {
		newAuth.Metadata = make(map[string]any)
	}
	for key, value := range storage.Metadata() {
		newAuth.Metadata[key] = value
	}
	newAuth.Metadata["type"] = mirasimauth.Provider
	// Tokens rotated: evict stale relay clients for this device identity.
	e.mu.Lock()
	e.dropRelayStateLocked(storage)
	e.mu.Unlock()
	return newAuth, nil
}

// dropRelayStateLocked evicts cached relay clients whose stored tokens no
// longer match the current ones for the same device identity.
func (e *MirasimOAuthExecutor) dropRelayStateLocked(storage mirasimauth.Storage) {
	currentKey := storage.AccessToken + "\x00" + storage.DevicePrivateKey
	for key, state := range e.relayState {
		if key != currentKey && state != nil && state.storage != nil && state.storage.DevicePrivateKey == storage.DevicePrivateKey {
			delete(e.relayState, key)
		}
	}
}

// mirasimSignTransport intercepts the outbound request to mint the device
// ticket (or fall back to the access token when the relay has no
// device-session route), sign the request mrs-sig-v2, and seal the
// x-mirasim-* metadata mrs-seal-v1 — per attempt, mirroring the official
// client's per-call metadata.
type mirasimSignTransport struct {
	base        http.RoundTripper
	state       *mirasimOAuthRelayState
	proxyClient *http.Client
}

func (t *mirasimSignTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || t == nil || t.state == nil || t.state.client == nil {
		return t.base.RoundTrip(req)
	}
	// Only the mirasim relay is signed; anything else (shouldn't happen on
	// this pipeline) passes through.
	credential, err := t.credential(req)
	if err != nil {
		return nil, err
	}
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("mirasim oauth: read request body for signing: %w", err)
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	signPath := req.URL.Path
	metadata := map[string]string{
		"x-mirasim-session": t.state.client.SessionID(),
		"x-mirasim-agent":   mirasimauth.AgentForRequest(signPath, body),
		"x-mirasim-call":    mirasimauth.NewCallID(),
	}
	if account := t.state.client.AccountID(); account != "" {
		metadata["x-mirasim-account"] = account
	}
	headers, err := t.state.client.SignHeaders(req.Method, signPath, credential, metadata, body)
	if err != nil {
		return nil, fmt.Errorf("mirasim oauth: sign request: %w", err)
	}
	// The wrapper owns the request's authorization and mirasim wire headers.
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	for key, values := range headers {
		req.Header[key] = append([]string(nil), values...)
	}
	return t.base.RoundTrip(req)
}

// credential resolves the Bearer value this attempt must carry, minting the
// device ticket through the wrapper's own proxy-aware client.
func (t *mirasimSignTransport) credential(req *http.Request) (string, error) {
	mintCtx := mirasimauth.WithRelayHTTPClient(req.Context(), t.proxyClient)
	return t.state.client.Credential(mintCtx)
}
