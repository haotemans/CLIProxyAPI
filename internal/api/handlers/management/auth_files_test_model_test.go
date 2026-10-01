package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// testModelWire fakes an OpenAI chat-completions relay. status/answer are
// returned for every request; lastModel records the model the executor sent.
type testModelWire struct {
	status    int
	answer    string
	lastModel atomic.Value
}

func (w *testModelWire) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.lastModel.Store(string(body))
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(w.status)
		_, _ = rw.Write([]byte(w.answer))
	})
	return mux
}

const testModelCompletion = `{"id":"chatcmpl-1","object":"chat.completion",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"ok ok ok ok ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`

func newTestModelHandler(t *testing.T, wire *testModelWire, provider, fileName string, models ...string) (*Handler, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	auth := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: provider,
		Attributes: map[string]string{
			"api_key":  provider + "-key",
			"base_url": wireBaseURL(t, wire),
		},
	}
	auth.EnsureIndex()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(models) > 0 {
		infos := make([]*registry.ModelInfo, 0, len(models))
		for _, id := range models {
			infos = append(infos, &registry.ModelInfo{ID: id})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, infos)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	return h, auth
}

func wireBaseURL(t *testing.T, wire *testModelWire) string {
	t.Helper()
	srv := httptest.NewServer(wire.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func testModelRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth-files/test-model", h.PostAuthFileTestModel)
	return router
}

func postTestModel(t *testing.T, router *gin.Engine, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth-files/test-model", bytes.NewReader([]byte(body))))
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("status=%d unmarshal: %v body=%s", rec.Code, err, rec.Body.String())
	}
	return rec, payload
}

func TestAuthFileTestModel_Success(t *testing.T) {
	wire := &testModelWire{status: http.StatusOK, answer: testModelCompletion}
	h, auth := newTestModelHandler(t, wire, "commandcode", "cc.json", "deepseek-v4.1-flash")
	router := testModelRouter(h)

	// Case-insensitive id resolves to the canonical registered id upstream.
	rec, payload := postTestModel(t, router, `{"auth_index":"`+auth.Index+`","model":"DEEPSEEK-V4.1-FLASH"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %+v", rec.Code, payload)
	}
	if payload["ok"] != true || payload["status"] != "usable" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload["model"] != "deepseek-v4.1-flash" {
		t.Fatalf("model = %+v", payload["model"])
	}
	if payload["reply_text"] != "ok ok ok ok ok" {
		t.Fatalf("reply_text = %+v", payload["reply_text"])
	}
	usage, ok := payload["usage"].(map[string]any)
	if !ok || usage["input"] != 12.0 || usage["output"] != 5.0 {
		t.Fatalf("usage = %+v", payload["usage"])
	}
	if _, okLatency := payload["latency_ms"]; !okLatency {
		t.Fatalf("latency_ms missing: %+v", payload)
	}
	sent, _ := wire.lastModel.Load().(string)
	if sent == "" || !bytes.Contains([]byte(sent), []byte(`"model":"deepseek-v4.1-flash"`)) {
		t.Fatalf("upstream did not see canonical model, body=%s", sent)
	}
	if !bytes.Contains([]byte(sent), []byte("say ok in five words")) {
		t.Fatalf("upstream did not see test prompt, body=%s", sent)
	}
}

func TestAuthFileTestModel_AuthFileResolutionAndUpstreamFailure(t *testing.T) {
	wire := &testModelWire{status: http.StatusUnauthorized, answer: `{"error":{"message":"invalid api key"}}`}
	h, _ := newTestModelHandler(t, wire, "opencode-go", "oc.json", "gpt-5")
	router := testModelRouter(h)

	rec, payload := postTestModel(t, router, `{"auth_file":"oc.json","model":"gpt-5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %+v", rec.Code, payload)
	}
	if payload["ok"] != false || payload["status"] != "auth_error" {
		t.Fatalf("payload = %+v", payload)
	}
	if errText, _ := payload["error"].(string); errText == "" {
		t.Fatalf("error missing: %+v", payload)
	}
}

func TestAuthFileTestModel_RequestLevelFailures(t *testing.T) {
	wire := &testModelWire{status: http.StatusOK, answer: testModelCompletion}
	h, auth := newTestModelHandler(t, wire, "commandcode", "cc.json", "deepseek-v4.1-flash")
	router := testModelRouter(h)

	unsupported := &coreauth.Auth{ID: "gem.json", FileName: "gem.json", Provider: "gemini"}
	unsupported.EnsureIndex()
	if _, err := h.authManager.Register(context.Background(), unsupported); err != nil {
		t.Fatalf("register unsupported: %v", err)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing model", `{"auth_index":"` + auth.Index + `"}`, http.StatusBadRequest},
		{"auth not found", `{"auth_index":"nope","model":"deepseek-v4.1-flash"}`, http.StatusNotFound},
		{"unsupported provider", `{"auth_index":"` + unsupported.Index + `","model":"gemini-2.5-pro"}`, http.StatusNotImplemented},
		{"model not in effective set", `{"auth_index":"` + auth.Index + `","model":"gpt-4o"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, payload := postTestModel(t, router, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %+v", rec.Code, tc.want, payload)
			}
			if tc.name == "model not in effective set" {
				models, ok := payload["models"].([]any)
				if !ok || len(models) != 1 || models[0] != "deepseek-v4.1-flash" {
					t.Fatalf("models hint = %+v", payload["models"])
				}
			}
		})
	}
}
