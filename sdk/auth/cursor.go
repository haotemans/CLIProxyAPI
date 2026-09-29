package auth

import (
	"context"
	"fmt"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// cursorRefreshLead is the duration before token expiry when refresh should occur.
var cursorRefreshLead = 10 * time.Minute

// CursorAuthenticator implements the Cursor OAuth PKCE login.
// There is no local callback server: the user opens cursor.com/loginDeepControl
// in any browser and the CLI polls api2.cursor.sh/auth/poll for the tokens,
// so the flow works over SSH and on headless servers.
type CursorAuthenticator struct{}

// NewCursorAuthenticator constructs a new Cursor authenticator.
func NewCursorAuthenticator() Authenticator {
	return &CursorAuthenticator{}
}

// Provider returns the provider key for cursor.
func (CursorAuthenticator) Provider() string { return "cursor" }

// RefreshLead returns the duration before token expiry when refresh should occur.
func (CursorAuthenticator) RefreshLead() *time.Duration {
	return &cursorRefreshLead
}

// Login initiates the Cursor PKCE authentication flow.
func (a CursorAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cursor auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := cursorauth.NewCursorAuth(cfg)

	authParams, err := cursorauth.GenerateAuthParams()
	if err != nil {
		return nil, fmt.Errorf("cursor: failed to generate auth params: %w", err)
	}

	fmt.Println("Starting Cursor authentication...")
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", authParams.LoginURL)

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authParams.LoginURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for Cursor authorization...")

	tokens, err := authSvc.PollForAuth(ctx, authParams.UUID, authParams.Verifier)
	if err != nil {
		return nil, fmt.Errorf("cursor: authentication failed: %w", err)
	}

	// Auto-identify the account from the JWT sub claim.
	sub := cursorauth.ParseJWTSub(tokens.AccessToken)
	subHash := cursorauth.SubToShortHash(sub)

	fileName := cursorauth.CredentialFileName("", subHash)

	metadata := map[string]any{
		"type":          "cursor",
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_at":    cursorauth.GetTokenExpiry(tokens.AccessToken).Format(time.RFC3339),
		"timestamp":     time.Now().UnixMilli(),
	}
	if sub != "" {
		metadata["sub"] = sub
	}

	fmt.Println("\nCursor authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    cursorauth.DisplayLabel("", subHash),
		Metadata: metadata,
	}, nil
}
