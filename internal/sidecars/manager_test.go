package sidecars

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Port = 8317
	cfg.RemoteManagement.SecretKey = "plaintext-key"
	cfg.Unified = config.UnifiedConfig{
		Enabled: true,
		Keeper: config.UnifiedKeeperConfig{
			Enabled: true,
			DataDir: "keeper-data",
		},
		Manager: config.UnifiedManagerConfig{
			Enabled: true,
			DataDir: "manager-data",
		},
	}
	return cfg
}

func TestNewManagerDisabledByDefault(t *testing.T) {
	m := NewManager(nil)
	if m.Enabled() {
		t.Fatal("nil config must produce a disabled manager")
	}
	m = NewManager(&config.Config{})
	if m.Enabled() {
		t.Fatal("zero unified config must be disabled")
	}
	if !m.unified.Enabled {
		// no-op sanity
	}
}

func TestEnabledRequiresUmbrella(t *testing.T) {
	cfg := testConfig()
	cfg.Unified.Enabled = false
	m := NewManager(cfg)
	if m.Enabled() {
		t.Fatal("disabled umbrella must disable sidecars")
	}
	cfg.Unified.Enabled = true
	cfg.Unified.Keeper.Enabled = false
	cfg.Unified.Manager.Enabled = false
	if NewManager(cfg).Enabled() {
		t.Fatal("no sidecar enabled")
	}
	cfg.Unified.Keeper.Enabled = true
	if !NewManager(cfg).Enabled() {
		t.Fatal("keeper enabled must enable manager")
	}
}

func TestManagementKeyFallbacks(t *testing.T) {
	cfg := testConfig()
	cfg.Unified.ManagementKey = "explicit"
	if got := NewManager(cfg).managementKey(cfg); got != "explicit" {
		t.Fatalf("managementKey = %q, want explicit", got)
	}
	cfg = testConfig()
	if got := NewManager(cfg).managementKey(cfg); got != "plaintext-key" {
		t.Fatalf("managementKey fallback = %q, want plaintext-key", got)
	}
	cfg = testConfig()
	cfg.RemoteManagement.SecretKey = "$2a$10$bcrypthash"
	if got := NewManager(cfg).managementKey(cfg); got != "" {
		t.Fatalf("bcrypt secret must not be shared, got %q", got)
	}
	cfg = testConfig()
	cfg.RemoteManagement.SecretKey = ""
	if got := NewManager(cfg).managementKey(cfg); got != "" {
		t.Fatalf("empty secrets must produce empty key, got %q", got)
	}
	if got := NewManager(testConfig()).managementKey(nil); got != "" {
		t.Fatalf("nil cfg must produce empty key, got %q", got)
	}
}

func TestCPABaseURL(t *testing.T) {
	m := NewManager(nil)
	if got := m.cpaBaseURL(nil); got != "http://127.0.0.1:8317" {
		t.Fatalf("default base url = %q", got)
	}
	cfg := &config.Config{}
	cfg.Port = 9000
	if got := m.cpaBaseURL(cfg); got != "http://127.0.0.1:9000" {
		t.Fatalf("port base url = %q", got)
	}
	cfg.TLS.Enable = true
	if got := m.cpaBaseURL(cfg); got != "https://127.0.0.1:9000" {
		t.Fatalf("tls base url = %q", got)
	}
}

func TestRegisterRoutesMountsOnlyEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	m := NewManager(testConfig())
	engine := gin.New()
	m.RegisterRoutes(engine)
	paths := map[string]bool{}
	for _, ri := range engine.Routes() {
		paths[ri.Path] = true
	}
	if !paths[KeeperBasePath] || !paths[KeeperBasePath+"/*proxyPath"] {
		t.Fatalf("keeper routes missing: %v", paths)
	}
	if !paths[ManagerBasePath] || !paths[ManagerBasePath+"/*proxyPath"] {
		t.Fatalf("manager routes missing: %v", paths)
	}
}

func TestRegisterRoutesDisabledIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := NewManager(&config.Config{})
	engine := gin.New()
	m.RegisterRoutes(engine)
	if len(engine.Routes()) != 0 {
		t.Fatalf("disabled manager must not register routes: %v", engine.Routes())
	}
}

func TestProxyForStripPrefixes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	target := strings.TrimPrefix(backend.URL, "http://")

	for _, tc := range []struct {
		name     string
		basePath string
		strip    bool
		in       string
		want     string
	}{
		{"strip manager", ManagerBasePath, true, "/manager/health", "/health"},
		{"strip manager root", ManagerBasePath, true, "/manager/", "/"},
		{"preserve keeper", KeeperBasePath, false, "/keeper/healthz", "/keeper/healthz"},
		{"preserve keeper root", KeeperBasePath, false, "/keeper/", "/keeper/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyHandler, err := proxyFor(target, tc.basePath, tc.strip)
			if err != nil {
				t.Fatalf("proxyFor: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, tc.in, nil)
			rec := httptest.NewRecorder()
			proxyHandler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Path"); got != tc.want {
				t.Fatalf("backend path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := NewManager(testConfig())
	m.mu.Lock()
	m.entries = m.buildEntries()
	m.mu.Unlock()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	m.Status(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Enabled  bool `json:"enabled"`
		Sidecars []struct {
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
			Running   bool   `json:"running"`
			BasePath  string `json:"base_path"`
			Target    string `json:"target"`
			LastError string `json:"last_error"`
			Note      string `json:"note"`
		} `json:"sidecars"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if !body.Enabled {
		t.Fatal("enabled should be true")
	}
	if len(body.Sidecars) != 2 {
		t.Fatalf("sidecars = %d, want 2", len(body.Sidecars))
	}
	var keeper, managerEntry *struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Running   bool   `json:"running"`
		BasePath  string `json:"base_path"`
		Target    string `json:"target"`
		LastError string `json:"last_error"`
		Note      string `json:"note"`
	}
	for i := range body.Sidecars {
		switch body.Sidecars[i].Name {
		case nameKeeper:
			keeper = &body.Sidecars[i]
		case nameManager:
			managerEntry = &body.Sidecars[i]
		}
	}
	if keeper == nil || managerEntry == nil {
		t.Fatalf("missing entries: %+v", body.Sidecars)
	}
	if keeper.Enabled {
		t.Fatal("keeper without login password must be force-disabled")
	}
	if !strings.Contains(keeper.LastError, "login-password") {
		t.Fatalf("keeper last_error = %q", keeper.LastError)
	}
	if managerEntry.BasePath != ManagerBasePath || managerEntry.Target != ManagerTarget {
		t.Fatalf("manager entry = %+v", managerEntry)
	}
	if managerEntry.Note == "" {
		t.Fatal("manager note (SPA caveat) should be present")
	}
}

func TestStatusJSONWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := NewManager(nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	m.Status(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Enabled  bool  `json:"enabled"`
		Sidecars []any `json:"sidecars"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Enabled {
		t.Fatal("disabled manager must report enabled=false")
	}
	if len(body.Sidecars) != 0 {
		t.Fatalf("sidecars must be empty, got %v", body.Sidecars)
	}
}

func TestStartStopNoSidecarsStartedWhenDisabled(t *testing.T) {
	m := NewManager(&config.Config{})
	m.Start(&config.Config{})
	m.Stop(context.Background())
	if m.cancel != nil {
		t.Fatal("disabled manager must not create lifecycle context")
	}
}

func TestStartAndStop(t *testing.T) {
	cfg := testConfig()
	// Isolate all manager artifacts in a temp dir: the runner may create a
	// database, data key and process lock as part of its (failing fast) boot.
	cfg.Unified.Manager.DataDir = t.TempDir()
	// Keeper force-disabled by missing password; manager starts and then fails
	// quickly (no valid upstream) — Start must not block and Stop must return.
	m := NewManager(cfg)
	m.Start(cfg)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		started := true
		for _, e := range m.entries {
			if e.Enabled && !e.started {
				started = false
			}
		}
		m.mu.RUnlock()
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop(context.Background())
}
