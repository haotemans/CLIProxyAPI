package config

import "testing"

func TestSanitizeCommandcodeKeys_DefaultBaseURLAndEmptyKey(t *testing.T) {
	cfg := &Config{
		CommandcodeKey: []CommandcodeKey{
			{APIKey: ""},   // empty key, should be dropped
			{APIKey: "  "}, // whitespace key, should be dropped
			{APIKey: "key-1"},
			{APIKey: "key-2", BaseURL: "https://custom-commandcode.example.com"},
		},
	}
	cfg.SanitizeCommandcodeKeys()

	if len(cfg.CommandcodeKey) != 2 {
		t.Fatalf("expected 2 CommandcodeKey entries, got %d", len(cfg.CommandcodeKey))
	}
	if cfg.CommandcodeKey[0].BaseURL != commandcodeDefaultBaseURL {
		t.Fatalf("expected default BaseURL %s, got %s", commandcodeDefaultBaseURL, cfg.CommandcodeKey[0].BaseURL)
	}
	if cfg.CommandcodeKey[1].BaseURL != "https://custom-commandcode.example.com" {
		t.Fatalf("expected custom BaseURL to be kept, got %s", cfg.CommandcodeKey[1].BaseURL)
	}
}

func TestSanitizeOpencodeGoKeys_DefaultBaseURLAndEmptyKey(t *testing.T) {
	cfg := &Config{
		OpencodeGoKey: []OpencodeGoKey{
			{APIKey: ""},   // empty key, should be dropped
			{APIKey: "  "}, // whitespace key, should be dropped
			{APIKey: "key-1"},
			{APIKey: "key-2", BaseURL: "https://custom-opencode-go.example.com"},
		},
	}
	cfg.SanitizeOpencodeGoKeys()

	if len(cfg.OpencodeGoKey) != 2 {
		t.Fatalf("expected 2 OpencodeGoKey entries, got %d", len(cfg.OpencodeGoKey))
	}
	if cfg.OpencodeGoKey[0].BaseURL != opencodeGoDefaultBaseURL {
		t.Fatalf("expected default BaseURL %s, got %s", opencodeGoDefaultBaseURL, cfg.OpencodeGoKey[0].BaseURL)
	}
	if cfg.OpencodeGoKey[1].BaseURL != "https://custom-opencode-go.example.com" {
		t.Fatalf("expected custom BaseURL to be kept, got %s", cfg.OpencodeGoKey[1].BaseURL)
	}
}

func TestSanitizeOpencodeGoKeys_PublicKeyDefaultsToZenFreeBase(t *testing.T) {
	cfg := &Config{
		OpencodeGoKey: []OpencodeGoKey{
			{APIKey: "public"},
			{APIKey: "PUBLIC"},  // case-sensitive: stays a paid key shape
			{APIKey: " public"}, // whitespace: not the anonymous tier either
			{APIKey: "public", BaseURL: "https://mirror.example.com/zen/v1"},
		},
	}
	cfg.SanitizeOpencodeGoKeys()

	if len(cfg.OpencodeGoKey) != 4 {
		t.Fatalf("expected 4 OpencodeGoKey entries, got %d", len(cfg.OpencodeGoKey))
	}
	if got := cfg.OpencodeGoKey[0].BaseURL; got != "https://opencode.ai/zen/v1" {
		t.Fatalf("public key BaseURL = %s, want the anonymous zen root", got)
	}
	if got := cfg.OpencodeGoKey[1].BaseURL; got != opencodeGoDefaultBaseURL {
		t.Fatalf("PUBLIC BaseURL = %s, want the paid default", got)
	}
	if got := cfg.OpencodeGoKey[2].BaseURL; got != opencodeGoDefaultBaseURL {
		t.Fatalf("whitespace-padded BaseURL = %s, want the paid default", got)
	}
	if got := cfg.OpencodeGoKey[3].BaseURL; got != "https://mirror.example.com/zen/v1" {
		t.Fatalf("explicit BaseURL = %s, want the configured mirror", got)
	}
}
