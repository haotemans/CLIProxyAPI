package modelprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// RequestExecutor is the executor surface a probe needs (satisfied by every
// provider executor in internal/runtime/executor).
type RequestExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

type executorFactory func(cfg *config.Config) RequestExecutor

// executorFactories maps provider keys to probe-capable executor builders.
// V1 drivers are executor-based (same translation path live traffic uses):
// cursor (Connect/H2), kiro (EventStream), cline, claude, codex, xai, devin,
// meta. Skipped providers (gemini/antigravity/vertex/aistudio/mirasim)
// are reported as unsupported drivers.
var executorFactories = map[string]executorFactory{
	"cline":  func(cfg *config.Config) RequestExecutor { return executor.NewClineExecutor(cfg) },
	"cursor": func(cfg *config.Config) RequestExecutor { return executor.NewCursorExecutor(cfg) },
	"kiro":   func(cfg *config.Config) RequestExecutor { return executor.NewKiroExecutor(cfg) },
	"claude": func(cfg *config.Config) RequestExecutor { return executor.NewClaudeExecutor(cfg) },
	"codex":  func(cfg *config.Config) RequestExecutor { return executor.NewCodexExecutor(cfg) },
	"xai":    func(cfg *config.Config) RequestExecutor { return executor.NewXAIExecutor(cfg) },
	"devin":  func(cfg *config.Config) RequestExecutor { return executor.NewDevinExecutor(cfg) },
	"meta":   func(cfg *config.Config) RequestExecutor { return executor.NewMetaExecutor(cfg) },
}

// SupportedProviders lists the V1 executor-driven probe drivers.
func SupportedProviders() []string {
	out := make([]string, 0, len(executorFactories))
	for key := range executorFactories {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Options tunes the engine.
type Options struct {
	// MaxModelsPerCredentialPerCycle probes at most this many models per
	// credential per cycle. 0 means no cap.
	MaxModelsPerCredentialPerCycle int
	// MaxParallel bounds concurrent in-flight probes (min 1).
	MaxParallel int
	// ProbeTimeoutPerModel bounds a single probe; credential acquisition
	// timeouts are the one spot AGENTS.md allows them, and a probe is one.
	ProbeTimeoutPerModel time.Duration
	// DriverOverrides lets tests replace executors per provider.
	DriverOverrides map[string]RequestExecutor
	// NowFunc overrides the clock; tests inject fake clocks.
	NowFunc func() time.Time
	// LogPrefix prefixes engine log lines; default "modelprobe".
	LogPrefix string
}

// Engine runs minimal live probes against provider executors.
type Engine struct {
	cfg         *config.Config
	opts        Options
	executorsMu sync.Mutex
	executors   map[string]RequestExecutor
}

// NewEngine constructs the probe engine bound to the runtime config.
func NewEngine(cfg *config.Config, opts Options) *Engine {
	return &Engine{
		cfg:       cfg,
		opts:      opts,
		executors: make(map[string]RequestExecutor, 12),
	}
}

// Now returns the engine's current time (fake-clock aware).
func (e *Engine) Now() time.Time {
	if e != nil && e.opts.NowFunc != nil {
		return e.opts.NowFunc()
	}
	return time.Now()
}

// HasDriver reports whether a provider has a probe implementation in V1.
func (e *Engine) HasDriver(provider string) bool {
	key := normalizeProvider(provider)
	if _, ok := executorFactories[key]; ok {
		return true
	}
	e.executorsMu.Lock()
	defer e.executorsMu.Unlock()
	_, ok := e.opts.DriverOverrides[key]
	return ok
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func (e *Engine) executorFor(provider string) (RequestExecutor, bool) {
	key := normalizeProvider(provider)
	e.executorsMu.Lock()
	defer e.executorsMu.Unlock()
	if driver, ok := e.opts.DriverOverrides[key]; ok {
		return driver, true
	}
	if exec, ok := e.executors[key]; ok {
		return exec, true
	}
	factory, ok := executorFactories[key]
	if !ok {
		return nil, false
	}
	exec := factory(e.cfg)
	if exec == nil {
		return nil, false
	}
	e.executors[key] = exec
	return exec, true
}

// ProbeOne runs one live request for (auth, model) and returns the outcome.
func (e *Engine) ProbeOne(ctx context.Context, auth *cliproxyauth.Auth, provider, model string) ModelOutcome {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelOutcome{Status: StatusUnreachable, Error: "empty model id"}
	}
	if auth == nil {
		return ModelOutcome{Status: StatusAuthError, Error: "auth is nil"}
	}
	exec, ok := e.executorFor(provider)
	if !ok {
		return ModelOutcome{Status: StatusUnreachable, Error: "no probe driver for provider " + provider}
	}
	timeout := e.opts.ProbeTimeoutPerModel
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := cliproxyexecutor.Request{Model: model, Payload: buildProbePayload(model)}
	_, err := exec.Execute(probeCtx, auth, req, probeOptions())
	outcome := ModelOutcome{
		Status:    classifyProbeError(err),
		CheckedMS: e.Now().UnixMilli(),
	}
	if err != nil {
		outcome.Error = summarizeProbeError(err)
	}
	return outcome
}

// CredentialCycle probes the given model list for one credential with bounded
// parallelism and returns the synthesized Section (persistence is a separate
// concern: see Store.ApplyOutcome).
func (e *Engine) CredentialCycle(ctx context.Context, auth *cliproxyauth.Auth, provider string, models []string) *Section {
	if auth == nil {
		return nil
	}
	provider = normalizeProvider(provider)
	if !e.HasDriver(provider) {
		log.Debugf("%s: skip %s (no probe driver)", logPrefix(e.opts), auth.ID)
		return nil
	}
	models = dedupeIDs(models)
	if len(models) == 0 {
		return ReadSection(auth.Metadata)
	}
	if cap := e.opts.MaxModelsPerCredentialPerCycle; cap > 0 && len(models) > cap {
		models = models[:cap]
	}
	outcomes := probeAll(ctx, e, auth, provider, models, maxInt(e.opts.MaxParallel, 1))
	section := synthesizeSection(e.Now(), outcomes)
	if len(section.Pruned) > 0 {
		log.Infof("%s: %s credential %s pruned %d models: %v", logPrefix(e.opts), provider, auth.ID, len(section.Pruned), firstN(section.Pruned, 6))
	}
	return section
}

// probeAll runs the probes on the worker pool deterministically.
func probeAll(ctx context.Context, e *Engine, auth *cliproxyauth.Auth, provider string, models []string, workers int) map[string]ModelOutcome {
	outcomes := make(map[string]ModelOutcome, len(models))
	if workers <= 1 {
		for _, model := range models {
			if ctx.Err() != nil {
				break
			}
			outcomes[model] = e.ProbeOne(ctx, auth, provider, model)
		}
		return outcomes
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, model := range models {
		model := model
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			outcome := e.ProbeOne(ctx, auth, provider, model)
			mu.Lock()
			outcomes[model] = outcome
			mu.Unlock()
		}()
	}
	wg.Wait()
	return outcomes
}

// synthesizeSection flattens outcomes into the persisted section. Only
// not_available lands in Pruned; limited/auth errors stay advertised, usable
// clears previous pruning on merge (handled by Store when merging on write).
func synthesizeSection(checked time.Time, outcomes map[string]ModelOutcome) *Section {
	usable := make([]string, 0, len(outcomes))
	pruned := make([]string, 0, len(outcomes))
	per := make(map[string]*ModelOutcome, len(outcomes))
	for id, outcome := range outcomes {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		processed := outcome
		processed.Error = strings.TrimSpace(outcome.Error)
		per[key] = &processed
		switch outcome.Status {
		case StatusUsable:
			usable = append(usable, key)
		case StatusNotAvailable:
			pruned = append(pruned, key)
		}
	}
	sort.Strings(usable)
	sort.Strings(pruned)
	return &Section{
		CheckedAt: checked.UTC().Format(time.RFC3339),
		Usable:    usable,
		Pruned:    pruned,
		PerModel:  per,
	}
}

func dedupeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func firstN(ids []string, n int) []string {
	if len(ids) <= n {
		return ids
	}
	return ids[:n]
}

func logPrefix(opts Options) string {
	if opts.LogPrefix != "" {
		return opts.LogPrefix
	}
	return "modelprobe"
}

// buildProbePayload synthesizes the minimal request body shared by all
// executor drivers (a single-turn chat with one-token budget).
func buildProbePayload(model string) []byte {
	payload, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	})
	if err != nil {
		return []byte(
			fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":1}`, model),
		)
	}
	return payload
}

func probeOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
}

// summarizeProbeError produces a bounded, log-safe error string for metadata.
func summarizeProbeError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	const cap = 400
	if len(msg) > cap {
		msg = msg[:cap] + "..."
	}
	return msg
}
