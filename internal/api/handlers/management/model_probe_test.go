package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type modelProbeHTTPMockExec struct {
	lastModels []string
	handlers   map[string]error
}

func (m *modelProbeHTTPMockExec) Execute(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	m.lastModels = append(m.lastModels, req.Model)
	if err, ok := m.handlers[req.Model]; ok {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func newHandlerForModelProbe(t *testing.T) (*Handler, *coreauth.Manager) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.ModelProbe.Enabled = true
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	return h, manager
}

func TestModelProbeStatusReportsSections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, manager := newHandlerForModelProbe(t)
	probed := &coreauth.Auth{
		ID:       "cursor-a.json",
		FileName: "cursor-a.json",
		Provider: "cursor",
		Metadata: map[string]any{
			"access_token": "at",
			modelprobe.MetadataKey: &modelprobe.Section{
				CheckedAt: "2026-10-01T00:00:00Z",
				Usable:    []string{"composer-2"},
				Pruned:    []string{"gpt-4o", "claude-3.5-sonnet"},
			},
		},
	}
	probed.EnsureIndex()
	if _, err := manager.Register(context.Background(), probed); err != nil {
		t.Fatalf("register: %v", err)
	}
	unprobed := &coreauth.Auth{ID: "kiro-x.json", FileName: "kiro-x.json", Provider: "kiro", Metadata: map[string]any{"access_token": "k"}}
	unprobed.EnsureIndex()
	if _, err := manager.Register(context.Background(), unprobed); err != nil {
		t.Fatalf("register: %v", err)
	}

	router := gin.New()
	router.GET("/model-probe/status", h.GetModelProbeStatus)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/model-probe/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Enabled          bool             `json:"enabled"`
		SupportedDrivers []string         `json:"supported_drivers"`
		Credentials      []map[string]any `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.Enabled {
		t.Fatal("enabled = false")
	}
	if len(payload.Credentials) != 2 {
		t.Fatalf("credentials = %d: %+v", len(payload.Credentials), payload.Credentials)
	}
	foundProbed, foundUnprobed := false, false
	for _, row := range payload.Credentials {
		if row["auth_file"] == "cursor-a.json" {
			foundProbed = true
			if row["probed"] != true || row["usable"] != 1.0 || row["pruned"] != 2.0 {
				t.Fatalf("probe row: %+v", row)
			}
			if row["checked_at"] != "2026-10-01T00:00:00Z" {
				t.Fatalf("checked_at: %+v", row)
			}
		}
		if row["auth_file"] == "kiro-x.json" {
			foundUnprobed = true
			if row["probed"] != false {
				t.Fatalf("unprobed row: %+v", row)
			}
		}
	}
	if !foundProbed || !foundUnprobed {
		t.Fatalf("missing rows: %+v", payload.Credentials)
	}
}

func TestModelProbeRunInlineWithEngineOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, manager := newHandlerForModelProbe(t)
	mock := &modelProbeHTTPMockExec{handlers: map[string]error{
		"model-y": errors.New("model not enabled"),
	}}
	engine := modelprobe.NewEngine(nil, modelprobe.Options{
		MaxParallel:     1,
		DriverOverrides: map[string]modelprobe.RequestExecutor{"cursor": mock},
	})
	h.SetModelProbeEngineOverride(engine)

	auth := &coreauth.Auth{
		ID:       "cursor-inline.json",
		FileName: "cursor-inline.json",
		Provider: "cursor",
		Metadata: map[string]any{"access_token": "at"},
	}
	auth.EnsureIndex()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "cursor", []*registry.ModelInfo{
		{ID: "composer-2"}, {ID: "model-y"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	router := gin.New()
	router.POST("/model-probe/run", h.PostModelProbeRun)
	router.GET("/model-probe/status", h.GetModelProbeStatus)

	// No target -> 501 with helpful error.
	noTarget := httptest.NewRecorder()
	router.ServeHTTP(noTarget, httptest.NewRequest(http.MethodPost, "/model-probe/run", bytes.NewReader([]byte(`{}`))))
	if noTarget.Code != http.StatusNotImplemented {
		t.Fatalf("no-target status = %d, want 501: %s", noTarget.Code, noTarget.Body.String())
	}

	body := bytes.NewReader([]byte(`{"auth_index":"` + auth.Index + `"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/model-probe/run", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("inline run status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Summary map[string]any   `json:"summary"`
		Models  []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Summary["probed"] != true {
		t.Fatalf("summary = %+v", payload.Summary)
	}
	if len(payload.Models) == 0 {
		t.Fatalf("models detail missing: %+v", payload.Models)
	}

	// Status now shows the section.
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/model-probe/status", nil))
	var statusPayload struct {
		Credentials []map[string]any `json:"credentials"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("status unmarshal: %v", err)
	}
	for _, row := range statusPayload.Credentials {
		if row["auth_file"] == "cursor-inline.json" {
			if row["probed"] != true {
				t.Fatalf("inline probe not reported in status: %+v", row)
			}
		}
	}
}
