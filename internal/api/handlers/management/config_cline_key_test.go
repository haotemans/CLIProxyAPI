package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGetClineKeysEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/cline-api-key", nil)
	h.GetClineKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []json.RawMessage `json:"cline-api-key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal GET body: %v", err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("items = %v, want empty", payload.Items)
	}
}

func TestPutClineKeysDefaultsAndWeightValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/cline-api-key",
		strings.NewReader(`[
			{"api-key":"cline-sk-1","prefix":"c","models":[{"name":"moonshotai/kimi-k3","alias":"kimi-k3"}],"weight":3,"cloak":{"mode":"always"},"fingerprint-profile":"claude-code-cli"},
			{"api-key":"  "},
			{"api-key":"cline-sk-empty-base","base-url":"  "}
		]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClineKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.ClineKey) != 2 {
		t.Fatalf("ClineKey length = %d, want 2 (empty api-key dropped): %+v", len(h.cfg.ClineKey), h.cfg.ClineKey)
	}
	entry := h.cfg.ClineKey[0]
	if entry.APIKey != "cline-sk-1" || entry.BaseURL != "https://api.cline.bot/api/v1" || entry.Prefix != "c" {
		t.Fatalf("entry identity = %+v", entry)
	}
	if entry.Cloak != nil || entry.FingerprintProfile != "" || entry.ExperimentalCCHSigning {
		t.Fatalf("claude-only knobs must be stripped: %+v", entry)
	}
	if entry.Weight == nil || *entry.Weight != 3 {
		t.Fatalf("weight = %+v", entry.Weight)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "moonshotai/kimi-k3" {
		t.Fatalf("models = %+v", entry.Models)
	}
	if !strings.HasPrefix(entry.BaseURL, "https://api.cline.bot") {
		t.Fatalf("default base must be public account API: %q", entry.BaseURL)
	}

	// Empty-key rejection: PUT with a bad weight fails hard.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/cline-api-key",
		strings.NewReader(`[{"api-key":"cline-sk-2","weight":"not-a-number"}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClineKeys(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad weight status = %d, want 400", rec.Code)
	}
}

func TestPatchAndDeleteClineKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}

	putRecorder := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRecorder)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/cline-api-key",
		strings.NewReader(`[{"api-key":"cline-sk-x","base-url":"https://mirror.example.com/api/v1"}]`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutClineKeys(putCtx)
	if putRecorder.Code != http.StatusOK || len(h.cfg.ClineKey) != 1 {
		t.Fatalf("seed PUT failed: %d %+v", putRecorder.Code, h.cfg.ClineKey)
	}

	// PATCH with an explicit empty base-url resets to the default instead of dropping.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/cline-api-key",
		strings.NewReader(`{"index":0,"value":{"base-url":""}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClineKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.ClineKey) != 1 || h.cfg.ClineKey[0].BaseURL != "https://api.cline.bot/api/v1" {
		t.Fatalf("entry after base-url reset: %+v", h.cfg.ClineKey)
	}

	// PATCH by api-key match.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/cline-api-key",
		strings.NewReader(`{"match":"cline-sk-x","value":{"prefix":"p","base-url":"https://mirror.example.com/api/v1"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClineKey(ctx)
	if rec.Code != http.StatusOK || h.cfg.ClineKey[0].Prefix != "p" || h.cfg.ClineKey[0].BaseURL != "https://mirror.example.com/api/v1" {
		t.Fatalf("PATCH by match: %+v", h.cfg.ClineKey[0])
	}

	// DELETE by api-key.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/cline-api-key?api-key=cline-sk-x", nil)
	h.DeleteClineKey(ctx)
	if rec.Code != http.StatusOK || len(h.cfg.ClineKey) != 0 {
		t.Fatalf("DELETE status = %d, entries = %+v", rec.Code, h.cfg.ClineKey)
	}
}
