package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// opencode-go-api-key: []OpencodeGoKey
// OpencodeGoKey aliases ClaudeKey, so the JSON contract matches claude-api-key;
// claude-only knobs (cloak, fingerprint-profile, experimental-cch-signing) are
// intentionally not handled and are cleared by SanitizeOpencodeGoKeys. Unlike
// mirasim, an empty base-url defaults to the public endpoint.
func (h *Handler) GetOpencodeGoKeys(c *gin.Context) {
	c.JSON(200, gin.H{"opencode-go-api-key": h.opencodeGoKeysWithAuthIndex()})
}

func (h *Handler) PutOpencodeGoKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.OpencodeGoKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.OpencodeGoKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.OpencodeGoKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		normalizeOpencodeGoKey(&entry)
		if entry.APIKey == "" {
			continue
		}
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("opencode-go-api-key[%d].weight", i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OpencodeGoKey = filtered
	h.cfg.SanitizeOpencodeGoKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchOpencodeGoKey(c *gin.Context) {
	type opencodeGoKeyPatch struct {
		APIKey                  *string                          `json:"api-key"`
		Priority                *int                             `json:"priority"`
		Weight                  json.RawMessage                  `json:"weight"`
		Prefix                  *string                          `json:"prefix"`
		BaseURL                 *string                          `json:"base-url"`
		ProxyURL                *string                          `json:"proxy-url"`
		Models                  *[]config.OpencodeGoModel        `json:"models"`
		Headers                 *map[string]string               `json:"headers"`
		ExcludedModels          *[]string                        `json:"excluded-models"`
		RebuildMidSystemMessage *bool                            `json:"rebuild-mid-system-message"`
		DisableCooling          json.RawMessage                  `json:"disable-cooling"`
		RequestRetry            *int                             `json:"request-retry"`
		RequestScopedErrors     *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int                 `json:"index"`
		Match *string              `json:"match"`
		Value *opencodeGoKeyPatch  `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpencodeGoKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.OpencodeGoKey {
			if h.cfg.OpencodeGoKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.OpencodeGoKey[targetIndex]
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
		entry.Models = append([]config.OpencodeGoModel(nil), (*body.Value.Models)...)
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
	normalizeOpencodeGoKey(&entry)
	h.cfg.OpencodeGoKey[targetIndex] = entry
	h.cfg.SanitizeOpencodeGoKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteOpencodeGoKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.OpencodeGoKey, 0, len(h.cfg.OpencodeGoKey))
			for _, v := range h.cfg.OpencodeGoKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.OpencodeGoKey = out
			h.cfg.SanitizeOpencodeGoKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.OpencodeGoKey {
			if strings.TrimSpace(h.cfg.OpencodeGoKey[i].APIKey) == val {
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
			h.cfg.OpencodeGoKey = append(h.cfg.OpencodeGoKey[:matchIndex], h.cfg.OpencodeGoKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeOpencodeGoKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.OpencodeGoKey) {
			h.cfg.OpencodeGoKey = append(h.cfg.OpencodeGoKey[:idx], h.cfg.OpencodeGoKey[idx+1:]...)
			h.cfg.SanitizeOpencodeGoKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// normalizeOpencodeGoKey trims and normalizes an opencode-go entry. OpencodeGoKey
// is an alias of ClaudeKey; the claude-only knobs (cloak, fingerprint-profile)
// do not apply and are cleared here.
func normalizeOpencodeGoKey(entry *config.OpencodeGoKey) {
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
