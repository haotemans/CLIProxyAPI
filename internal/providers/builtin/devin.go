// Package builtin registers the built-in providers that follow the standard
// homogeneous channel shape. Importing this package (via a blank import from
// the service wiring) is what wires the providers fast path for those
// channels. The closures below adapt the real internal types (registry catalog
// getter, executor factory, config entries slice, config-models converter) into
// the neutral providers.Spec hooks so the registry package itself stays
// SDK-clean.
package builtin

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/providers"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// entriesForCfg unwraps the opaque cfg argument to the typed config pointer.
func entriesForCfg(cfg any) []any {
	typed, ok := cfg.(*config.Config)
	if !ok || typed == nil || len(typed.DevinKey) == 0 {
		return nil
	}
	out := make([]any, 0, len(typed.DevinKey))
	for _, entry := range typed.DevinKey {
		out = append(out, entry)
	}
	return out
}

// authAsCoreauth unwraps the opaque auth argument to the typed coreauth auth.
func authAsCoreauth(auth any) *coreauth.Auth {
	typed, _ := auth.(*coreauth.Auth)
	return typed
}

// entriesToAliasModels renders a resolved DevinKey entry's config-listed
// models through coreauth's generic alias renderer so callers get an opaque
// []modelAliasEntry without ever touching the concrete config model type.
func entriesToAliasModels(entry *config.DevinKey) any {
	if entry == nil {
		return nil
	}
	return coreauth.AsModelAliasEntriesExported(entry.Models)
}

func init() {
	// Devin is the shadow-validation provider for the P1 fast path: its Spec
	// mirrors the existing hand-written devin cases and is asserted equivalent
	// against them. The original devin cases stay in place; the fast path only
	// adds an alternative lookup entry.
	providers.Register(providers.Spec{
		Key:      "devin",
		Baseline: true,

		// Models mirrors the service_models devin case base: the static catalog.
		Models: func(req providers.ModelsRequest) any {
			return registry.GetDevinModels()
		},

		// Entries returns cfg.DevinKey as []any of config.DevinKey values
		// (satisfying coreauth.APIKeyConfigEntry); kept for structural parity
		// with the Spec contract while ResolveEntry is the consumed fast path.
		Entries: entriesForCfg,

		// ResolveEntry closes over the generic path callers must never see:
		// Entries → AsEntries[config.DevinKey] → coreauth.ResolveAPIKeyConfigExported
		// → asModelAliasEntries. Callers get AliasModels already rendered and
		// Excluded for api-key auths; Entry carries the resolved concrete value.
		ResolveEntry: func(cfg any, auth any) providers.ResolvedEntry {
			resolved := coreauth.ResolveAPIKeyConfigExported(
				providers.AsEntries[config.DevinKey](entriesForCfg(cfg)),
				authAsCoreauth(auth))
			if resolved == nil {
				return providers.ResolvedEntry{HasEntry: false}
			}
			return providers.ResolvedEntry{
				HasEntry:    true,
				AliasModels: entriesToAliasModels(resolved),
				Excluded:    resolved.ExcludedModels,
				Entry:       *resolved,
			}
		},

		// BuildConfigModels mirrors buildDevinConfigModels + the per-entry
		// postprocessing the service_models devin case performs on top of the
		// generic builder (userDefined via buildConfigModels, context window,
		// IsCompat, thinking normalization, native capability overlay).
		BuildConfigModels: func(models []any, ownedBy, modelType string) any {
			if len(models) == 0 {
				return nil
			}
			typedModels := providers.AsModels[config.DevinModel](codexEntryCarrier{models: models})
			if len(typedModels) == 0 {
				return nil
			}
			return buildRegisteredConfigModels(typedModels, ownedBy, modelType, modelType)
		},

		// NewExecutor mirrors service_executors' devin case.
		NewExecutor: func(req providers.ExecutorRequest) any {
			typed, _ := req.Cfg.(*config.Config)
			return executor.NewDevinExecutor(typed)
		},
	})
}

// codexEntryCarrier adapts a raw []any of config.DevinModel into the
// GetConfigModels contract providers.AsModels expects.
type codexEntryCarrier struct {
	models []any
}

func (c codexEntryCarrier) GetConfigModels() []any { return c.models }

// buildRegisteredConfigModels reproduces the existing buildConfigModels
// pipeline (service_models.go) so a registered channel's config-listed models
// render identically: alias fallback to name, display-name fallback to
// GetFallbackDisplayName/alias, metadata model id fallback, thinking resolved
// through modelconfig.ResolveModelInfo, native capability overlay from the
// static catalog channel, and per-entry context/IsCompat overrides.
func buildRegisteredConfigModels(models []config.DevinModel, ownedBy, modelType, metadataChannel string) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	entries := make([]registeredConfigModelEntry, 0, len(models))
	for _, m := range models {
		entries = append(entries, registeredConfigModelEntry{model: m})
	}
	return buildRegisteredConfigModelEntries(entries, ownedBy, modelType, metadataChannel)
}

// registeredConfigModelEntry adapts config.DevinModel to the neutral contract
// buildRegisteredConfigModelEntries consumes.
type registeredConfigModelEntry struct {
	model config.DevinModel
}

func (e registeredConfigModelEntry) GetName() string        { return e.model.GetName() }
func (e registeredConfigModelEntry) GetAlias() string       { return e.model.GetAlias() }
func (e registeredConfigModelEntry) GetDisplayName() string { return e.model.GetDisplayName() }
func (e registeredConfigModelEntry) GetFallbackDisplayName() string {
	return e.model.GetName()
}
func (e registeredConfigModelEntry) GetThinking() *registry.ThinkingSupport {
	return e.model.Thinking
}
func (e registeredConfigModelEntry) GetMaxContextLength() int { return e.model.GetMaxContextLength() }
func (e registeredConfigModelEntry) GetIsCompat() bool        { return e.model.GetIsCompat() }

// buildRegisteredConfigModelEntries mirrors sdk/cliproxy.buildConfigModels
// faithfully on the neutral entry contract.
func buildRegisteredConfigModelEntries(models []registeredConfigModelEntry, ownedBy, modelType, metadataChannel string) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	now := nowUnix()
	out := make([]*registry.ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.GetName())
		if name == "" {
			continue
		}
		alias := strings.TrimSpace(model.GetAlias())
		if alias == "" {
			alias = name
		}
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		displayName := strings.TrimSpace(model.GetDisplayName())
		if displayName == "" {
			displayName = strings.TrimSpace(model.GetFallbackDisplayName())
		}
		if displayName == "" {
			displayName = alias
		}
		metadataModelID := name
		if metadataModelID == "" {
			metadataModelID = alias
		}

		info := &registry.ModelInfo{
			ID:              alias,
			MetadataModelID: metadataModelID,
			Object:          "model",
			Created:         now,
			OwnedBy:         ownedBy,
			Type:            modelType,
			DisplayName:     displayName,
			UserDefined:     true,
		}
		if model.GetThinking() != nil {
			info.ExplicitThinking = true
		}
		if resolved := modelconfig.ResolveModelInfo(name, modelType, model.GetThinking()); resolved.Thinking != nil {
			info.Thinking = resolved.Thinking
		}
		if staticInfo := registry.LookupStaticModelInfoByChannel(name, metadataChannel); staticInfo != nil && staticInfo.NativeCapabilities != nil {
			clone := *staticInfo
			info.NativeCapabilities = clone.NativeCapabilities
		}
		if maxContext := model.GetMaxContextLength(); maxContext > 0 {
			info.ContextLength = maxContext
			info.MaxContextLength = maxContext
		}
		if model.GetIsCompat() {
			info.IsCompat = true
		}
		out = append(out, info)
	}
	return out
}

var nowUnix = func() int64 {
	return time.Now().Unix()
}
