package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// zenFreeWire serves a mutable zen /models listing.
type zenFreeWire struct {
	server *httptest.Server
	feed   atomic.Value
}

func newZenFreeWire(t *testing.T, body string) *zenFreeWire {
	t.Helper()
	wire := &zenFreeWire{}
	wire.feed.Store(body)
	wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zen/v1/models" {
			t.Errorf("unexpected zen path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wire.feed.Load().(string)))
	}))
	t.Cleanup(wire.server.Close)
	return wire
}

const zenFreeFeedFull = `{"object":"list","data":[
	{"id":"space-bunny-free","object":"model"},
	{"id":"mimo-v2.5-free","object":"model"},
	{"id":"kimi-k3","object":"model"},
	{"id":"grok-code-fast-1","object":"model"}
]}`

const zenFreeFeedShrunk = `{"object":"list","data":[
	{"id":"mimo-v2.5-free","object":"model"},
	{"id":"kimi-k3","object":"model"}
]}`

func zenFreeAuthForTest(id, baseURL string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: "opencode-go",
		Label:    "opencode-go-apikey",
		Attributes: map[string]string{
			"api_key":  "public",
			"base_url": baseURL,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func zenFreeServiceForTest() (*Service, *config.Config) {
	cfg := &config.Config{}
	return &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}, cfg
}

func zenRegistryIDs(t *testing.T, clientID string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, model := range GlobalModelRegistry().GetModelsForClient(clientID) {
		ids[model.ID] = true
	}
	return ids
}

func TestZenFreeSyncAdvertisesOnlyFreeModels(t *testing.T) {
	wire := newZenFreeWire(t, zenFreeFeedFull)
	s, cfg := zenFreeServiceForTest()
	baseURL := wire.server.URL + "/zen/v1"
	cfg.OpencodeGoKey = []config.OpencodeGoKey{{APIKey: "public", BaseURL: baseURL}}

	auth := zenFreeAuthForTest("zen-free-a.json", baseURL)
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
		usagestats.RegisterZenFreeModelPrices(nil)
	})

	s.applyCoreAuthAddOrUpdate(context.Background(), auth)
	if got := waitForModelCount(t, auth.ID, 3*time.Second); got != 2 {
		t.Fatalf("advertised %d models, want exactly 2 free ids", got)
	}
	ids := zenRegistryIDs(t, auth.ID)
	if !ids["space-bunny-free"] || !ids["mimo-v2.5-free"] {
		t.Fatalf("free ids missing: %+v", ids)
	}
	if ids["kimi-k3"] || ids["grok-code-fast-1"] {
		t.Fatalf("paid ids must not leak onto the anonymous credential: %+v", ids)
	}
	// Successful sync prices the exposed ids at zero in the pricer defaults.
	if !usagestats.IsZeroPricedModel("space-bunny-free") {
		t.Fatal("space-bunny-free must be zero-priced after sync")
	}
	if usagestats.IsZeroPricedModel("kimi-k3") {
		t.Fatal("paid id must not be zero-priced")
	}

	// Feed shrink: the vanished id leaves the effective set on the next
	// refresh cycle, the kept id stays.
	wire.feed.Store(zenFreeFeedShrunk)
	s.applyCoreAuthAddOrUpdate(context.Background(), auth)
	deadline := time.Now().Add(3 * time.Second)
	for {
		ids = zenRegistryIDs(t, auth.ID)
		if len(ids) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ids["mimo-v2.5-free"] || ids["space-bunny-free"] {
		t.Fatalf("after feed shrink ids = %+v, want only mimo-v2.5-free", ids)
	}
	if usagestats.IsZeroPricedModel("space-bunny-free") {
		t.Fatal("vanished id must leave the zero-priced set after re-sync")
	}
}

func TestZenFreeSyncIntersectsConfigListedModels(t *testing.T) {
	wire := newZenFreeWire(t, zenFreeFeedFull)
	s, cfg := zenFreeServiceForTest()
	baseURL := wire.server.URL + "/zen/v1"
	cfg.OpencodeGoKey = []config.OpencodeGoKey{{
		APIKey:  "public",
		BaseURL: baseURL,
		Models: []config.OpencodeGoModel{
			{Name: "space-bunny-free", Alias: "bunny"},
			// Not in the free feed: must not be advertised even while listed.
			{Name: "kimi-k3", Alias: "k3"},
		},
	}}
	t.Cleanup(func() { usagestats.RegisterZenFreeModelPrices(nil) })

	auth := zenFreeAuthForTest("zen-free-b.json", baseURL)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	s.applyCoreAuthAddOrUpdate(context.Background(), auth)
	if got := waitForModelCount(t, auth.ID, 3*time.Second); got != 1 {
		t.Fatalf("advertised %d models, want exactly 1 intersected id", got)
	}
	ids := zenRegistryIDs(t, auth.ID)
	if !ids["bunny"] {
		t.Fatalf("configured free model missing: %+v", ids)
	}
	if ids["k3"] || ids["kimi-k3"] {
		t.Fatalf("configured but non-free model must be filtered: %+v", ids)
	}
}

func TestZenFreeSyncFetchFailureKeepsCachedSet(t *testing.T) {
	wire := newZenFreeWire(t, zenFreeFeedFull)
	s, _ := zenFreeServiceForTest()
	baseURL := wire.server.URL + "/zen/v1"
	auth := zenFreeAuthForTest("zen-free-c.json", baseURL)
	t.Cleanup(func() { usagestats.RegisterZenFreeModelPrices(nil) })

	ids := s.zenFreeModelIDs(context.Background(), auth)
	if len(ids) != 2 {
		t.Fatalf("initial fetch = %v, want 2 free ids", ids)
	}
	wire.server.Close()
	cached := s.zenFreeModelIDs(context.Background(), auth)
	if len(cached) != 2 {
		t.Fatalf("fetch failure must keep the cached snapshot, got %v", cached)
	}
}
