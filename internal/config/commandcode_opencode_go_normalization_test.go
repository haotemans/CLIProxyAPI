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
