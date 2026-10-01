package management

import (
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// litellmFetchTimeout bounds only the LiteLLM catalog download; the AGENTS.md
// rule against post-connect timeouts applies to upstream API traffic, not to
// this operator-triggered management fetch.
const litellmFetchTimeout = 60 * time.Second

// pricingEntry is one row of the merged pricing view.
type pricingEntry struct {
	Model  string  `json:"model"`
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Source string  `json:"source"` // default | user | litellm
}

// mergedPricingEntries combines the embedded defaults with the configured
// overrides into one sorted view; overrides win on identical model keys.
func mergedPricingEntries(cfg *config.Config) []pricingEntry {
	merged := make(map[string]pricingEntry, 128)
	for model, price := range usagestats.DefaultPrices() {
		merged[model] = pricingEntry{Model: model, Input: price.Input, Output: price.Output, Source: "default"}
	}
	if cfg != nil {
		for model, price := range cfg.UsageStats.Pricing {
			key := strings.ToLower(strings.TrimSpace(model))
			if key == "" {
				continue
			}
			source := "user"
			if price.Source == usagestats.PriceSourceLitellm {
				source = usagestats.PriceSourceLitellm
			}
			merged[key] = pricingEntry{Model: key, Input: price.Input, Output: price.Output, Source: source}
		}
	}
	entries := make([]pricingEntry, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Model < entries[j].Model })
	return entries
}

// GetUsageMetersPricing handles GET /usage-meters/pricing: the merged
// defaults + overrides view consumed by the panel pricing tab.
func (h *Handler) GetUsageMetersPricing(c *gin.Context) {
	h.mu.Lock()
	entries := mergedPricingEntries(h.cfg)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// pricingModelParam resolves the target model: the explicit ?model= query
// wins (it carries ids containing slashes), the path param is the default.
func pricingModelParam(c *gin.Context) string {
	if model := strings.TrimSpace(c.Query("model")); model != "" {
		return strings.ToLower(model)
	}
	return strings.ToLower(strings.TrimSpace(c.Param("model")))
}

// PutUsageMetersPrice handles PUT /usage-meters/pricing/:model: upserts a
// user override ({input, output} USD per 1M tokens) into usage-stats.pricing.
func (h *Handler) PutUsageMetersPrice(c *gin.Context) {
	model := pricingModelParam(c)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}
	var body struct {
		Input  *float64 `json:"input"`
		Output *float64 `json:"output"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Input == nil || body.Output == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: {input, output} required"})
		return
	}
	input, output := *body.Input, *body.Output
	if math.IsNaN(input) || math.IsInf(input, 0) || math.IsNaN(output) || math.IsInf(output, 0) || input < 0 || output < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input/output must be finite numbers >= 0"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.UsageStats.Pricing == nil {
		h.cfg.UsageStats.Pricing = make(map[string]config.UsageStatsPrice)
	}
	// A user save clears any previous litellm source marker.
	h.cfg.UsageStats.Pricing[model] = config.UsageStatsPrice{Input: input, Output: output}
	h.persistLocked(c)
}

// DeleteUsageMetersPrice handles DELETE /usage-meters/pricing/:model: removes
// the override (user or litellm-synced), falling back to embedded defaults.
func (h *Handler) DeleteUsageMetersPrice(c *gin.Context) {
	model := pricingModelParam(c)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.UsageStats.Pricing == nil {
		h.cfg.UsageStats.Pricing = make(map[string]config.UsageStatsPrice)
	}
	if _, ok := h.cfg.UsageStats.Pricing[model]; !ok {
		// Also try the raw (un-lowercased) configured keys for cleanup.
		found := false
		for key := range h.cfg.UsageStats.Pricing {
			if strings.EqualFold(strings.TrimSpace(key), model) {
				delete(h.cfg.UsageStats.Pricing, key)
				found = true
			}
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "no override for model"})
			return
		}
	} else {
		delete(h.cfg.UsageStats.Pricing, model)
	}
	if len(h.cfg.UsageStats.Pricing) == 0 {
		h.cfg.UsageStats.Pricing = nil
	}
	h.persistLocked(c)
}

// SyncUsageMetersPricing handles POST /usage-meters/pricing/sync-litellm:
// downloads the LiteLLM price catalog (respecting requests.proxy-url) and
// merges it into usage-stats.pricing as litellm-sourced overrides, never
// clobbering user-managed entries.
func (h *Handler) SyncUsageMetersPricing(c *gin.Context) {
	h.mu.Lock()
	proxyURL := ""
	if h.cfg != nil {
		proxyURL = strings.TrimSpace(h.cfg.ProxyURL)
	}
	h.mu.Unlock()

	client := &http.Client{Timeout: litellmFetchTimeout}
	if proxyURL != "" && !strings.EqualFold(proxyURL, "direct") {
		transport, _, errTransport := proxyutil.BuildHTTPTransport(proxyURL)
		if errTransport != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid requests.proxy-url: " + errTransport.Error()})
			return
		}
		client.Transport = transport
	}

	req, errRequest := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, usagestats.LiteLLMPricesURL, nil)
	if errRequest != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errRequest.Error()})
		return
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "litellm fetch failed: " + errDo.Error()})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("usagestats: close litellm response body")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "litellm fetch failed: status " + resp.Status})
		return
	}
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if errRead != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "litellm read failed: " + errRead.Error()})
		return
	}
	prices, skippedInvalid, errMap := usagestats.MapLitellmPrices(data)
	if errMap != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errMap.Error()})
		return
	}

	h.mu.Lock()
	if h.cfg.UsageStats.Pricing == nil {
		h.cfg.UsageStats.Pricing = make(map[string]config.UsageStatsPrice)
	}
	added, updated, skippedUser := usagestats.MergeLitellmPrices(h.cfg.UsageStats.Pricing, prices)
	// Persist with a custom payload (persistLocked writes its own envelope).
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":          "ok",
		"synced":          added + updated,
		"added":           added,
		"updated":         updated,
		"skipped_user":    skippedUser,
		"skipped_invalid": skippedInvalid,
		"catalog_size":    len(prices),
	})
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
}
