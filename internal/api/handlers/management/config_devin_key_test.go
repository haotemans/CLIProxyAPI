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

func TestPatchDevinKeyUpdatesExecutionFields(t *testing.T) {
	disableCooling := false
	h := &Handler{
		cfg: &config.Config{DevinKey: []config.DevinKey{{
			APIKey:         "devin-key",
			Priority:       1,
			BaseURL:        "https://server.codeium.com",
			DisableCooling: &disableCooling,
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/devin-api-key", strings.NewReader(`{
		"index": 0,
		"value": {
			"priority": 7,
			"websockets": true,
			"disable-cooling": true,
			"request-retry": 0
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchDevinKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.DevinKey[0]
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if entry.Websockets {
		t.Fatal("websockets = true, want false (forced off by SanitizeDevinKeys)")
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", entry.DisableCooling)
	}
	if entry.RequestRetry == nil || *entry.RequestRetry != 0 {
		t.Fatalf("request-retry = %v, want 0", entry.RequestRetry)
	}
}

func TestPutDevinKeysDefaultsBaseURL(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/devin-api-key", strings.NewReader(`[
		{"api-key": "devin-key", "base-url": "https://custom.devin.example.com"},
		{"api-key": "devin-key-default"},
		{"api-key": "   ", "base-url": "https://dropped.example.com"}
	]`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutDevinKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.DevinKey); got != 2 {
		t.Fatalf("devin keys len = %d, want 2", got)
	}
	if got := h.cfg.DevinKey[0].BaseURL; got != "https://custom.devin.example.com" {
		t.Fatalf("base-url = %q, want %q", got, "https://custom.devin.example.com")
	}
	if got := h.cfg.DevinKey[1].BaseURL; got != "https://server.codeium.com" {
		t.Fatalf("defaulted base-url = %q, want %q", got, "https://server.codeium.com")
	}
}

func TestPatchDevinKeyEmptyBaseURLResetsToDefault(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{DevinKey: []config.DevinKey{{
			APIKey:  "devin-key",
			BaseURL: "https://custom.devin.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/devin-api-key", strings.NewReader(`{
		"index": 0,
		"value": {"base-url": ""}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchDevinKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.DevinKey); got != 1 {
		t.Fatalf("devin keys len = %d, want 1", got)
	}
	if got := h.cfg.DevinKey[0].BaseURL; got != "https://server.codeium.com" {
		t.Fatalf("base-url = %q, want %q", got, "https://server.codeium.com")
	}
}

func TestGetDevinKeys(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{DevinKey: []config.DevinKey{{
			APIKey:  "devin-key",
			BaseURL: "https://server.codeium.com",
		}}},
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/devin-api-key", nil)

	h.GetDevinKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		DevinKeys []struct {
			APIKey  string `json:"api-key"`
			BaseURL string `json:"base-url"`
		} `json:"devin-api-key"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v; body=%s", errUnmarshal, rec.Body.String())
	}
	if len(body.DevinKeys) != 1 {
		t.Fatalf("devin-api-key len = %d, want 1; body=%s", len(body.DevinKeys), rec.Body.String())
	}
	if got := body.DevinKeys[0].APIKey; got != "devin-key" {
		t.Fatalf("api-key = %q, want %q", got, "devin-key")
	}
	if got := body.DevinKeys[0].BaseURL; got != "https://server.codeium.com" {
		t.Fatalf("base-url = %q, want %q", got, "https://server.codeium.com")
	}
}

func TestDeleteDevinKeyByIndex(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			DevinKey: []config.DevinKey{
				{APIKey: "key-a", BaseURL: "https://a.example.com"},
				{APIKey: "key-b", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/devin-api-key?index=0", nil)

	h.DeleteDevinKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.DevinKey); got != 1 {
		t.Fatalf("devin keys len = %d, want 1", got)
	}
	if got := h.cfg.DevinKey[0].APIKey; got != "key-b" {
		t.Fatalf("remaining api-key = %q, want %q", got, "key-b")
	}
}
