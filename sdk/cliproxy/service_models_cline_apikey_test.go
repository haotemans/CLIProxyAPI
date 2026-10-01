package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// clineConfigSynthAuthForTest mirrors the auth the config synthesizer produces
// for an api-keys.cline entry: Attributes carry the api key and base URL and
// auth_kind stays apikey.
func clineConfigSynthAuthForTest(baseURL, apiKey string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "cline:apikey:test",
		Provider: "cline",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  apiKey,
			"base_url": baseURL,
		},
	}
}

func newClineDiscoveryServer(t *testing.T, status int, models any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/cline/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cline-sk-discovery" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"latest version of Cline requires an API key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(models)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDiscoverClineModels_HappyPathBucketMerge(t *testing.T) {
	server := newClineDiscoveryServer(t, http.StatusOK, map[string]any{
		"recommended": []any{
			map[string]any{"id": "moonshotai/kimi-k3", "name": "kimi-k3", "description": "flagship"},
			map[string]any{"id": "anthropic/claude-sonnet-4-6", "name": "Claude Sonnet 4.6"},
		},
		"free": []any{
			map[string]any{"id": "deepseek/deepseek-v4.1-flash", "name": "DeepSeek Flash"},
			map[string]any{"id": "moonshotai/kimi-k3", "name": "kimi-k3"},
		},
	})
	s := &Service{cfg: &config.Config{}}
	auth := clineConfigSynthAuthForTest(server.URL, "cline-sk-discovery")

	models := s.discoverClineModels(context.Background(), auth)
	if len(models) != 3 {
		t.Fatalf("discovered %d models, want 3 (buckets merged and deduped): %+v", len(models), models)
	}
	// Bucket merge deduplicates by id; ordering follows Go map iteration, so
	// assert membership rather than sequence.
	ids := map[string]bool{}
	for _, model := range models {
		ids[model.ID] = true
	}
	for _, want := range []string{"moonshotai/kimi-k3", "anthropic/claude-sonnet-4-6", "deepseek/deepseek-v4.1-flash"} {
		if !ids[want] {
			t.Fatalf("missing %s in discovered set: %+v", want, ids)
		}
	}
}

func TestDiscoverClineModels_UnreachableFallsBack(t *testing.T) {
	// Chat returns 401 without an API key: discovery must silently fall back.
	server := newClineDiscoveryServer(t, http.StatusUnauthorized, map[string]any{})
	s := &Service{cfg: &config.Config{}}
	auth := clineConfigSynthAuthForTest(server.URL, "wrong-key")
	if got := s.discoverClineModels(context.Background(), auth); got != nil {
		t.Fatalf("unauthorized discovery = %+v, want nil fallback", got)
	}

	// An unreachable endpoint must also fall back rather than error.
	dead := "http://127.0.0.1:1"
	auth = clineConfigSynthAuthForTest(dead, "cline-sk-discovery")
	if got := s.discoverClineModels(context.Background(), auth); got != nil {
		t.Fatalf("unreachable discovery = %+v, want nil fallback", got)
	}
}

func TestRegisterModelsForAuth_ClineAPIKeyDiscoveryWins(t *testing.T) {
	server := newClineDiscoveryServer(t, http.StatusOK, []any{
		map[string]any{"id": "moonshotai/kimi-k3", "name": "kimi-k3"},
	})
	cfg := &config.Config{
		ClineKey: []config.ClineKey{{
			APIKey:  "cline-sk-discovery",
			BaseURL: server.URL,
		}},
	}
	s := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := clineConfigSynthAuthForTest(server.URL, "cline-sk-discovery")

	ctx := context.Background()
	s.applyCoreAuthAddOrUpdate(ctx, auth)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(auth.ID)) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("discovery must override the static catalog: got %d models", len(models))
	}
	if got := models[0].ID; got != "moonshotai/kimi-k3" {
		t.Fatalf("first model = %q, want the discovered kimi-k3", got)
	}
}

func TestRegisterModelsForAuth_ClineAPIKeyStaticFallback(t *testing.T) {
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := clineConfigSynthAuthForTest("http://127.0.0.1:1", "cline-sk-discovery")

	ctx := context.Background()
	s.applyCoreAuthAddOrUpdate(ctx, auth)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(auth.ID)) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("static catalog must fill in when discovery is unreachable")
	}
	registryCount := len(registry.GetClineModels())
	if len(models) != registryCount {
		t.Logf("fallback catalog size = %d (registry cline %d)", len(models), registryCount)
	}
}
