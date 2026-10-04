package management

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/distribution"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

// distributionKeyMasked renders a key for management output: "dk-ab12…wxyz"
// (first 6 + last 4). Full values only cross the wire once, at creation.
func distributionKeyMasked(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 10 {
		return key
	}
	return key[:6] + "…" + key[len(key)-4:]
}

type distributionKeyOut struct {
	KeyHash       string   `json:"key_hash"`
	KeyMasked     string   `json:"key_masked"`
	Name          string   `json:"name,omitempty"`
	Enabled       bool     `json:"enabled"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Expired       bool     `json:"expired,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	QuotaUSD      float64  `json:"quota_usd,omitempty"`
	SpentUSD      float64  `json:"spent_usd"`
	Requests      int64    `json:"requests"`
	LastRequestAt string   `json:"last_request_at,omitempty"`
}

func (h *Handler) distributionKeyEntryOut(entry config.DistributionKey, usage map[string]distribution.KeyUsage, now time.Time) distributionKeyOut {
	hash := usagestats.MaskKeyLabel(entry.Key)
	out := distributionKeyOut{
		KeyHash:       hash,
		KeyMasked:     distributionKeyMasked(entry.Key),
		Name:          entry.Name,
		Enabled:       entry.Enabled == nil || *entry.Enabled,
		ExpiresAt:     entry.ExpiresAt,
		AllowedModels: entry.AllowedModels,
		QuotaUSD:      entry.QuotaUSD,
	}
	if expires := strings.TrimSpace(entry.ExpiresAt); expires != "" {
		if at, errParse := time.Parse(time.RFC3339, expires); errParse == nil && !now.Before(at) {
			out.Expired = true
		}
	}
	if row, ok := usage[hash]; ok {
		out.SpentUSD = row.SpentUSD
		out.Requests = row.Requests
		out.LastRequestAt = row.LastRequestAt
	}
	return out
}

func (h *Handler) distributionUsageSnapshot() map[string]distribution.KeyUsage {
	if store := distribution.Global(); store != nil {
		return store.Snapshot()
	}
	return map[string]distribution.KeyUsage{}
}

// GetDistributionKeys handles GET /distribution-keys: every issued key masked,
// with lifecycle and cumulative spend.
func (h *Handler) GetDistributionKeys(c *gin.Context) {
	h.mu.Lock()
	entries := append([]config.DistributionKey(nil), h.cfg.DistributionKeys...)
	h.mu.Unlock()
	usage := h.distributionUsageSnapshot()
	out := make([]distributionKeyOut, 0, len(entries))
	for _, entry := range entries {
		out = append(out, h.distributionKeyEntryOut(entry, usage, time.Now()))
	}
	c.JSON(http.StatusOK, gin.H{"distribution-keys": out})
}

type distributionKeyWrite struct {
	Key           string   `json:"key"`
	Name          *string  `json:"name"`
	Enabled       *bool    `json:"enabled"`
	ExpiresAt     *string  `json:"expires_at"`
	AllowedModels []string `json:"allowed_models"`
	QuotaUSD      *float64 `json:"quota_usd"`
}

// PostDistributionKey handles POST /distribution-keys. The key mints a
// dk-prefixed random value when the body omits one; the full value is
// returned exactly once here.
func (h *Handler) PostDistributionKey(c *gin.Context) {
	var body distributionKeyWrite
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		key = mintDistributionKey()
	}
	entry := config.DistributionKey{Key: key}
	if body.Name != nil {
		entry.Name = strings.TrimSpace(*body.Name)
	}
	if body.Enabled != nil {
		entry.Enabled = body.Enabled
	}
	if body.ExpiresAt != nil {
		expires := strings.TrimSpace(*body.ExpiresAt)
		if expires != "" {
			if _, err := time.Parse(time.RFC3339, expires); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339 (or empty for never)"})
				return
			}
		}
		entry.ExpiresAt = expires
	}
	entry.AllowedModels = body.AllowedModels
	if body.QuotaUSD != nil {
		if *body.QuotaUSD < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "quota_usd must be >= 0 (0 or omitted = unlimited)"})
			return
		}
		entry.QuotaUSD = *body.QuotaUSD
	}

	h.mu.Lock()
	for _, existing := range h.cfg.DistributionKeys {
		if existing.Key == key {
			h.mu.Unlock()
			c.JSON(http.StatusConflict, gin.H{"error": "a distribution key with this value already exists"})
			return
		}
	}
	for _, master := range h.cfg.APIKeys {
		if strings.TrimSpace(master) == key {
			h.mu.Unlock()
			c.JSON(http.StatusConflict, gin.H{"error": "this key value is already a master key in api-keys"})
			return
		}
	}
	h.cfg.DistributionKeys = append(h.cfg.DistributionKeys, entry)
	h.cfg.SanitizeDistributionKeys()
	// Save under the lock, then fire the reload and answer with the one-time
	// full key value (persist() alone would only return {"status":"ok"}).
	if !h.saveConfigLocked(c) {
		h.mu.Unlock()
		return
	}
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.scheduleReloadAfterSave(c, snapshot)
	usage := h.distributionUsageSnapshot()
	c.JSON(http.StatusCreated, gin.H{
		"distribution-key": h.distributionKeyEntryOut(entry, usage, time.Now()),
		"key":              key,
		"note":             "the full key value is returned only once — store it safely",
	})
}

// PutDistributionKey handles PUT /distribution-keys?key-hash=<hash>: updates
// the mutable fields of one entry (never the key value itself).
func (h *Handler) PutDistributionKey(c *gin.Context) {
	hash := strings.ToLower(strings.TrimSpace(c.Query("key-hash")))
	if hash == "" {
		hash = strings.ToLower(strings.TrimSpace(c.Param("hash")))
	}
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key-hash is required"})
		return
	}
	var body distributionKeyWrite
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	entryIndex := -1
	for i := range h.cfg.DistributionKeys {
		if usagestats.MaskKeyLabel(h.cfg.DistributionKeys[i].Key) == hash {
			entryIndex = i
			break
		}
	}
	if entryIndex < 0 {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "distribution key not found"})
		return
	}
	entry := h.cfg.DistributionKeys[entryIndex]
	if body.Name != nil {
		entry.Name = strings.TrimSpace(*body.Name)
	}
	if body.Enabled != nil {
		entry.Enabled = body.Enabled
	}
	if body.ExpiresAt != nil {
		expires := strings.TrimSpace(*body.ExpiresAt)
		if expires != "" {
			if _, err := time.Parse(time.RFC3339, expires); err != nil {
				h.mu.Unlock()
				c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339 (or empty for never)"})
				return
			}
		}
		entry.ExpiresAt = expires
	}
	if body.AllowedModels != nil {
		entry.AllowedModels = body.AllowedModels
	}
	if body.QuotaUSD != nil {
		if *body.QuotaUSD < 0 {
			h.mu.Unlock()
			c.JSON(http.StatusBadRequest, gin.H{"error": "quota_usd must be >= 0 (0 or omitted = unlimited)"})
			return
		}
		entry.QuotaUSD = *body.QuotaUSD
	}
	h.cfg.DistributionKeys[entryIndex] = entry
	h.cfg.SanitizeDistributionKeys()
	if !h.saveConfigLocked(c) {
		h.mu.Unlock()
		return
	}
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.scheduleReloadAfterSave(c, snapshot)
	usage := h.distributionUsageSnapshot()
	c.JSON(http.StatusOK, gin.H{"distribution-key": h.distributionKeyEntryOut(entry, usage, time.Now())})
}

// DeleteDistributionKey handles DELETE /distribution-keys?key-hash=<hash>.
// The usage ledger keeps its row so reporting stays complete across a
// re-issue; create/delete cycles zero out with ResetUsage when wanted.
func (h *Handler) DeleteDistributionKey(c *gin.Context) {
	hash := strings.ToLower(strings.TrimSpace(c.Query("key-hash")))
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key-hash is required"})
		return
	}
	h.mu.Lock()
	next := make([]config.DistributionKey, 0, len(h.cfg.DistributionKeys))
	removed := false
	for _, entry := range h.cfg.DistributionKeys {
		if usagestats.MaskKeyLabel(entry.Key) == hash {
			removed = true
			continue
		}
		next = append(next, entry)
	}
	if !removed {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "distribution key not found"})
		return
	}
	h.cfg.DistributionKeys = next
	h.mu.Unlock()
	h.persist(c)
}

// PostDistributionKeyResetUsage handles POST /distribution-keys/reset-usage:
// zeroes one key's cumulative spend/counters ({key_hash} in the body).
func (h *Handler) PostDistributionKeyResetUsage(c *gin.Context) {
	var body struct {
		KeyHash string `json:"key_hash"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	hash := strings.ToLower(strings.TrimSpace(body.KeyHash))
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_hash is required"})
		return
	}
	store := distribution.Global()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage store unavailable"})
		return
	}
	store.Reset(hash)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "key_hash": hash})
}

// GetDistributionKeysUsage handles GET /distribution-keys/usage: per-key
// cumulative ledger rows merged with a usage-meters summary window
// (group_by=api_key), aligned to the meters' response style.
func (h *Handler) GetDistributionKeysUsage(c *gin.Context) {
	fromMS, toMS, ok := parseUsageWindow(c)
	if !ok {
		return
	}
	h.mu.Lock()
	entries := append([]config.DistributionKey(nil), h.cfg.DistributionKeys...)
	h.mu.Unlock()
	ledger := h.distributionUsageSnapshot()

	windowByHash := map[string]usagestats.SummaryRow{}
	if recorder := usagestats.Global(); recorder != nil {
		rows, err := recorder.Summary(c.Request.Context(), fromMS, toMS, usagestats.GroupAPIKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, row := range rows {
			windowByHash[strings.ToLower(strings.TrimSpace(row.Key))] = row
		}
	}

	out := make([]gin.H, 0, len(entries))
	for _, entry := range entries {
		hash := usagestats.MaskKeyLabel(entry.Key)
		row := gin.H{
			"key_hash":   hash,
			"key_masked": distributionKeyMasked(entry.Key),
			"name":       entry.Name,
			"quota_usd":  entry.QuotaUSD,
		}
		if usageRow, okRow := ledger[hash]; okRow {
			row["spent_usd"] = usageRow.SpentUSD
			row["requests"] = usageRow.Requests
			row["last_request_at"] = usageRow.LastRequestAt
		} else {
			row["spent_usd"] = 0.0
			row["requests"] = 0
		}
		if window, okWindow := windowByHash[hash]; okWindow {
			row["window"] = window
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{
		"rows": out,
		"from": fromMS,
		"to":   toMS,
	})
}

// mintDistributionKey generates a dk- prefixed random client key (16 random
// bytes, hex encoded — 35 chars total).
func mintDistributionKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read never fails on supported platforms; fall back to a longer
		// timestamp-derived value rather than dying (no log.Fatal per policy).
		return fmt.Sprintf("dk-%x%x", time.Now().UnixNano(), time.Now().UnixNano()^0x5a5a5a5a)
	}
	return "dk-" + hex.EncodeToString(buf)
}
