package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type pruneStatusErr struct{ code int }

func (e pruneStatusErr) Error() string   { return fmt.Sprintf("upstream status %d", e.code) }
func (e pruneStatusErr) StatusCode() int { return e.code }

// seedPrunableAuth registers a cursor auth with a backing file (so the probe
// store can persist) and a registry catalog, then wires a mock engine whose
// per-model handlers produce the aggressive matrix.
func seedPrunableAuth(t *testing.T, h *Handler, manager *coreauth.Manager, name string, handlers map[string]error, registryIDs ...string) *coreauth.Auth {
	t.Helper()
	if errWrite := os.WriteFile(filepath.Join(h.cfg.AuthDir, name), []byte(`{"type":"cursor","access_token":"at"}`), 0o600); errWrite != nil {
		t.Fatalf("seed auth file: %v", errWrite)
	}
	mock := &modelProbeHTTPMockExec{handlers: handlers}
	engine := modelprobe.NewEngine(nil, modelprobe.Options{
		MaxParallel:     1,
		DriverOverrides: map[string]modelprobe.RequestExecutor{"cursor": mock},
	})
	h.SetModelProbeEngineOverride(engine)

	auth := &coreauth.Auth{
		ID:       name,
		FileName: name,
		Provider: "cursor",
		Metadata: map[string]any{"access_token": "at"},
	}
	auth.EnsureIndex()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	infos := make([]*registry.ModelInfo, 0, len(registryIDs))
	for _, id := range registryIDs {
		infos = append(infos, &registry.ModelInfo{ID: id})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "cursor", infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return auth
}

func postRunPayload(t *testing.T, router *gin.Engine, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/model-probe/run", bytes.NewReader([]byte(body))))
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("status=%d unmarshal: %v body=%s", rec.Code, err, rec.Body.String())
	}
	return rec.Code, payload
}

func registeredIDsFiltered(t *testing.T, auth *coreauth.Auth) []string {
	t.Helper()
	models, _ := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
	filtered := modelprobe.FilterPrunedForAuth(auth.Metadata, models)
	out := make([]string, 0, len(filtered))
	for _, model := range filtered {
		out = append(out, model.ID)
	}
	sort.Strings(out)
	return out
}

func pruneRunRemoved(t *testing.T, payload map[string]any) []string {
	t.Helper()
	raw, ok := payload["prune_run"].(map[string]any)
	if !ok {
		t.Fatalf("prune_run missing in payload: %+v", payload)
	}
	if raw["at"] == nil || raw["at"] == "" {
		t.Fatalf("prune_run.at missing: %+v", raw)
	}
	items, _ := raw["removed"].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, okS := item.(string); okS {
			out = append(out, s)
		}
	}
	return out
}

func TestModelProbeRunPruneUnusedRemovesNonUsable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, manager := newHandlerForModelProbe(t)
	auth := seedPrunableAuth(t, h, manager, "prune-a.json", map[string]error{
		"gpt-4o": pruneStatusErr{404},
		"busy-1": pruneStatusErr{429},
		"down-1": pruneStatusErr{503},
		"nope-1": pruneStatusErr{401},
		"good-1": nil,
		"good-2": nil,
	},
		// Duplicate id proves the aggressive removed list dedupes.
		"good-1", "good-2", "gpt-4o", "busy-1", "down-1", "nope-1", "gpt-4o")

	router := gin.New()
	router.POST("/model-probe/run", h.PostModelProbeRun)

	body := `{"auth_index":"` + auth.Index + `","prune_unused":true}`
	code, payload := postRunPayload(t, router, body)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %+v", code, payload)
	}
	if got := pruneRunRemoved(t, payload); fmt.Sprint(got) != "[gpt-4o]" {
		t.Fatalf("removed = %v, want only the not_available id (busy/limited/unreachable/auth_error stay)", got)
	}
	// Conservative summary stays: only the not_available id counts as pruned.
	if summary, ok := payload["summary"].(map[string]any); !ok || summary["pruned"] != 1.0 || summary["usable"] != 2.0 {
		t.Fatalf("summary = %+v", payload["summary"])
	}

	// Marker persisted in memory (the registered auth snapshot) and in the
	// auth file.
	latest, ok := manager.GetByID(auth.ID)
	if !ok || latest == nil {
		t.Fatal("registered auth missing after run")
	}
	section := modelprobe.ReadSection(latest.Metadata)
	if section == nil || section.PruneRun == nil {
		t.Fatal("prune_run marker missing in credential metadata")
	}
	raw, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, "prune-a.json"))
	if errRead != nil {
		t.Fatalf("read persisted auth: %v", errRead)
	}
	var persisted map[string]any
	if errJSON := json.Unmarshal(raw, &persisted); errJSON != nil {
		t.Fatalf("persisted auth invalid JSON: %v", errJSON)
	}
	persistedSection := modelprobe.ReadSection(persisted)
	if persistedSection == nil || persistedSection.PruneRun == nil || len(persistedSection.PruneRun.Removed) != 1 {
		t.Fatalf("persisted section = %+v", persistedSection)
	}

	// Registry-facing effect: only the usable ids survive the catalog filter
	// (the same filter registerModelsForAuth applies on the Update path).
	if got := registeredIDsFiltered(t, latest); fmt.Sprint(got) != "[busy-1 down-1 good-1 good-2 nope-1]" {
		t.Fatalf("filtered catalog = %v (only not_available pruned)", got)
	}
	// The credential is never auto-disabled by an aggressive run.
	if latest.Disabled {
		t.Fatal("credential must stay enabled after aggressive prune")
	}

	// A conservative re-run without the flag keeps the audit marker but adds
	// nothing new: a previously removed model re-probed usable would re-enter.
	mockOK := &modelProbeHTTPMockExec{handlers: map[string]error{}}
	engine := modelprobe.NewEngine(nil, modelprobe.Options{
		MaxParallel:     1,
		DriverOverrides: map[string]modelprobe.RequestExecutor{"cursor": mockOK},
	})
	h.SetModelProbeEngineOverride(engine)
	code, payload = postRunPayload(t, router, `{"auth_index":"`+auth.Index+`"}`)
	if code != http.StatusOK {
		t.Fatalf("default run status = %d: %+v", code, payload)
	}
	if _, has := payload["prune_run"]; has {
		t.Fatalf("default run must not set prune_run in the fresh response: %+v", payload["prune_run"])
	}
	mergedAuth, _ := manager.GetByID(auth.ID)
	merged := modelprobe.ReadSection(mergedAuth.Metadata)
	if merged == nil || merged.PruneRun == nil || len(merged.PruneRun.Removed) != 1 {
		t.Fatalf("scheduled merge erased the audit marker: %+v", merged)
	}
}

func TestModelProbeRunPruneUnusedEverythingUnusable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, manager := newHandlerForModelProbe(t)
	auth := seedPrunableAuth(t, h, manager, "prune-all.json", map[string]error{
		"m-1": pruneStatusErr{404},
		"m-2": pruneStatusErr{503},
		"m-3": pruneStatusErr{401},
	}, "m-1", "m-2", "m-3")

	router := gin.New()
	router.POST("/model-probe/run", h.PostModelProbeRun)

	code, payload := postRunPayload(t, router, `{"auth_index":"`+auth.Index+`","prune_unused":true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %+v", code, payload)
	}
	if got := pruneRunRemoved(t, payload); fmt.Sprint(got) != "[m-1]" {
		t.Fatalf("removed = %v, want only not_available pruned; limited/unreachable/auth_error stay", got)
	}
	if summary, ok := payload["summary"].(map[string]any); !ok || summary["usable"] != 0.0 {
		t.Fatalf("summary = %+v", payload["summary"])
	}
	latest, ok := manager.GetByID(auth.ID)
	if !ok || latest == nil {
		t.Fatal("registered auth missing after run")
	}
	if got := registeredIDsFiltered(t, latest); fmt.Sprint(got) != "[m-2 m-3]" {
		t.Fatalf("not_available-only prune leaves limited/unreachable/auth_error models: %v", got)
	}
	if latest.Disabled {
		t.Fatal("fully pruned credential must NOT be auto-disabled (self-heal via scheduled cycles)")
	}
}
