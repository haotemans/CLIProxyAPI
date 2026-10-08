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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// modelTestMockExec is a controllable executor for single-model probe tests:
// results maps model id to the error ProbeOne should classify; a nil entry
// means the probe succeeds.
type modelTestMockExec struct {
	probedModels []string
	results      map[string]error
}

func (m *modelTestMockExec) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	m.probedModels = append(m.probedModels, req.Model)
	if err, ok := m.results[req.Model]; ok {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func newHandlerForModelTest(t *testing.T, mock *modelTestMockExec) (*Handler, *coreauth.Manager) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	engine := modelprobe.NewEngine(cfg, modelprobe.Options{
		DriverOverrides: map[string]modelprobe.RequestExecutor{"opencode-go": mock},
	})
	h.SetModelProbeEngineOverride(engine)
	return h, manager
}

func registerModelTestAuth(t *testing.T, manager *coreauth.Manager) *coreauth.Auth {
	t.Helper()
	auth := &coreauth.Auth{
		ID:       "opencode-go-a.json",
		FileName: "opencode-go-a.json",
		Provider: "opencode-go",
		Metadata: map[string]any{"access_token": "tok"},
	}
	auth.EnsureIndex()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	return auth
}

func serveModelTest(t *testing.T, router *gin.Engine, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestPostModelTestUsable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &modelTestMockExec{results: map[string]error{}}
	h, manager := newHandlerForModelTest(t, mock)
	auth := registerModelTestAuth(t, manager)

	router := gin.New()
	router.POST("/model-test", h.PostModelTest)
	rec := serveModelTest(t, router, "/model-test", `{"auth_index":"`+auth.Index+`","model":"kimi-k2"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		LatencyMS int64  `json:"latency_ms"`
		CheckedMS int64  `json:"checked_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Status != string(modelprobe.StatusUsable) {
		t.Fatalf("status = %q, want usable", payload.Status)
	}
	if payload.Model != "kimi-k2" || payload.Provider != "opencode-go" {
		t.Fatalf("model/provider = %q/%q", payload.Model, payload.Provider)
	}
	if payload.LatencyMS < 0 {
		t.Fatalf("latency_ms = %d", payload.LatencyMS)
	}
	if payload.CheckedMS <= 0 {
		t.Fatalf("checked_ms = %d", payload.CheckedMS)
	}
	if len(mock.probedModels) != 1 || mock.probedModels[0] != "kimi-k2" {
		t.Fatalf("probed models = %v", mock.probedModels)
	}
}

func TestPostModelTestAuthNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &modelTestMockExec{results: map[string]error{}}
	h, _ := newHandlerForModelTest(t, mock)

	router := gin.New()
	router.POST("/model-test", h.PostModelTest)
	rec := serveModelTest(t, router, "/model-test", `{"auth_index":"missing","model":"kimi-k2"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
	if len(mock.probedModels) != 0 {
		t.Fatalf("no probe should run, got %v", mock.probedModels)
	}
}

func TestPostModelTestMissingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &modelTestMockExec{results: map[string]error{}}
	h, manager := newHandlerForModelTest(t, mock)
	auth := registerModelTestAuth(t, manager)

	router := gin.New()
	router.POST("/model-test", h.PostModelTest)
	rec := serveModelTest(t, router, "/model-test", `{"auth_index":"`+auth.Index+`"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if len(mock.probedModels) != 0 {
		t.Fatalf("no probe should run, got %v", mock.probedModels)
	}
}

func TestPostModelTestProbeFailureIsBusinessResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &modelTestMockExec{results: map[string]error{
		// A 403 from the upstream must surface as status+error, not HTTP 5xx.
		"forbidden-model": errors.New("403 permission denied: model not enabled for this tier"),
	}}
	h, manager := newHandlerForModelTest(t, mock)
	auth := registerModelTestAuth(t, manager)

	router := gin.New()
	router.POST("/model-test", h.PostModelTest)
	rec := serveModelTest(t, router, "/model-test", `{"auth_index":"`+auth.Index+`","model":"forbidden-model"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("probe failure must stay HTTP 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Status == string(modelprobe.StatusUsable) {
		t.Fatalf("status = usable for a failing probe: %+v", payload)
	}
	if payload.Error == "" {
		t.Fatalf("error summary empty: %+v", payload)
	}
}

func TestPostModelTestAuthIndexViaQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &modelTestMockExec{results: map[string]error{}}
	h, manager := newHandlerForModelTest(t, mock)
	auth := registerModelTestAuth(t, manager)

	router := gin.New()
	router.POST("/model-test", h.PostModelTest)
	rec := serveModelTest(t, router, "/model-test?auth_index="+auth.Index, `{"model":"kimi-k2"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Status != string(modelprobe.StatusUsable) {
		t.Fatalf("status = %q, want usable", payload.Status)
	}
}
