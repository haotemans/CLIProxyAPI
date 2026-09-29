package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// kiroRefreshLead is the duration before token expiry when refresh should occur.
var kiroRefreshLead = 20 * time.Minute

// KiroAuthenticator implements OAuth authentication for Kiro.
// Supported login paths: AWS Builder ID device code flow (works headless),
// AWS Builder ID authorization code flow (local callback), IAM Identity
// Center (IDC), and importing the token file written by Kiro IDE.
// Social login (Google/GitHub) is unavailable for third-party clients due to
// AWS Cognito restrictions.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a Kiro authenticator.
func NewKiroAuthenticator() *KiroAuthenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key for the authenticator.
func (a *KiroAuthenticator) Provider() string {
	return "kiro"
}

// RefreshLead indicates how soon before expiry a refresh should be attempted.
func (a *KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

// createAuthRecord creates an auth record from token data.
func (a *KiroAuthenticator) createAuthRecord(tokenData *kiroauth.KiroTokenData, source string) (*coreauth.Auth, error) {
	// Parse expires_at
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	// Determine label and identifier based on auth method.
	// Generate sequence number for uniqueness.
	seq := time.Now().UnixNano() % 100000

	var label, idPart string
	if tokenData.AuthMethod == "idc" {
		label = "kiro-idc"
		// Priority: email > startUrl identifier > sequence only.
		// Email is unique, so no sequence needed when email is available.
		switch {
		case tokenData.Email != "":
			idPart = kiroauth.SanitizeEmailForFilename(tokenData.Email)
		case tokenData.StartURL != "":
			if identifier := kiroauth.ExtractIDCIdentifier(tokenData.StartURL); identifier != "" {
				idPart = fmt.Sprintf("%s-%05d", identifier, seq)
			} else {
				idPart = fmt.Sprintf("%05d", seq)
			}
		default:
			idPart = fmt.Sprintf("%05d", seq)
		}
	} else {
		label = fmt.Sprintf("kiro-%s", source)
		idPart = extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)
	}

	now := time.Now()
	fileName := fmt.Sprintf("%s-%s.json", label, idPart)

	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  tokenData.AccessToken,
		"refresh_token": tokenData.RefreshToken,
		"profile_arn":   tokenData.ProfileArn,
		"expires_at":    tokenData.ExpiresAt,
		"auth_method":   tokenData.AuthMethod,
		"provider":      tokenData.Provider,
		"client_id":     tokenData.ClientID,
		"client_secret": tokenData.ClientSecret,
		"email":         tokenData.Email,
	}

	// Add IDC-specific fields if present
	if tokenData.StartURL != "" {
		metadata["start_url"] = tokenData.StartURL
	}
	if tokenData.Region != "" {
		metadata["region"] = tokenData.Region
	}

	attributes := map[string]string{
		"profile_arn": tokenData.ProfileArn,
		"source":      source,
		"email":       tokenData.Email,
	}

	// Add IDC-specific attributes if present
	if tokenData.AuthMethod == "idc" {
		attributes["source"] = "aws-idc"
		if tokenData.StartURL != "" {
			attributes["start_url"] = tokenData.StartURL
		}
		if tokenData.Region != "" {
			attributes["region"] = tokenData.Region
		}
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "kiro",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Metadata:   metadata,
		Attributes: attributes,
		// NextRefreshAfter: refresh 20 minutes before expiry
		NextRefreshAfter: expiresAt.Add(-kiroRefreshLead),
	}, nil
}

// printKiroLoginSuccess emits a user-facing success message.
func printKiroLoginSuccess(email string) {
	if email != "" {
		fmt.Printf("\nKiro authentication completed successfully! (Account: %s)\n", email)
	} else {
		fmt.Println("\nKiro authentication completed successfully!")
	}
}

// Login performs OAuth login for Kiro with AWS (Builder ID or IDC).
// With no IDC options in metadata it uses the AWS Builder ID device code flow.
func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}

	// Extract IDC options from metadata if present
	var idcOpts *kiroauth.IDCLoginOptions
	if opts != nil && opts.Metadata != nil {
		if startURL := opts.Metadata["start-url"]; startURL != "" {
			idcOpts = &kiroauth.IDCLoginOptions{
				StartURL:      startURL,
				Region:        opts.Metadata["region"],
				UseDeviceCode: opts.Metadata["flow"] == "device",
			}
		}
	}

	ssoClient := kiroauth.NewSSOOIDCClient(cfg)
	tokenData, err := ssoClient.LoginWithMethodSelection(ctx, idcOpts)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	record, errRecord := a.createAuthRecord(tokenData, "aws")
	if errRecord == nil {
		printKiroLoginSuccess(tokenData.Email)
	}
	return record, errRecord
}

// LoginWithAuthCode performs OAuth login for Kiro with AWS Builder ID using
// the authorization code flow (local browser callback on port 9876).
func (a *KiroAuthenticator) LoginWithAuthCode(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}

	oauth := kiroauth.NewKiroOAuth(cfg)
	tokenData, err := oauth.LoginWithBuilderIDAuthCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	record, errRecord := a.createAuthRecord(tokenData, "aws")
	if errRecord == nil {
		printKiroLoginSuccess(tokenData.Email)
	}
	return record, errRecord
}

// LoginWithGoogle is unavailable for third-party applications due to AWS Cognito restrictions.
func (a *KiroAuthenticator) LoginWithGoogle(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	return nil, fmt.Errorf("Google login is not available for third-party applications due to AWS Cognito restrictions.\n\nAlternatives:\n  1. Use AWS Builder ID: cliproxy kiro --builder-id\n  2. Import token from Kiro IDE: cliproxy kiro --import\n\nTo get a token from Kiro IDE:\n  1. Open Kiro IDE and login with Google\n  2. Find: ~/.kiro/kiro-auth-token.json\n  3. Run: cliproxy kiro --import")
}

// LoginWithGitHub is unavailable for third-party applications due to AWS Cognito restrictions.
func (a *KiroAuthenticator) LoginWithGitHub(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	return nil, fmt.Errorf("GitHub login is not available for third-party applications due to AWS Cognito restrictions.\n\nAlternatives:\n  1. Use AWS Builder ID: cliproxy kiro --builder-id\n  2. Import token from Kiro IDE: cliproxy kiro --import\n\nTo get a token from Kiro IDE:\n  1. Open Kiro IDE and login with GitHub\n  2. Find: ~/.kiro/kiro-auth-token.json\n  3. Run: cliproxy kiro --import")
}

// ImportFromKiroIDE imports token from Kiro IDE's token file.
func (a *KiroAuthenticator) ImportFromKiroIDE(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	tokenData, err := kiroauth.LoadKiroIDEToken()
	if err != nil {
		return nil, fmt.Errorf("failed to load Kiro IDE token: %w", err)
	}

	// Extract email from JWT if not already set (for imported tokens)
	if tokenData.Email == "" {
		tokenData.Email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}

	// Sanitize provider to prevent path traversal (defense-in-depth)
	provider := kiroauth.SanitizeEmailForFilename(strings.ToLower(strings.TrimSpace(tokenData.Provider)))
	if provider == "" {
		provider = "imported" // Fallback for legacy tokens without provider
	}

	record, errRecord := a.createAuthRecord(tokenData, provider)
	if errRecord == nil {
		record.Attributes["region"] = tokenData.Region
		fmt.Printf("\nImported Kiro token from IDE (Provider: %s)\n", tokenData.Provider)
	}
	return record, errRecord
}

// extractKiroIdentifier extracts a meaningful identifier for file naming.
// Returns account name if provided, otherwise profile ARN ID, then client ID.
// All extracted values are sanitized to prevent path injection attacks.
func extractKiroIdentifier(accountName, profileArn, clientID string) string {
	// Priority 1: Use account name if provided
	if accountName != "" {
		return kiroauth.SanitizeEmailForFilename(accountName)
	}

	// Priority 2: Use profile ARN ID part (sanitized to prevent path injection)
	if profileArn != "" {
		parts := strings.Split(profileArn, "/")
		if len(parts) >= 2 {
			return kiroauth.SanitizeEmailForFilename(parts[len(parts)-1])
		}
	}

	// Priority 3: Use client ID (for IDC auth without email/profileArn)
	if clientID != "" {
		return kiroauth.SanitizeEmailForFilename(clientID)
	}

	// Fallback: timestamp
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}
