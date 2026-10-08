// Package providers provides a compile-time wiring registry for providers that
// follow the standard homogeneous channel shape: an api-key config entries
// slice, an advertised-models callback, and (optionally) an executor factory.
// Registering one Spec replaces the set of hand-written switch cases such a
// provider would otherwise need, and forms an optional fast path consulted
// before the existing provider switches. Special-case providers keep their
// hand-written logic.
//
// The package is SDK-neutral by design: it names only opaque values and
// function types so it never imports internal executors, auth, or registry.
// Composition (registry getters, executor factories, typed config joins) lives
// in internal/providers/builtin via closures handed to Register.
package providers

import (
	"strings"
	"sync"
)

// ModelsRequest is the input bag for a spec's advertised-models hook. Auth and
// Cfg carry the concrete *coreauth.Auth / *config.Config as opaque values the
// concrete closure type-asserts back. ScopedCfg lets the wiring layer forward
// a narrowed config view (e.g. a cfg with only this provider's entry slice)
// when relevant — the builtin devin closure ignores it today.
type ModelsRequest struct {
	Auth      any
	Cfg       any
	AuthKind  string
	ScopedCfg any
}

// ExecutorRequest is the input bag for a spec's executor factory. Cfg carries
// the concrete *config.Config as an opaque value.
type ExecutorRequest struct {
	Cfg any
}

// ResolvedEntry is the SDK-neutral result of resolving one api-key config
// entry for an auth. All its contents are opaque to the registry; callers in
// the conductor/service layer type-assert AliasModels back to the concrete
// []modelAliasEntry that the builtin closure produced through coreauth's
// asModelAliasEntries, and read Excluded directly.
//
// HasEntry distinguishes three states that the conductor fast path must not
// confuse:
//   - HasEntry=false            → no entry matched (fall through to existing switch)
//   - HasEntry=true, nil Alias  → an entry IS resolved but holds no models
//     (still handled by the registry path; do NOT fall through)
//   - HasEntry=true, non-nil    → entry resolved, models present
//
// AliasModels is the already-modelAliasEntry-rendered slice ([]coreauth
// modelAliasEntry, kept opaque here). Excluded is the entry's excluded-model
// list, consumed only by callers whose auth is kind "apikey".
type ResolvedEntry struct {
	HasEntry    bool
	AliasModels any
	Excluded    []string
	// Entry carries the resolved entry itself opaquely for callers that need
	// the concrete config value (e.g. service_models' BuildConfigModels path).
	// Conductors that only need AliasModels/Excluded ignore it.
	Entry any
}

// Spec describes every wiring point of a standard homogeneous channel. Models
// and Entries must be non-nil for the models fast path to engage; NewExecutor
// additionally enables the executor fast path.
type Spec struct {
	// Key is the canonical lowercase provider key, e.g. "nova".
	Key string

	// Labels are additional provider keys (lowercased, trimmed) beyond Key. A
	// provider with multiple spellings registers once and relies on label
	// lookup instead of duplicate switch cases.
	Labels []string

	// Models returns the advertised base catalog for one auth. The returned
	// value is the concrete []*registry.ModelInfo kept opaque here; the wiring
	// layer type-asserts it. It must return a fresh mutable slice like the
	// existing registry getters do (clone-on-read).
	Models func(req ModelsRequest) any

	// Entries returns the provider's api-key config slice as []any, each
	// element satisfying coreauth.APIKeyConfigEntry and GetConfigModels() []any
	// (adapted by the builtin package — the config structs stay untouched).
	// The []any indirection keeps the registry non-generic while callers keep
	// the generic resolveAPIKeyConfig helper via AsEntries below.
	Entries func(cfg any) []any

	// ResolveEntry resolves the single api-key config entry backing one auth,
	// returning a fully opaque ResolvedEntry. All generic config-type joins
	// (AsEntries → resolveAPIKeyConfig → asModelAliasEntries) are closed over
	// inside the builtin closure, so callers in the auth/conductor layer stay
	// free of any concrete config type, any literal provider key, and any
	// registry-internal type. Callers consume only AliasModels (already
	// rendered through coreauth's asModelAliasEntries) and Excluded. The cfg
	// and auth arguments carry the concrete values opaquely; the closure
	// type-asserts them back.
	ResolveEntry func(cfg any, auth any) ResolvedEntry

	// BuildConfigModels converts a resolved entry's config-listed model slice
	// ([]config.CodexModel passed as []any) into catalog model infos,
	// mirroring buildDevinConfigModels-style converters. The wiring layer
	// passes exact []config.CodexModel elements (any-holding by index), and
	// owned-by/model-type equal to Key.
	BuildConfigModels func(models []any, ownedBy, modelType string) any

	// NewExecutor builds the runtime executor for this provider, mirroring the
	// service_executors case. The returned concrete executor stays opaque here.
	NewExecutor func(req ExecutorRequest) any

	// Baseline marks whether the provider keeps an always-on executor (mirrors
	// baselineExecutorAuths membership). Reserved for P2; not consulted yet.
	Baseline bool
}

var (
	specsMu sync.RWMutex
	specs   = make(map[string]Spec)
)

func specKeys(key string, labels []string) []string {
	keys := make([]string, 0, 1+len(labels))
	if normalized := strings.ToLower(strings.TrimSpace(key)); normalized != "" {
		keys = append(keys, normalized)
	}
	for _, label := range labels {
		if normalized := strings.ToLower(strings.TrimSpace(label)); normalized != "" {
			keys = append(keys, normalized)
		}
	}
	return keys
}

// Register records s under its lowercase Key and Labels. Re-registering under
// a key that already exists replaces the previous Spec (last write wins), so
// re-registration and test overrides behave predictably instead of failing at
// import time.
func Register(s Spec) {
	keys := specKeys(s.Key, s.Labels)
	if len(keys) == 0 {
		return
	}
	s.Key = keys[0]
	specsMu.Lock()
	for _, key := range keys {
		specs[key] = s
	}
	specsMu.Unlock()
}

// Lookup returns the Spec registered under key, matching case-insensitively.
// The returned Spec's Key carries the canonical key from registration.
func Lookup(key string) (Spec, bool) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return Spec{}, false
	}
	specsMu.RLock()
	spec, ok := specs[normalized]
	specsMu.RUnlock()
	return spec, ok
}

// All returns a snapshot of the registered specs, deduplicated by canonical
// Key so multi-label providers appear once.
func All() []Spec {
	specsMu.RLock()
	out := make([]Spec, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for key, spec := range specs {
		if key != spec.Key {
			continue
		}
		if _, dup := seen[spec.Key]; dup {
			continue
		}
		seen[spec.Key] = struct{}{}
		out = append(out, spec)
	}
	specsMu.RUnlock()
	return out
}

// Unregister removes every binding of the Spec registered under key (including
// its labels). Used by tests to reset global state.
func Unregister(key string) {
	specsMu.Lock()
	spec, ok := specs[strings.ToLower(strings.TrimSpace(key))]
	if ok {
		for _, candidate := range specKeys(spec.Key, spec.Labels) {
			delete(specs, candidate)
		}
		delete(specs, key)
	}
	specsMu.Unlock()
}

// AsEntries views the registry-returned []any entries as []T so the existing
// generic resolveAPIKeyConfig helper in coreauth can run unchanged. Elements
// not satisfying T are dropped.
func AsEntries[T any](entries []any) []T {
	if len(entries) == 0 {
		return nil
	}
	out := make([]T, 0, len(entries))
	for _, entry := range entries {
		if typed, ok := entry.(T); ok {
			out = append(out, typed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AsModels extracts the config-listed models of a resolved entry as []T for
// the existing generic asModelAliasEntries/compileAPIKeyModelAliasForModels
// helpers in coreauth. Elements not satisfying T are dropped. The entry must
// expose GetConfigModels() []any.
func AsModels[T any](entry any) []T {
	models, ok := entry.(interface{ GetConfigModels() []any })
	if !ok || models == nil {
		return nil
	}
	raw := models.GetConfigModels()
	if len(raw) == 0 {
		return nil
	}
	out := make([]T, 0, len(raw))
	for _, model := range raw {
		if typed, ok := model.(T); ok {
			out = append(out, typed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExcludedModels pulls the entry's excluded-model list through the neutral
// interface so the wiring layer does not name the concrete config type.
func ExcludedModels(entry any) []string {
	if entry == nil {
		return nil
	}
	if carrier, ok := entry.(interface{ GetExcludedModels() []string }); ok {
		return carrier.GetExcludedModels()
	}
	return nil
}
