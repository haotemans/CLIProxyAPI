package auth

import (
	"context"
	"fmt"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// mirasimRefreshLead is how far ahead of expiry the mirasim OAuth access
// token should be refreshed (the official client's quarter hour).
var mirasimRefreshLead = 15 * time.Minute

// MirasimAuthenticator implements the Mirasim browser OAuth login: a loopback
// callback listener (Mirasim only redirects to loopback), a manual paste
// fallback, and an email-code variant for accounts with no provider bound.
type MirasimAuthenticator struct{}

// NewMirasimAuthenticator constructs a new mirasim authenticator.
func NewMirasimAuthenticator() Authenticator {
	return &MirasimAuthenticator{}
}

// Provider returns the provider key for mirasim.
func (MirasimAuthenticator) Provider() string { return mirasimauth.Provider }

// RefreshLead returns the duration before token expiry when refresh should occur.
func (MirasimAuthenticator) RefreshLead() *time.Duration {
	return &mirasimRefreshLead
}

// Login performs the Mirasim OAuth login (browser + loopback listener) or the
// email-code login when opts.Metadata["email"] is set, then persists the
// credential file via the sdk auth manager.
func (a MirasimAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	settings := mirasimauth.SettingsFromConfig(cfg.ProxyURL)
	settings.CallbackPort = opts.CallbackPort
	if requested := opts.Metadata["provider"]; requested != "" {
		settings.LoginProvider = requested
	}

	fmt.Println("Starting Mirasim authentication...")

	var result *mirasimauth.CLILoginResult
	var err error
	if email := opts.Metadata["email"]; email != "" {
		result, err = mirasimauth.RunEmailLogin(ctx, settings, email, opts.Metadata["code"], opts.Prompt)
	} else {
		// The flow prints the login URL and opens the browser itself (best
		// effort); headless servers paste the callback URL at the prompt.
		result, err = mirasimauth.RunCLILogin(ctx, settings, opts.NoBrowser, opts.Prompt)
	}
	if err != nil {
		return nil, fmt.Errorf("mirasim: %w", err)
	}

	storage := result.Storage
	fileName := storage.DefaultAuthFileName()
	label := storage.AuthLabel()

	fmt.Println("\nMirasim authentication successful!")

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   mirasimauth.Provider,
		FileName:   fileName,
		Label:      label,
		Metadata:   storage.Metadata(),
		Attributes: map[string]string{"auth_kind": "oauth"},
	}, nil
}
