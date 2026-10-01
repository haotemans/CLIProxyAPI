package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// commandcode-api-key: []CommandcodeKey
// CommandcodeKey aliases ClaudeKey, so the JSON contract matches claude-api-key;
// claude-only knobs (cloak, fingerprint-profile, experimental-cch-signing) are
// intentionally not handled and are cleared by SanitizeCommandcodeKeys. Unlike
// mirasim, an empty base-url defaults to the public endpoint.
func (h *Handler) GetCommandcodeKeys(c *gin.Context) {
	c.JSON(200, gin.H{"commandcode-api-key": h.commandcodeKeysWithAuthIndex()})
}

func (h *Handler) PutCommandcodeKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.CommandcodeKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.CommandcodeKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.CommandcodeKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		normalizeCommandcodeKey(&entry)
		if entry.APIKey == "" {
			continue
		}
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("commandcode-api-key[%d].weight", i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.CommandcodeKey = filtered
	h.cfg.SanitizeCommandcodeKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchCommandcodeKey(c *gin.Context) {
	type commandcodeKeyPatch struct {
		APIKey                  *string                          `json:"api-key"`
		Priority                *int                             `json:"priority"`
		Weight                  json.RawMessage                  `json:"weight"`
		Prefix                  *string                          `json:"prefix"`
		BaseURL                 *string                          `json:"base-url"`
		ProxyURL                *string                          `json:"proxy-url"`
		Models                  *[]config.CommandcodeModel       `json:"models"`
		Headers                 *map[string]string               `json:"headers"`
		ExcludedModels          *[]string                        `json:"excluded-models"`
		RebuildMidSystemMessage *bool                            `json:"rebuild-mid-system-message"`
		DisableCooling          json.RawMessage                  `json:"disable-cooling"`
		RequestRetry            *int                             `json:"request-retry"`
		RequestScopedErrors     *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int                   `json:"index"`
		Match *string                `json:"match"`
		Value *commandcodeKeyPatch   `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.CommandcodeKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.CommandcodeKey {
			if h.cfg.CommandcodeKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.CommandcodeKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if len(body.Value.Weight) > 0 {
		weight, errWeight := parseCredentialWeightPatch(body.Value.Weight)
		if errWeight != nil {
			c.JSON(400, gin.H{"error": errWeight.Error()})
			return
		}
		entry.Weight = weight
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.BaseURL != nil {
		entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.CommandcodeModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.ExcludedModels != nil {
		entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
	}
	if body.Value.RebuildMidSystemMessage != nil {
		entry.RebuildMidSystemMessage = *body.Value.RebuildMidSystemMessage
	}
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeCommandcodeKey(&entry)
	h.cfg.CommandcodeKey[targetIndex] = entry
	h.cfg.SanitizeCommandcodeKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteCommandcodeKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.CommandcodeKey, 0, len(h.cfg.CommandcodeKey))
			for _, v := range h.cfg.CommandcodeKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.CommandcodeKey = out
			h.cfg.SanitizeCommandcodeKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.CommandcodeKey {
			if strings.TrimSpace(h.cfg.CommandcodeKey[i].APIKey) == val {
				matchCount++
				if matchIndex == -1 {
					matchIndex = i
				}
			}
		}
		if matchCount > 1 {
			c.JSON(400, gin.H{"error": "multiple items match api-key; base-url is required"})
			return
		}
		if matchIndex != -1 {
			h.cfg.CommandcodeKey = append(h.cfg.CommandcodeKey[:matchIndex], h.cfg.CommandcodeKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeCommandcodeKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.CommandcodeKey) {
			h.cfg.CommandcodeKey = append(h.cfg.CommandcodeKey[:idx], h.cfg.CommandcodeKey[idx+1:]...)
			h.cfg.SanitizeCommandcodeKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// normalizeCommandcodeKey trims and normalizes a commandcode entry. CommandcodeKey
// is an alias of ClaudeKey; the claude-only knobs (cloak, fingerprint-profile)
// do not apply and are cleared here.
func normalizeCommandcodeKey(entry *config.CommandcodeKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
	entry.Cloak = nil
	entry.FingerprintProfile = ""
	entry.ExperimentalCCHSigning = false
}
