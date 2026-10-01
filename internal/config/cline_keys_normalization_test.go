package config

import "testing"

func TestSanitizeClineKeys_DefaultBaseURLAndEmptyKey(t *testing.T) {
	cfg := &Config{
		ClineKey: []ClineKey{
			{APIKey: ""},   // empty key, should be dropped (chat requires it)
			{APIKey: "  "}, // whitespace key, should be dropped
			{APIKey: "key-1"},
			{APIKey: "key-2", BaseURL: "https://mirror.example.com/api/v1"},
		},
	}
	cfg.SanitizeClineKeys()

	if len(cfg.ClineKey) != 2 {
		t.Fatalf("expected 2 ClineKey entries, got %d", len(cfg.ClineKey))
	}
	if cfg.ClineKey[0].BaseURL != clineDefaultBaseURL {
		t.Fatalf("expected default BaseURL %s, got %s", clineDefaultBaseURL, cfg.ClineKey[0].BaseURL)
	}
	if cfg.ClineKey[0].BaseURL != "https://api.cline.bot/api/v1" {
		t.Fatalf("default must carry the /api/v1 root (chat and models join under it): %s", cfg.ClineKey[0].BaseURL)
	}
	if cfg.ClineKey[1].BaseURL != "https://mirror.example.com/api/v1" {
		t.Fatalf("expected custom BaseURL to be kept, got %s", cfg.ClineKey[1].BaseURL)
	}
}

func TestParseClineAPIKeyConfig(t *testing.T) {
	data := []byte(`
api-keys:
  cline:
    - name: cline-1
      keys:
        - api-key: "cline-sk-test"
          weight: 3
`)
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if len(cfg.ClineKey) != 1 {
		t.Fatalf("expected 1 ClineKey, got %d", len(cfg.ClineKey))
	}
	entry := cfg.ClineKey[0]
	if entry.APIKey != "cline-sk-test" {
		t.Fatalf("api key = %q", entry.APIKey)
	}
	if entry.BaseURL != clineDefaultBaseURL {
		t.Fatalf("base-url = %q, want default %s", entry.BaseURL, clineDefaultBaseURL)
	}
}
