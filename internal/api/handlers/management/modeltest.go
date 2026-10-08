package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// PostModelTest handles POST /model-test: a single-model on-demand probe for
// one credential. It reuses (*modelprobe.Engine).ProbeOne, which routes
// through the provider's own executor — so provider behavior (e.g. the
// opencode-go free-tier fingerprint headers injected by its executor) matches
// live traffic exactly.
//
// The probe outcome is a business result, never an HTTP error: a 403/timeout
// from the upstream surfaces as status+error with HTTP 200. Only handler-level
// failures (missing auth, bad request, unavailable manager/engine) map to
// non-200 status codes.
func (h *Handler) PostModelTest(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager unavailable"})
		return
	}
	var body struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("auth_index"))
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required (JSON body or ?auth_index=)"})
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

	engine := h.modelProbeEngine()
	if engine == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "model-probe engine unavailable"})
		return
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	start := time.Now()
	outcome := engine.ProbeOne(c.Request.Context(), auth, provider, model)
	latencyMS := time.Since(start).Milliseconds()

	c.JSON(http.StatusOK, gin.H{
		"status":     string(outcome.Status),
		"error":      outcome.Error,
		"model":      model,
		"provider":   provider,
		"latency_ms": latencyMS,
		"checked_ms": outcome.CheckedMS,
	})
}
