package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Uploaded auth files keep base_url/api_key in Metadata (the auth-file
// synthesizer does not lift them into Attributes the way config key families
// do), so resolveCredentials must fall back to Metadata when Attributes are
// empty, and Attributes must still win when both are set.
func TestResolveCredentialsFallsBackToMetadata(t *testing.T) {
	e := &OpenAICompatExecutor{}
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Metadata: map[string]any{
			"base_url": "https://opencode.ai/zen/v1",
			"api_key":  "public",
		},
	}
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("baseURL = %q, want metadata fallback", baseURL)
	}
	if apiKey != "public" {
		t.Errorf("apiKey = %q, want metadata fallback", apiKey)
	}
}

func TestResolveCredentialsAttributesWinOverMetadata(t *testing.T) {
	e := &OpenAICompatExecutor{}
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": "https://attr.example/v1",
			"api_key":  "attr-key",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.example/v1",
			"api_key":  "meta-key",
		},
	}
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL != "https://attr.example/v1" {
		t.Errorf("baseURL = %q, want attributes to win", baseURL)
	}
	if apiKey != "attr-key" {
		t.Errorf("apiKey = %q, want attributes to win", apiKey)
	}
}

func TestResolveCredentialsPartialMetadataFallback(t *testing.T) {
	e := &OpenAICompatExecutor{}
	// Attributes has only the key; base URL must come from Metadata.
	auth := &cliproxyauth.Auth{
		Provider:   "opencode-go",
		Attributes: map[string]string{"api_key": "public"},
		Metadata:   map[string]any{"base_url": "https://opencode.ai/zen/v1"},
	}
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("baseURL = %q, want metadata fallback for missing attribute", baseURL)
	}
	if apiKey != "public" {
		t.Errorf("apiKey = %q, want attributes value", apiKey)
	}
}
