package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroLogin triggers the Kiro authentication flow with AWS Builder ID.
// This is the default login method (same as --kiro-aws-login): the AWS SSO
// OIDC device code flow, which works on headless servers.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including Prompt field
func DoKiroLogin(cfg *config.Config, options *LoginOptions) {
	DoKiroAWSLogin(cfg, options)
}

// DoKiroAWSLogin triggers Kiro authentication with AWS Builder ID.
// This uses the device code flow for AWS SSO OIDC authentication.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
func DoKiroAWSLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	record, savedPath, err := manager.Login(context.Background(), "kiro", cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro AWS authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure you have an AWS Builder ID")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If callback fails, try: --kiro-import (after logging in via Kiro IDE)")
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro AWS authentication successful!")
}

// DoKiroAWSAuthCodeLogin triggers Kiro authentication with AWS Builder ID using authorization code flow.
// This provides a better UX than device code flow as it uses automatic browser callback.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
func DoKiroAWSAuthCodeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.LoginWithAuthCode(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro AWS authentication (auth code) failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure you have an AWS Builder ID")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If callback fails, try: --kiro-aws-login (device code flow)")
		return
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro AWS authentication successful!")
}

// DoKiroImport imports Kiro token from Kiro IDE's token file.
// This is useful for users who have already logged in via Kiro IDE
// and want to use the same credentials in CLI Proxy API.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options (currently unused for import)
func DoKiroImport(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.ImportFromKiroIDE(context.Background(), cfg)
	if err != nil {
		log.Errorf("Kiro token import failed: %v", err)
		fmt.Println("\nMake sure you have logged in to Kiro IDE first:")
		fmt.Println("1. Open Kiro IDE")
		fmt.Println("2. Click 'Sign in with Google' (or GitHub)")
		fmt.Println("3. Complete the login process")
		fmt.Println("4. Run this command again")
		return
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Imported as %s\n", record.Label)
	}
	fmt.Println("Kiro token import successful!")
}

// DoKiroIDCLogin triggers Kiro authentication with AWS IAM Identity Center (IDC).
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
//   - startURL: The IDC start URL (required)
//   - region: The AWS region of the identity center
//   - flow: "device" for the device code flow, otherwise the authorization code flow
func DoKiroIDCLogin(cfg *config.Config, options *LoginOptions, startURL, region, flow string) {
	if options == nil {
		options = &LoginOptions{}
	}

	if startURL == "" {
		log.Errorf("Kiro IDC login requires --kiro-idc-start-url")
		fmt.Println("\nUsage: --kiro-idc-login --kiro-idc-start-url https://d-xxx.awsapps.com/start")
		return
	}

	manager := newAuthManager()

	record, savedPath, err := manager.Login(context.Background(), "kiro", cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata: map[string]string{
			"start-url": startURL,
			"region":    region,
			"flow":      flow,
		},
		Prompt: options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro IDC authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure your IDC Start URL is correct")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If auth code flow fails, try: --kiro-idc-flow device")
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro IDC authentication successful!")
}
