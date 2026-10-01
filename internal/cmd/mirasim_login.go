package cmd

import (
	"context"
	"fmt"
	"strings"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoMirasimLogin performs the Mirasim OAuth login and saves the tokens.
// The browser flow opens the login URL and waits for the loopback callback
// (Mirasim only redirects to loopback); headless servers paste the refused
// callback URL at the prompt. With options.Email set, the email-code login
// runs instead (accounts with no OAuth provider bound).
//
// Parameters:
//   - cfg: The application configuration containing proxy and auth directory settings
//   - options: Login options including browser behavior, callback port, and
//     the optional provider choice, email address and sign-in code
func DoMirasimLogin(cfg *config.Config, options *LoginOptions, provider, email, code string) {
	if options == nil {
		options = &LoginOptions{}
	}

	metadata := map[string]string{}
	if provider = strings.TrimSpace(provider); provider != "" {
		metadata["provider"] = provider
	}
	if email = strings.TrimSpace(email); email != "" {
		metadata["email"] = email
	}
	if code = strings.TrimSpace(code); code != "" {
		metadata["code"] = code
	}

	prompt := options.Prompt
	if prompt == nil {
		prompt = defaultProjectPrompt()
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     metadata,
		Prompt:       prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), mirasimauth.Provider, cfg, authOpts)
	if err != nil {
		log.Errorf("Mirasim authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Mirasim authentication successful!")
}
