package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Zero-priced dynamic free-tier ids (OpenCode Zen anonymous sync) surface a
// free flag on the auth-file models endpoint; ordinarily priced models don't.
func TestGetAuthFileModelsMarksZeroPricedModelsFree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { usagestats.RegisterZenFreeModelPrices(nil) })
	usagestats.RegisterZenFreeModelPrices([]string{"space-bunny-free"})

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	auth := &coreauth.Auth{
		ID:         "zen-free-m.json",
		FileName:   "zen-free-m.json",
		Provider:   "opencode-go",
		Attributes: map[string]string{"api_key": "public", "base_url": "https://opencode.ai/zen/v1"},
	}
	auth.EnsureIndex()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "opencode-go", []*registry.ModelInfo{
		{ID: "space-bunny-free"},
		{ID: "kimi-k3"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	router := gin.New()
	router.GET("/auth-files/models", h.GetAuthFileModels)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth-files/models?name=zen-free-m.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Models) != 2 {
		t.Fatalf("models = %+v", payload.Models)
	}
	byID := map[string]map[string]any{}
	for _, model := range payload.Models {
		byID[model["id"].(string)] = model
	}
	if byID["space-bunny-free"]["free"] != true {
		t.Fatalf("zero-priced free id must carry free=true: %+v", byID["space-bunny-free"])
	}
	if _, hasFree := byID["kimi-k3"]["free"]; hasFree {
		t.Fatalf("unsynced id must not carry the free flag: %+v", byID["kimi-k3"])
	}
}
