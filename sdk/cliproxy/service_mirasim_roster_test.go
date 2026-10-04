package cliproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// mirasimRelayWire fakes the relay control plane (no signature check).
type mirasimRelayWire struct {
	server   *httptest.Server
	models   atomic.Value
	failCode atomic.Int32
}

func newMirasimRelayWire(t *testing.T, modelsBody string) *mirasimRelayWire {
	t.Helper()
	wire := &mirasimRelayWire{}
	wire.models.Store(modelsBody)
	wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if code := wire.failCode.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wire.models.Load().(string)))
	}))
	t.Cleanup(wire.server.Close)
	return wire
}

func mirasimOAuthAuthForTest(t *testing.T, id, relayURL string, metadata map[string]any) *coreauth.Auth {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("device key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal device key: %v", err)
	}
	identity := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	auth := &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: "mirasim",
		Label:    "mirasim-oauth",
		Metadata: map[string]any{
			"access_token":       "at",
			"device_private_key": identity,
			"relay_url":          relayURL,
			"type":               "mirasim",
			"plan":               "go",
		},
	}
	for key, value := range metadata {
		auth.Metadata[key] = value
	}
	return auth
}

const mirasimRosterBody = `{"data":[
	{"id":"claude-sonnet-4-6","object":"model"},
	{"id":"gpt-5.2"},
	{"id":"vendor/claude-3-7-sonnet"},
	{"id":"claude-haiku-4-5-20251001"},
	{"id":"claude-haiku-4-5"}
]}`

func TestMirasimModelFamilyPrefixes(t *testing.T) {
	for id, want := range map[string]string{
		"kimi-k3":                      "kimi",
		"glm-5.3-flash":                "glm",
		"deepseek-flash":               "deepseek",
		"deepseek-v4-flash-vision-exp": "deepseek",
		"claude-sonnet-4-6":            "claude",
		"gpt-5.2":                      "mirasim",
		"something-new-1":              "mirasim",
	} {
		if got := mirasimModelFamily(id); got != want {
			t.Fatalf("mirasimModelFamily(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestMirasimRosterRegisteredPerCredential(t *testing.T) {
	wire := newMirasimRelayWire(t, mirasimRosterBody)
	tmpDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(tmpDir)
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(nil) })

	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := mirasimOAuthAuthForTest(t, "mirasim-roster-a.json", wire.server.URL, nil)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	s.applyCoreAuthAddOrUpdate(context.Background(), auth)
	if got := waitForModelCount(t, auth.ID, 3*time.Second); got != 3 {
		t.Fatalf("advertised %d models, want the 3 servable roster ids", got)
	}
	ids := zenRegistryIDs(t, auth.ID)
	for _, want := range []string{"claude-sonnet-4-6", "gpt-5.2", "claude-haiku-4-5"} {
		if !ids[want] {
			t.Fatalf("roster id %s missing from registry: %v", want, ids)
		}
	}
	// Family badges follow the true id family: the static claude clone keeps
	// "claude", relay-only families map by prefix, unknown falls to mirasim.
	typesByID := map[string]string{}
	for _, model := range GlobalModelRegistry().GetModelsForClient(auth.ID) {
		typesByID[model.ID] = model.Type
	}
	for id, wantType := range map[string]string{
		"claude-sonnet-4-6": "claude",
		"gpt-5.2":           "mirasim",
		"claude-haiku-4-5":  "claude",
	} {
		if got := typesByID[id]; got != wantType {
			t.Fatalf("type[%s] = %q, want %q", id, got, wantType)
		}
	}
	if ids["vendor/claude-3-7-sonnet"] || ids["claude-haiku-4-5-20251001"] {
		t.Fatalf("non-servable ids leaked: %v", ids)
	}

	// The successfully fetched roster persists as last-good into the auth file.
	raw, errRead := os.ReadFile(filepath.Join(tmpDir, auth.ID))
	if errRead != nil {
		t.Fatalf("auth file missing after roster persist: %v", errRead)
	}
	if !strings.Contains(string(raw), "mirasim_models") || !strings.Contains(string(raw), "gpt-5.2") {
		t.Fatalf("roster not persisted: %s", raw)
	}
}

func TestMirasimRosterFallbackChain(t *testing.T) {
	wire := newMirasimRelayWire(t, mirasimRosterBody)
	wire.failCode.Store(500)
	// The recovery step persists through the token store; point it at a temp
	// dir so tests never write auth files into the repo tree.
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(t.TempDir())
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(nil) })
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}

	// Relay down + persisted last-good roster: the persisted ids win.
	authPersisted := mirasimOAuthAuthForTest(t, "mirasim-roster-b.json", wire.server.URL, map[string]any{
		mirasimauth.ModelsMetadataKey: []string{"saved-model-a", "saved-model-b"},
	})
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authPersisted.ID) })
	s.applyCoreAuthAddOrUpdate(context.Background(), authPersisted)
	if got := waitForModelCount(t, authPersisted.ID, 3*time.Second); got != 2 {
		t.Fatalf("last-good advertise = %d, want 2", got)
	}
	ids := zenRegistryIDs(t, authPersisted.ID)
	if !ids["saved-model-a"] || !ids["saved-model-b"] {
		t.Fatalf("last-good roster = %v", ids)
	}

	// Relay down + no last-good: static Claude catalog (never empty).
	authPlain := mirasimOAuthAuthForTest(t, "mirasim-roster-c.json", wire.server.URL, nil)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authPlain.ID) })
	s.applyCoreAuthAddOrUpdate(context.Background(), authPlain)
	if got := waitForModelCount(t, authPlain.ID, 3*time.Second); got < 10 {
		t.Fatalf("static fallback = %d, want the full claude catalog", got)
	}

	// Relay recovers mid-flight: the roster fetch wins over last-good entries.
	wire.failCode.Store(0)
	s.applyCoreAuthAddOrUpdate(context.Background(), authPlain)
	deadline := time.Now().Add(3 * time.Second)
	for {
		ids = zenRegistryIDs(t, authPlain.ID)
		if ids["gpt-5.2"] || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ids["gpt-5.2"] {
		t.Fatalf("recovered roster must replace the fallback: %v", ids)
	}
}
