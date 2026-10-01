package executor

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// MirasimExecutor serves mirasim credentials of both kinds (the claude
// coexistence pattern): api-keys.mirasim entries use the plain Bearer path
// into an Anthropic-Messages reverse proxy, while mirasim OAuth auth files
// take the signed relay protocol (mrs-sig-v2 + device tickets) the upstream
// requires for OAuth-issued tokens. Both reuse the Claude executor pipeline.
type MirasimExecutor struct {
	*ClaudeExecutor
	oauth *MirasimOAuthExecutor
}

// NewMirasimExecutor creates a new Mirasim executor.
func NewMirasimExecutor(cfg *config.Config) *MirasimExecutor {
	return &MirasimExecutor{
		ClaudeExecutor: &ClaudeExecutor{
			cfg:                cfg,
			requestLogProvider: "mirasim",
		},
		oauth: NewMirasimOAuthExecutor(cfg),
	}
}

// Identifier returns the executor identifier.
func (e *MirasimExecutor) Identifier() string { return "mirasim" }

// useOAuthProtocol reports whether this credential must take the signed relay
// protocol (mirasim OAuth auth file) rather than the plain api-key Bearer.
func useOAuthProtocol(auth *cliproxyauth.Auth) bool {
	return isMirasimOAuthCredential(auth)
}

// mirasimBaseURL returns the mandatory upstream base URL for api-key Mirasim
// credentials; OAuth credentials are served by the signed path instead.
func mirasimBaseURL(auth *cliproxyauth.Auth) (string, error) {
	_, baseURL := claudeCreds(auth)
	if strings.TrimSpace(baseURL) == "" {
		return "", statusErr{code: 400, msg: "mirasim executor: base_url is required for mirasim api-key credentials"}
	}
	return baseURL, nil
}

// Execute performs a non-streaming request to the Mirasim upstream.
func (e *MirasimExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if useOAuthProtocol(auth) {
		return e.oauth.Execute(ctx, auth, req, opts)
	}
	if _, err := mirasimBaseURL(auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming request to the Mirasim upstream.
func (e *MirasimExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if useOAuthProtocol(auth) {
		return e.oauth.ExecuteStream(ctx, auth, req, opts)
	}
	if _, err := mirasimBaseURL(auth); err != nil {
		return nil, err
	}
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens estimates token count for Mirasim requests. Third-party
// upstreams keep local estimation, matching Claude's custom-base-URL path.
func (e *MirasimExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if useOAuthProtocol(auth) {
		return e.oauth.CountTokens(ctx, auth, req, opts)
	}
	if _, err := mirasimBaseURL(auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.ClaudeExecutor.CountTokens(ctx, auth, req, opts)
}

// Refresh rotates mirasim OAuth tokens; api-key credentials never rotate.
func (e *MirasimExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("mirasim executor: refresh called")
	if useOAuthProtocol(auth) {
		return e.oauth.Refresh(ctx, auth)
	}
	return e.ClaudeExecutor.Refresh(ctx, auth)
}
