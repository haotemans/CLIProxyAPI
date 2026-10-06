package config

import "testing"

func TestParseConfigBytesDevinAPIKey(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`devin-api-key:
  - api-key: " devin-key "
    priority: 3
    weight: 5
    prefix: " team-devin "
    base-url: " https://custom.devin.example.com "
    websockets: true
    alpha-search: true
    proxy-url: " http://proxy.local "
    headers:
      X-Custom: value
    models:
      - name: devin/swe-2
        alias: swe-2
        display-name: SWE 2
        force-mapping: true
    excluded-models:
      - " devin/swe-1-* "
    disable-cooling: true
    request-retry: 0
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.DevinKey) != 1 {
		t.Fatalf("devin-api-key count = %d, want 1", len(cfg.DevinKey))
	}
	entry := cfg.DevinKey[0]
	if entry.APIKey != "devin-key" {
		t.Fatalf("api-key = %q, want devin-key", entry.APIKey)
	}
	if entry.Priority != 3 {
		t.Fatalf("priority = %d, want 3", entry.Priority)
	}
	if entry.Weight == nil || *entry.Weight != 5 {
		t.Fatalf("weight = %v, want 5", entry.Weight)
	}
	if entry.Prefix != "team-devin" {
		t.Fatalf("prefix = %q, want team-devin", entry.Prefix)
	}
	if entry.BaseURL != "https://custom.devin.example.com" {
		t.Fatalf("base-url = %q, want https://custom.devin.example.com", entry.BaseURL)
	}
	if entry.Websockets {
		t.Fatal("websockets = true, want false (devin has no websocket transport)")
	}
	if entry.AlphaSearch {
		t.Fatal("alpha-search = true, want false (devin has no alpha search)")
	}
	if entry.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy-url = %q, want http://proxy.local", entry.ProxyURL)
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", entry.DisableCooling)
	}
	if entry.RequestRetry == nil || *entry.RequestRetry != 0 {
		t.Fatalf("request-retry = %v, want 0", entry.RequestRetry)
	}
	if entry.Headers["X-Custom"] != "value" {
		t.Fatalf("X-Custom header = %q, want value", entry.Headers["X-Custom"])
	}
	if len(entry.Models) != 1 {
		t.Fatalf("model count = %d, want 1", len(entry.Models))
	}
	model := entry.Models[0]
	if model.Name != "devin/swe-2" || model.Alias != "swe-2" || model.DisplayName != "SWE 2" || !model.ForceMapping {
		t.Fatalf("unexpected model mapping: %+v", model)
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "devin/swe-1-*" {
		t.Fatalf("excluded-models = %#v, want [devin/swe-1-*]", entry.ExcludedModels)
	}
}

func TestParseConfigBytesDevinAPIKeyDefaultsBaseURL(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`devin-api-key:
  - api-key: "devin-key"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.DevinKey) != 1 {
		t.Fatalf("devin-api-key count = %d, want 1", len(cfg.DevinKey))
	}
	if cfg.DevinKey[0].BaseURL != "https://server.codeium.com" {
		t.Fatalf("base-url = %q, want https://server.codeium.com", cfg.DevinKey[0].BaseURL)
	}
}

func TestParseConfigBytesDevinAPIKeyDropsEmptyKey(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`devin-api-key:
  - api-key: "kept"
  - api-key: "  "
  - base-url: "https://custom.devin.example.com"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.DevinKey) != 1 {
		t.Fatalf("devin-api-key count = %d, want 1", len(cfg.DevinKey))
	}
	if cfg.DevinKey[0].APIKey != "kept" {
		t.Fatalf("api-key = %q, want kept", cfg.DevinKey[0].APIKey)
	}
	if cfg.DevinKey[0].BaseURL != "https://server.codeium.com" {
		t.Fatalf("base-url = %q, want https://server.codeium.com", cfg.DevinKey[0].BaseURL)
	}
}
