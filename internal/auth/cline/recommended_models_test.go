package cline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const recommendedFixture = `{
	"recommended": [
		{"id": "moonshotai/kimi-k3", "name": "Kimi K3"},
		{"id": "anthropic/claude-sonnet-4-6", "name": "Claude Sonnet 4.6"}
	],
	"free": [
		{"id": "cline-free/kimi-k3", "name": "Kimi K3 (Free)"},
		{"id": "cline-free/glm-5", "name": "GLM 5 (Free)"},
		{"id": "moonshotai/kimi-k3", "name": "duplicate of recommended"}
	],
	"clinePass": [
		{"id": "cline-pass/claude-sonnet-4-6", "name": "Claude Sonnet 4.6 (Pass)"},
		{"id": "cline-pass/gpt-5", "name": "GPT 5 (Pass)"}
	],
	"clineCloud": [
		{"id": "cline-cloud/router", "name": "Cloud router"}
	]
}`

func TestParseRecommendedModels_BucketMergeAndDedupe(t *testing.T) {
	catalog, err := ParseRecommendedModels([]byte(recommendedFixture))
	if err != nil {
		t.Fatalf("ParseRecommendedModels: %v", err)
	}
	if got := catalog.TierCounts(); got["recommended"] != 2 || got["free"] != 3 || got["paid"] != 3 {
		t.Fatalf("tier counts = %+v, want {2 3 3}", got)
	}

	flat := catalog.Flat(false)
	if len(flat) != 4 {
		t.Fatalf("flat(false) = %d models, want 4 (recommended+free, deduped: 2+3-1): %+v", len(flat), flat)
	}

	full := catalog.Flat(true)
	if len(full) != 7 {
		t.Fatalf("flat(true) = %d models, want 7 (with paid tiers, deduped): %+v", len(full), full)
	}
	ids := map[string]bool{}
	for _, model := range full {
		ids[model.ID] = true
	}
	for _, want := range []string{"cline-pass/claude-sonnet-4-6", "cline-cloud/router", "cline-free/glm-5"} {
		if !ids[want] {
			t.Fatalf("missing %s in full catalog: %+v", want, ids)
		}
	}
	// Dedup keeps the first-seen entry (recommended's kimi over free's duplicate).
	if strings.Contains(strings.ToLower(full[0].Name), "duplicate") {
		t.Fatal("duplicate entry must not shadow the first-seen one")
	}
}

func TestParseRecommendedModels_KeyVariants(t *testing.T) {
	variant := `{"cline-pass": [{"id": "cline-pass/a"}], "cline_cloud": [{"id": "cline-cloud/b"}]}`
	catalog, err := ParseRecommendedModels([]byte(variant))
	if err != nil {
		t.Fatalf("ParseRecommendedModels(variant): %v", err)
	}
	if len(catalog.ClinePass) != 1 || len(catalog.ClineCloud) != 1 {
		t.Fatalf("variant keys not accepted: %+v", catalog)
	}
}

func newRecommendedTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestFetchRecommendedModels_EndpointAndHeaders(t *testing.T) {
	var seenPath, seenAuth, seenUA, seenVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		seenUA = r.Header.Get("User-Agent")
		seenVersion = r.Header.Get("X-CLIENT-VERSION")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(recommendedFixture))
	}))
	t.Cleanup(server.Close)

	feed := NewClineAuthWithProxyURLAndBaseURL(&config.Config{}, "", server.URL)
	catalog, err := feed.FetchRecommendedModels(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchRecommendedModels: %v", err)
	}
	if seenPath != "/ai/cline/recommended-models" {
		t.Fatalf("path = %q", seenPath)
	}
	if seenAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q", seenAuth)
	}
	if !strings.HasPrefix(seenUA, "Cline/") || seenVersion == "" {
		t.Fatalf("client headers missing: ua=%q version=%q", seenUA, seenVersion)
	}
	if got := len(catalog.Flat(false)); got != 4 {
		t.Fatalf("models = %d, want 4 (2 recommended + 3 free - 1 dup)", got)
	}
}

func TestFetchRecommendedModels_Errors(t *testing.T) {
	server := newRecommendedTestServer(t, http.StatusNotFound, `{"detail":"not found"}`)
	t.Cleanup(server.Close)
	feed := NewClineAuthWithProxyURLAndBaseURL(&config.Config{}, "", server.URL)
	if _, err := feed.FetchRecommendedModels(context.Background(), "tok"); err == nil {
		t.Fatal("404 feed must error (so callers fall back to the full catalog)")
	}
	if _, err := feed.FetchRecommendedModels(context.Background(), ""); err == nil {
		t.Fatal("empty token must error")
	}
}
