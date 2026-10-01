package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// testModelSemaphore bounds concurrent live test-model executions.
var testModelSemaphore = make(chan struct{}, 2)

type authFileTestModelRequest struct {
	AuthIndex string `json:"auth_index"`
	AuthFile  string `json:"auth_file"`
	Model     string `json:"model"`
}

// testModelExecutorFor builds the production executor for probe-capable
// providers (mirrors internal/modelprobe's executorFactories).
func testModelExecutorFor(provider string, cfg *config.Config) (modelprobe.RequestExecutor, bool) {
	switch provider {
	case "cline":
		return executor.NewClineExecutor(cfg), true
	case "cursor":
		return executor.NewCursorExecutor(cfg), true
	case "kiro":
		return executor.NewKiroExecutor(cfg), true
	case "claude":
		return executor.NewClaudeExecutor(cfg), true
	case "codex":
		return executor.NewCodexExecutor(cfg), true
	case "xai":
		return executor.NewXAIExecutor(cfg), true
	case "devin":
		return executor.NewDevinExecutor(cfg), true
	case "meta":
		return executor.NewMetaExecutor(cfg), true
	case "mirasim":
		return executor.NewMirasimExecutor(cfg), true
	case "commandcode":
		return executor.NewOpenAICompatExecutor("commandcode", cfg), true
	case "opencode-go":
		return executor.NewOpenAICompatExecutor("opencode-go", cfg), true
	}
	return nil, false
}

// PostAuthFileTestModel handles POST /auth-files/test-model. It executes one
// real minimal user request for (auth, model) through the production
// executor and reports the reply. Outcome failures still answer 200 with
// ok=false and the probe status vocabulary; 4xx/5xx mark request-level
// problems. No probe, disable, or refresh state is mutated.
func (h *Handler) PostAuthFileTestModel(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager unavailable"})
		return
	}
	var body authFileTestModelRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	auth := h.resolveTestModelAuth(body.AuthIndex, body.AuthFile)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	exec, ok := testModelExecutorFor(provider, h.cfg)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "no test-model driver for provider " + provider})
		return
	}
	available := effectiveModelIDs(auth)
	if len(available) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential has no registered models to test"})
		return
	}
	canonical, okMatch := matchModelID(model, available)
	if !okMatch {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  fmt.Sprintf("model %q is not in the credential's effective model set", model),
			"models": available,
		})
		return
	}

	select {
	case testModelSemaphore <- struct{}{}:
		defer func() { <-testModelSemaphore }()
	case <-c.Request.Context().Done():
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "test-model is busy; retry shortly"})
		return
	}

	started := time.Now()
	reply, usage, errExecute := executeTestModelRequest(c.Request.Context(), exec, auth, canonical)
	resp := gin.H{
		"ok":         false,
		"model":      canonical,
		"latency_ms": time.Since(started).Milliseconds(),
	}
	if errExecute != nil {
		resp["status"] = string(modelprobe.ClassifyError(errExecute))
		resp["error"] = summarizeTestModelError(errExecute)
		c.JSON(http.StatusOK, resp)
		return
	}
	resp["ok"] = true
	resp["status"] = string(modelprobe.StatusUsable)
	resp["reply_text"] = reply
	resp["usage"] = usage
	c.JSON(http.StatusOK, resp)
}

// resolveTestModelAuth finds the credential by auth_index (index hit, then ID
// fallback as in model-probe/run) or by auth_file name.
func (h *Handler) resolveTestModelAuth(authIndex, authFile string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex != "" {
		if auth := h.authByIndex(authIndex); auth != nil {
			return auth
		}
		if byID, ok := h.authManager.GetByID(authIndex); ok {
			return byID
		}
	}
	authFile = strings.TrimSpace(authFile)
	if authFile != "" {
		for _, auth := range h.authManager.List() {
			if auth != nil && (auth.FileName == authFile || auth.ID == authFile) {
				return auth
			}
		}
	}
	return nil
}

// effectiveModelIDs returns the credential's current effective model set:
// the registry registration, falling back to the last probe snapshot during
// provider block phases (same as inlineModelProbe's candidate recovery).
func effectiveModelIDs(auth *coreauth.Auth) []string {
	models, _ := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		ids = append(ids, model.ID)
	}
	if len(ids) == 0 {
		if section := modelprobe.ReadSection(auth.Metadata); section != nil {
			for id := range section.PerModel {
				ids = append(ids, id)
			}
			sort.Strings(ids)
		}
	}
	return ids
}

// matchModelID resolves the requested id against the effective set; a
// case-insensitive hit returns the canonical registered id.
func matchModelID(requested string, available []string) (string, bool) {
	for _, id := range available {
		if id == requested {
			return id, true
		}
	}
	lower := strings.ToLower(requested)
	for _, id := range available {
		if strings.ToLower(id) == lower {
			return id, true
		}
	}
	return "", false
}

// executeTestModelRequest sends the single user-style chat request and reads
// the full non-streamed body (openai response format, the executor
// translates from provider-native shapes).
func executeTestModelRequest(ctx context.Context, exec modelprobe.RequestExecutor, auth *coreauth.Auth, model string) (string, gin.H, error) {
	probeCtx, cancel := context.WithTimeout(ctx, modelProbeInlineTimeout)
	defer cancel()
	payload, errMarshal := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "say ok in five words"}},
		"max_tokens": 32,
		"stream":     false,
	})
	if errMarshal != nil {
		return "", nil, errMarshal
	}
	req := cliproxyexecutor.Request{Model: model, Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	resp, err := exec.Execute(probeCtx, auth, req, opts)
	if err != nil {
		return "", nil, err
	}
	return extractReplyText(resp.Payload), extractUsage(resp.Payload), nil
}

// extractReplyText pulls the assistant text out of an openai chat completion
// (string or content-part array), with a Claude-native fallback shape.
func extractReplyText(payload []byte) string {
	content := gjson.GetBytes(payload, "choices.0.message.content")
	text := ""
	switch {
	case content.Type == gjson.String:
		text = content.String()
	case content.IsArray():
		parts := make([]string, 0, len(content.Array()))
		for _, part := range content.Array() {
			if s := part.Get("text").String(); s != "" {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, "\n")
	default:
		text = gjson.GetBytes(payload, "content.0.text").String()
	}
	text = strings.TrimSpace(text)
	const cap = 500
	if len(text) > cap {
		text = text[:cap] + "..."
	}
	return text
}

// extractUsage reads token counters from the openai usage block, accepting
// the input/output naming variant some translators emit.
func extractUsage(payload []byte) gin.H {
	usage := gjson.GetBytes(payload, "usage")
	input := usage.Get("prompt_tokens").Int()
	if input == 0 {
		input = usage.Get("input_tokens").Int()
	}
	output := usage.Get("completion_tokens").Int()
	if output == 0 {
		output = usage.Get("output_tokens").Int()
	}
	return gin.H{"input": input, "output": output}
}

func summarizeTestModelError(err error) string {
	msg := strings.TrimSpace(err.Error())
	const cap = 400
	if len(msg) > cap {
		msg = msg[:cap] + "..."
	}
	return msg
}
