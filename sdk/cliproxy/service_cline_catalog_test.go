package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	clineauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const clineFixtureRecommended = `{
	"recommended": [{"id": "moonshotai/kimi-k3", "name": "Kimi K3"}],
	"free": [{"id": "cline-free/glm-5", "name": "GLM 5 (Free)"}],
	"clinePass": [{"id": "cline-pass/claude-sonnet-4-6", "name": "Sonnet pass"}],
	"clineCloud": [{"id": "cline-cloud/router", "name": "cloud router"}]
}`

const clineFixtureFullCatalog = `[{"id": "catalog-model-a"}, {"id": "catalog-model-b"}]`

// clineCatalogTestServer serves both the curated feed and the full catalog,
// with a toggle for the feed failing (fallback coverage).
type clineCatalogTestServer struct {
	t            *testing.T
	server       *httptest.Server
	feedStatus   int
	fullStatus   int
	seenFeedPath int
	seenFullPath int
}

func newClineCatalogTestServer(t *testing.T) *clineCatalogTestServer {
	t.Helper()
	fake := &clineCatalogTestServer{t: t, feedStatus: http.StatusOK, fullStatus: http.StatusOK}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ai/cline/recommended-models":
			fake.seenFeedPath++
			if fake.feedStatus != http.StatusOK {
				w.WriteHeader(fake.feedStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": "no feed here"})
				return
			}
			_ = json.NewEncoder(w).Encode(json.RawMessage(clineFixtureRecommended))
		case "/ai/cline/models":
			fake.seenFullPath++
			if fake.fullStatus != http.StatusOK {
				w.WriteHeader(fake.fullStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": "old deployment"})
				return
			}
			_ = json.NewEncoder(w).Encode(json.RawMessage(clineFixtureFullCatalog))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func clineTestOAuthAuth() *coreauth.Auth {
	return &coreauth.Auth{
		Provider: "cline",
		Label:    "user@example.com",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "placeholder",
		},
		Metadata: map[string]any{"auth_kind": "oauth", "access_token": "tok"},
	}
}

func clineTestAPIKeyAuth() *coreauth.Auth {
	return &coreauth.Auth{
		Provider: "cline",
		Attributes: map[string]string{
			"api_key":  "cline-sk-1",
			"base_url": "placeholder",
		},
	}
}

func TestClinePaidTiersEnabled_Matrix(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		auth *coreauth.Auth
		want bool
	}{
		{"oauth default off", &config.Config{}, clineTestOAuthAuth(), false},
		{"apikey always on (subscription key)", &config.Config{}, clineTestAPIKeyAuth(), true},
		{"global knob on", &config.Config{Cline: config.ClineConfig{IncludePaidTiers: boolPtr(true)}}, clineTestOAuthAuth(), true},
		{"global knob off", &config.Config{Cline: config.ClineConfig{IncludePaidTiers: boolPtr(false)}}, clineTestOAuthAuth(), false},
		{"per-credential attr override", &config.Config{}, withIncludePaidAttr(clineTestOAuthAuth(), true), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clinePaidTiersEnabled(tc.cfg, tc.auth); got != tc.want {
				t.Errorf("clinePaidTiersEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func withIncludePaidAttr(auth *coreauth.Auth, value bool) *coreauth.Auth {
	clone := auth.Clone()
	if clone.Attributes == nil {
		clone.Attributes = make(map[string]string)
	}
	if value {
		clone.Attributes["include_paid_tiers"] = "true"
	} else {
		delete(clone.Attributes, "include_paid_tiers")
	}
	return clone
}

func boolPtr(value bool) *bool { return &value }

func TestClineCatalogForAccount_RecommendedFirst(t *testing.T) {
	fake := newClineCatalogTestServer(t)
	auth := clineTestOAuthAuth()
	auth.Attributes["base_url"] = fake.server.URL
	s := &Service{cfg: &config.Config{}}
	svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, "", fake.server.URL)

	models, tiers, err := s.clineCatalogForAccount(context.Background(), svc, auth, "tok")
	if err != nil {
		t.Fatalf("clineCatalogForAccount: %v", err)
	}
	if fake.seenFeedPath != 1 {
		t.Fatalf("feed called %d times, want 1", fake.seenFeedPath)
	}
	if fake.seenFullPath != 0 {
		t.Fatal("full catalog must not be consulted when the feed answers")
	}
	// OAuth defaults keep recommended+free only (no paid tiers).
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2 (recommended+free): %+v", len(models), models)
	}
	if got := tiers["recommended"]; got != 1 {
		t.Fatalf("tiers = %+v", tiers)
	}
	if got := tiers["free"]; got != 1 {
		t.Fatalf("tiers free = %+v", tiers)
	}
	if got := tiers["paid"]; got != 2 {
		t.Fatalf("tiers paid = %+v", tiers)
	}
}

func TestClineCatalogForAccount_APIKeyIncludesPaid(t *testing.T) {
	fake := newClineCatalogTestServer(t)
	auth := clineTestAPIKeyAuth()
	auth.Attributes["base_url"] = fake.server.URL
	s := &Service{cfg: &config.Config{}}
	svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, "", fake.server.URL)

	models, tiers, err := s.clineCatalogForAccount(context.Background(), svc, auth, "tok")
	if err != nil {
		t.Fatalf("clineCatalogForAccount: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %d, want 4 (all tiers for subscription keys): %+v", len(models), models)
	}
	if got := tiers["paid"]; got != 2 {
		t.Fatalf("tiers = %+v", tiers)
	}
}

func TestClineCatalogForAccount_KnobIncludesPaidForOAuth(t *testing.T) {
	fake := newClineCatalogTestServer(t)
	auth := clineTestOAuthAuth()
	auth.Attributes["base_url"] = fake.server.URL
	s := &Service{cfg: &config.Config{Cline: config.ClineConfig{IncludePaidTiers: boolPtr(true)}}}
	svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, "", fake.server.URL)

	models, _, err := s.clineCatalogForAccount(context.Background(), svc, auth, "tok")
	if err != nil {
		t.Fatalf("clineCatalogForAccount: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %d, want 4 (knob on): %+v", len(models), models)
	}
}

func TestClineCatalogForAccount_FeedFailureFallsBackToFullCatalog(t *testing.T) {
	fake := newClineCatalogTestServer(t)
	fake.feedStatus = http.StatusNotFound
	auth := clineTestOAuthAuth()
	auth.Attributes["base_url"] = fake.server.URL
	s := &Service{cfg: &config.Config{}}
	svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, "", fake.server.URL)

	models, tiers, err := s.clineCatalogForAccount(context.Background(), svc, auth, "tok")
	if err != nil {
		t.Fatalf("clineCatalogForAccount (fallback): %v", err)
	}
	if fake.seenFullPath != 1 {
		t.Fatalf("fallback to full catalog must fire exactly once, got %d", fake.seenFullPath)
	}
	if len(models) != 2 || models[0].ID != "catalog-model-a" {
		t.Fatalf("fallback models = %+v", models)
	}
	if tiers != nil {
		t.Fatalf("tier markers must be absent on the imperative-catlog fallback: %+v", tiers)
	}
}

func TestClineCatalogForAccount_TotalFailureSurfacesFeedError(t *testing.T) {
	fake := newClineCatalogTestServer(t)
	fake.feedStatus = http.StatusUnauthorized
	fake.fullStatus = http.StatusUnauthorized
	auth := clineTestOAuthAuth()
	s := &Service{cfg: &config.Config{}}
	svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, "", fake.server.URL)
	models, _, err := s.clineCatalogForAccount(context.Background(), svc, auth, "tok")
	_ = models
	if err == nil {
		t.Fatal("expected an error when both the feed and the fallback fail")
	}
	if got := err.Error(); !strings.Contains(got, "cline recommended models") {
		t.Fatalf("expected the feed error to surface, got %q", got)
	}
}
