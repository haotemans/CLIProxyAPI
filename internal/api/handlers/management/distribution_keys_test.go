package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/distribution"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

func distributionTestHandler(t *testing.T, entries []config.DistributionKey) *Handler {
	t.Helper()
	return &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys:          []string{"sk-master"},
				DistributionKeys: entries,
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
}

func distributionDo(t *testing.T, h *Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	switch {
	case method == http.MethodGet && strings.HasPrefix(target, "/v0/management/distribution-keys/usage"):
		h.GetDistributionKeysUsage(ctx)
	case method == http.MethodPost && strings.HasPrefix(target, "/v0/management/distribution-keys/reset-usage"):
		h.PostDistributionKeyResetUsage(ctx)
	case method == http.MethodGet:
		h.GetDistributionKeys(ctx)
	case method == http.MethodPost:
		h.PostDistributionKey(ctx)
	case method == http.MethodPut:
		h.PutDistributionKey(ctx)
	case method == http.MethodDelete:
		h.DeleteDistributionKey(ctx)
	}
	var payload map[string]any
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("status=%d invalid JSON: %v body=%s", rec.Code, err, rec.Body.String())
		}
	}
	return rec, payload
}

func TestDistributionKeysCRUDLifecycle(t *testing.T) {
	t.Cleanup(func() { distribution.SetGlobal(nil) })
	store, err := distribution.NewUsageStore(t.TempDir()+"/usage.json", nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	distribution.SetGlobal(store)

	h := distributionTestHandler(t, nil)

	// Create without a key value: the server mints a dk- key and returns it
	// exactly once.
	rec, payload := distributionDo(t, h, http.MethodPost, "/v0/management/distribution-keys", `{"name":"alice","enabled":true,"expires_at":"2027-01-01T00:00:00Z","allowed_models":["gpt-5"],"quota_usd":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %+v", rec.Code, payload)
	}
	fullKey, _ := payload["key"].(string)
	if !strings.HasPrefix(fullKey, "dk-") || len(fullKey) != 35 {
		t.Fatalf("minted key shape: %q len=%d", fullKey, len(fullKey))
	}
	entryOut, _ := payload["distribution-key"].(map[string]any)
	if entryOut["key_masked"] == fullKey || !strings.HasSuffix(entryOut["key_masked"].(string), fullKey[len(fullKey)-4:]) {
		t.Fatalf("masked output must hide the middle: %+v", entryOut)
	}
	hash := usagestats.MaskKeyLabel(fullKey)

	// Config persisted with the full value (file stays local, logs never).
	raw, errRead := os.ReadFile(h.configFilePath)
	if errRead != nil {
		t.Fatalf("read persisted config: %v", errRead)
	}
	if !strings.Contains(string(raw), fullKey) {
		t.Fatalf("persisted config must carry the minted key: %s", raw)
	}

	// Duplicate and master collisions are rejected.
	rec, _ = distributionDo(t, h, http.MethodPost, "/v0/management/distribution-keys", `{"key":"`+fullKey+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", rec.Code)
	}
	rec, _ = distributionDo(t, h, http.MethodPost, "/v0/management/distribution-keys", `{"key":"sk-master"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("master collision = %d, want 409", rec.Code)
	}

	// Seed spend, then list: the ledger row merges into the masked output.
	store.Add(hash, 2.5, 1000, 250)
	rec, payload = distributionDo(t, h, http.MethodGet, "/v0/management/distribution-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	rows, _ := payload["distribution-keys"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	row0, _ := rows[0].(map[string]any)
	if row0["key_hash"] != hash || row0["spent_usd"] != 2.5 || row0["requests"] != 1.0 || row0["quota_usd"] != 5.0 {
		t.Fatalf("row = %+v", row0)
	}

	// Update: disable and lift the whitelist; the key value stays.
	rec, payload = distributionDo(t, h, http.MethodPut, "/v0/management/distribution-keys?key-hash="+hash, `{"enabled":false,"allowed_models":["gpt-5","claude-sonnet-4-5"],"quota_usd":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %+v", rec.Code, payload)
	}
	entryOut, _ = payload["distribution-key"].(map[string]any)
	if entryOut["enabled"] != false {
		t.Fatalf("update enabled = %+v", entryOut)
	}
	if got := h.cfg.DistributionKeys[0].AllowedModels; len(got) != 2 || got[1] != "claude-sonnet-4-5" {
		t.Fatalf("updated whitelist = %v", got)
	}

	// Usage summary: cumulative + per-key rows in meters style.
	rec, payload = distributionDo(t, h, http.MethodGet, "/v0/management/distribution-keys/usage", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d", rec.Code)
	}
	usageRows, _ := payload["rows"].([]any)
	if len(usageRows) != 1 || usageRows[0].(map[string]any)["spent_usd"] != 2.5 {
		t.Fatalf("usage rows = %+v", usageRows)
	}

	// Reset then delete.
	rec, payload = distributionDo(t, h, http.MethodPost, "/v0/management/distribution-keys/reset-usage", `{"key_hash":"`+hash+`"}`)
	if rec.Code != http.StatusOK || store.Spent(hash) != 0 {
		t.Fatalf("reset = %d spent=%v", rec.Code, store.Spent(hash))
	}
	rec, _ = distributionDo(t, h, http.MethodDelete, "/v0/management/distribution-keys?key-hash="+hash, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if len(h.cfg.DistributionKeys) != 0 {
		t.Fatalf("entries after delete = %+v", h.cfg.DistributionKeys)
	}
}
