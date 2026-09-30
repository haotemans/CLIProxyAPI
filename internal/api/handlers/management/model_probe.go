package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const modelProbeInlineTimeout = 90 * time.Second

// modelProbeEngine lazily built by the handler for manual probe triggers.
func (h *Handler) modelProbeEngine() *modelprobe.Engine {
	if h == nil {
		return nil
	}
	if h.modelProbeEngineOverride != nil {
		return h.modelProbeEngineOverride
	}
	if h.cfg == nil {
		return nil
	}
	maxParallel := h.cfg.ModelProbe.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return modelprobe.NewEngine(h.cfg, modelprobe.Options{
		MaxModelsPerCredentialPerCycle: h.cfg.ModelProbe.MaxModelsPerCredentialPerCycle,
		MaxParallel:                    maxParallel,
		ProbeTimeoutPerModel:           modelProbeInlineTimeout,
	})
}

// SetModelProbeEngineOverride installs a prebuilt engine (used by tests to
// keep outbound probes deterministic with fake executors).
func (h *Handler) SetModelProbeEngineOverride(engine *modelprobe.Engine) {
	h.modelProbeEngineOverride = engine
}

// GetModelProbeStatus handles GET /model-probe/status.
// Shape: {enabled, interval_seconds, max_parallel, supported_drivers,
// credentials: [{auth_file, auth_index, provider, label, disabled, driver,
// probed, checked_at?, usable?, pruned?, pruned_models?}]}.
func (h *Handler) GetModelProbeStatus(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager unavailable"})
		return
	}
	engine := h.modelProbeEngine()
	rows := make([]map[string]any, 0)
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		row := map[string]any{
			"auth_file":  auth.FileName,
			"auth_index": auth.Index,
			"provider":   provider,
			"label":      auth.Label,
			"disabled":   auth.Disabled,
			"driver":     engine != nil && engine.HasDriver(provider),
		}
		section := modelprobe.ReadSection(auth.Metadata)
		if section != nil {
			row["probed"] = true
			row["checked_at"] = section.CheckedAt
			row["usable"] = len(section.Usable)
			row["pruned"] = len(section.Pruned)
			if len(section.Pruned) > 0 {
				row["pruned_models"] = section.Pruned
			}
		} else {
			row["probed"] = false
		}
		rows = append(rows, row)
	}
	interval := 6 * 3600
	maxParallel := 4
	if h.cfg != nil {
		if s := h.cfg.ModelProbe.Interval; s > 0 {
			interval = s
		}
		if s := h.cfg.ModelProbe.MaxParallel; s > 0 {
			maxParallel = s
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":           h.cfg != nil && h.cfg.ModelProbe.Enabled,
		"interval_seconds":  interval,
		"max_parallel":      maxParallel,
		"supported_drivers": modelprobe.SupportedProviders(),
		"credentials":       rows,
	})
}

type modelProbeRunRequest struct {
	AuthIndex string `json:"auth_index"`
}

// PostModelProbeRun handles POST /model-probe/run. Inline probe for one
// credential (auth_index JSON or query); without it, returns 501 — full
// cycles belong to the scheduler (enable model-probe.enabled).
func (h *Handler) PostModelProbeRun(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager unavailable"})
		return
	}
	var body modelProbeRunRequest
	if err := c.ShouldBindJSON(&body); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("auth_index"))
	}
	if authIndex == "" {
		c.JSON(http.StatusNotImplemented, gin.H{
			"error": "no target: pass auth_index (or ?auth_index=) to probe one credential inline; full cycles run on the scheduler when model-probe.enabled is set",
		})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		if byID, ok := h.authManager.GetByID(authIndex); ok {
			auth = byID
		}
	}
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	section, errProbe := h.inlineModelProbe(c.Request.Context(), auth)
	if errProbe != nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": errProbe.Error()})
		return
	}
	details := modelprobe.DetailRows(section)
	if details == nil {
		details = []map[string]any{}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"summary": modelprobe.SummaryForAuth(auth),
		"models":  details,
	})
}

// inlineModelProbe probes one credential synchronously with the native engine.
func (h *Handler) inlineModelProbe(ctx context.Context, auth *cliproxyauth.Auth) (*modelprobe.Section, error) {
	engine := h.modelProbeEngine()
	if engine == nil {
		return nil, fmt.Errorf("model-probe engine unavailable")
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if !engine.HasDriver(provider) {
		return nil, fmt.Errorf("no model-probe driver for provider %s", provider)
	}
	models, _ := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		ids = append(ids, model.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("credential has no registered models to probe")
	}
	section := engine.CredentialCycle(ctx, auth, provider, ids)
	if section == nil {
		return nil, fmt.Errorf("probe cycle skipped")
	}
	store := &modelprobe.Store{AuthDir: h.cfg.AuthDir}
	merged := store.ApplyOutcome(auth, section)
	// Reflect the probe result into the live registered auth: the returned
	// auth is a clone, and management status reads from the auth manager.
	if h.authManager != nil {
		if _, errUpdate := h.authManager.Update(ctx, auth); errUpdate != nil {
			// Inline view stays correct; the file write already re-registers
			// through the watcher pipeline on next load.
			_ = errUpdate
		}
	}
	return merged, nil
}
