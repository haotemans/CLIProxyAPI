package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// cline-api-key: []ClineKey
// ClineKey aliases ClaudeKey, so the JSON contract matches claude-api-key;
// claude-only knobs (cloak, fingerprint-profile, experimental-cch-signing) are
// intentionally not handled and are cleared by SanitizeClineKeys. Unlike
// mirasim, an empty base-url defaults to the public account API.
func (h *Handler) GetClineKeys(c *gin.Context) {
	c.JSON(200, gin.H{"cline-api-key": h.clineKeysWithAuthIndex()})
}

func (h *Handler) PutClineKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.ClineKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.ClineKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.ClineKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		normalizeClineKey(&entry)
		if entry.APIKey == "" {
			// Chat completions require the account API key; an empty one would
			// only produce 401s.
			continue
		}
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("cline-api-key[%d].weight", i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ClineKey = filtered
	h.cfg.SanitizeClineKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchClineKey(c *gin.Context) {
	type clineKeyPatch struct {
		APIKey                  *string                          `json:"api-key"`
		Priority                *int                             `json:"priority"`
		Weight                  json.RawMessage                  `json:"weight"`
		Prefix                  *string                          `json:"prefix"`
		BaseURL                 *string                          `json:"base-url"`
		ProxyURL                *string                          `json:"proxy-url"`
		Models                  *[]config.ClineModel             `json:"models"`
		Headers                 *map[string]string               `json:"headers"`
		ExcludedModels          *[]string                        `json:"excluded-models"`
		RebuildMidSystemMessage *bool                            `json:"rebuild-mid-system-message"`
		DisableCooling          json.RawMessage                  `json:"disable-cooling"`
		RequestRetry            *int                             `json:"request-retry"`
		RequestScopedErrors     *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int           `json:"index"`
		Match *string        `json:"match"`
		Value *clineKeyPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.ClineKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.ClineKey {
			if h.cfg.ClineKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.ClineKey[targetIndex]
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
		// Empty resets the entry to the public account API instead of dropping
		// it (the sanitizer fills the default).
		entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.ClineModel(nil), (*body.Value.Models)...)
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
	normalizeClineKey(&entry)
	h.cfg.ClineKey[targetIndex] = entry
	h.cfg.SanitizeClineKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteClineKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.ClineKey, 0, len(h.cfg.ClineKey))
			for _, v := range h.cfg.ClineKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.ClineKey = out
			h.cfg.SanitizeClineKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.ClineKey {
			if strings.TrimSpace(h.cfg.ClineKey[i].APIKey) == val {
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
			h.cfg.ClineKey = append(h.cfg.ClineKey[:matchIndex], h.cfg.ClineKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeClineKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.ClineKey) {
			h.cfg.ClineKey = append(h.cfg.ClineKey[:idx], h.cfg.ClineKey[idx+1:]...)
			h.cfg.SanitizeClineKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// normalizeClineKey trims and normalizes a cline entry. ClineKey is an alias
// of ClaudeKey; the claude-only knobs (cloak, fingerprint-profile) do not
// apply and are cleared here.
func normalizeClineKey(entry *config.ClineKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
	entry.Cloak = nil
	entry.FingerprintProfile = ""
	entry.ExperimentalCCHSigning = false
	if len(entry.Models) == 0 {
		return
	}
	normalized := make([]config.ClineModel, 0, len(entry.Models))
	for i := range entry.Models {
		model := entry.Models[i]
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" && model.Alias == "" {
			continue
		}
		normalized = append(normalized, model)
	}
	entry.Models = normalized
}
