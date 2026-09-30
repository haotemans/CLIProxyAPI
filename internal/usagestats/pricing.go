package usagestats

import (
	"strings"
	"sync"
)

// Price holds USD rates per 1M tokens for one model family.
type Price struct {
	// Input is USD per 1M input tokens.
	Input float64
	// Output is USD per 1M output tokens.
	Output float64
}

// defaultPrices maps model-id prefixes (lowercase) to USD-per-1M rates. The
// longest matching prefix wins. Values mirror current public list prices for
// the mainstream families so computed cost is meaningful with zero config.
var defaultPrices = map[string]Price{
	// Claude family
	"claude-opus-4-5":   {Input: 5, Output: 25},
	"claude-opus-4-1":   {Input: 15, Output: 75},
	"claude-opus-4":     {Input: 15, Output: 75},
	"claude-opus":       {Input: 15, Output: 75},
	"claude-sonnet-4-5": {Input: 3, Output: 15},
	"claude-sonnet-4":   {Input: 3, Output: 15},
	"claude-3-5-sonnet": {Input: 3, Output: 15},
	"claude-haiku-4-5":  {Input: 1, Output: 5},
	"claude-haiku-3-5":  {Input: 0.8, Output: 4},
	"claude-haiku":      {Input: 1, Output: 5},
	// GPT family
	"gpt-5-pro":   {Input: 5, Output: 40},
	"gpt-5-codex": {Input: 1.25, Output: 10},
	"gpt-5":       {Input: 1.25, Output: 10},
	"gpt-5-mini":  {Input: 0.25, Output: 2},
	"gpt-5-nano":  {Input: 0.05, Output: 0.4},
	"gpt-4.1":     {Input: 2, Output: 8},
	"gpt-4o-mini": {Input: 0.15, Output: 0.6},
	"gpt-4o":      {Input: 2.5, Output: 10},
	"o4-mini":     {Input: 1.1, Output: 4.4},
	"o3":          {Input: 2, Output: 8},
	"o1":          {Input: 15, Output: 60},
	// Gemini family
	"gemini-3-pro":          {Input: 2, Output: 12},
	"gemini-3-flash":        {Input: 0.5, Output: 3},
	"gemini-2.5-pro":        {Input: 1.25, Output: 10},
	"gemini-2.5-flash":      {Input: 0.3, Output: 2.5},
	"gemini-2.0-flash":      {Input: 0.1, Output: 0.4},
	"gemini-2.5-flash-lite": {Input: 0.1, Output: 0.4},
	// Grok (xAI)
	"grok-4.1-fast-reasoning": {Input: 0.2, Output: 0.5},
	"grok-4":                  {Input: 3, Output: 15},
	"grok-3-mini":             {Input: 0.3, Output: 0.5},
	"grok-code-fast-1":        {Input: 0.2, Output: 1.5},
	// Kimi / Moonshot
	"kimi-k2":  {Input: 0.6, Output: 2.5},
	"moonshot": {Input: 0.6, Output: 2.5},
	// DeepSeek / others
	"deepseek-v3": {Input: 0.27, Output: 1.1},
	"deepseek-r1": {Input: 0.55, Output: 2.19},
	"deepseek":    {Input: 0.27, Output: 1.1},
	"qwen3-coder": {Input: 1.5, Output: 6},
	"minimax-m2":  {Input: 0.3, Output: 1.2},
}

// Pricer resolves computed cost per event from merged price tables.
type Pricer struct {
	mu     sync.RWMutex
	custom map[string]Price
}

// NewPricer builds a Pricer from optional user overrides (usage-stats.pricing:
// pattern -> {input, output}); override keys may be exact IDs or prefixes.
// Users overrides win on equal-prefix-length matches against defaults.
func NewPricer(custom map[string]Price) *Pricer {
	p := &Pricer{}
	if len(custom) > 0 {
		p.custom = make(map[string]Price, len(custom))
		for pattern, price := range custom {
			key := normalizePattern(pattern)
			if key == "" {
				continue
			}
			p.custom[key] = price
		}
	}
	return p
}

// UpdateOverrides replaces the custom table (hot config reload hook).
func (p *Pricer) UpdateOverrides(custom map[string]Price) {
	fresh := NewPricer(custom)
	p.mu.Lock()
	p.custom = fresh.custom
	p.mu.Unlock()
}

func normalizePattern(pattern string) string {
	return strings.ToLower(strings.TrimSpace(pattern))
}

// PriceFor returns the USD-per-1M price for a model identifier, preferring
// the longest custom prefix, then the longest default prefix.
func (p *Pricer) PriceFor(model string) (Price, bool) {
	model = normalizePattern(model)
	if model == "" {
		return Price{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if price, ok := longestPrefixPrice(p.custom, model); ok {
		return price, true
	}
	return longestPrefixPrice(defaultPrices, model)
}

func longestPrefixPrice(table map[string]Price, model string) (Price, bool) {
	bestLen := -1
	var best Price
	for pattern, price := range table {
		if !strings.HasPrefix(model, pattern) {
			continue
		}
		if len(pattern) > bestLen {
			bestLen = len(pattern)
			best = price
		}
	}
	return best, bestLen >= 0
}

// Cost computes computed-cost via the longest-prefix price across model and
// upstream_model candidates.
func (p *Pricer) Cost(model, upstreamModel string, inputTokens, outputTokens int64) float64 {
	price, ok := p.PriceFor(upstreamModel)
	if !ok {
		price, ok = p.PriceFor(model)
	}
	if !ok {
		return 0
	}
	return float64(inputTokens)*price.Input/1_000_000 + float64(outputTokens)*price.Output/1_000_000
}
