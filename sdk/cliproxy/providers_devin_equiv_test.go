package cliproxy

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/providers"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The devin Spec is the P1 shadow-validation: this suite asserts its fast
// path output equals the existing hand-written devin case output for key
// auth/config situations, without touching the original case.

func newDevinAuth(apiKey, baseURL string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "devin-equiv",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  apiKey,
			"base_url": baseURL,
		},
	}
}

func TestDevinRegistryModelsEquivalenceStaticCatalog(t *testing.T) {
	spec, ok := providers.Lookup("devin")
	if !ok {
		t.Fatalf("devin spec not registered (builtin import missing)")
	}
	if spec.Models == nil {
		t.Fatalf("devin spec.Models is nil")
	}
	base, _ := spec.Models(providers.ModelsRequest{Auth: newDevinAuth("k", ""), Cfg: &config.Config{}, AuthKind: "apikey"}).([]*registry.ModelInfo)
	hand := registry.GetDevinModels()
	if len(base) != len(hand) {
		t.Fatalf("fast path catalog %d models, hand case %d", len(base), len(hand))
	}
	// Same IDs in the same order (both come from GetDevinModels).
	for i := range base {
		if base[i].ID != hand[i].ID {
			t.Fatalf("catalog[%d] fast=%q hand=%q", i, base[i].ID, hand[i].ID)
		}
	}
}

func TestDevinRegistryEntriesResolutionEquivalence(t *testing.T) {
	cfg := &config.Config{
		DevinKey: []config.DevinKey{
			{
				APIKey:  "sk-a",
				BaseURL: "https://a.example.com",
				Models: []config.DevinModel{
					{Name: "upstream-a", Alias: "alias-a", DisplayName: "Alpha"},
				},
			},
			{
				APIKey:  "sk-b",
				BaseURL: "https://b.example.com",
			},
		},
	}
	spec, ok := providers.Lookup("devin")
	if !ok {
		t.Fatalf("devin spec not registered")
	}
	if spec.ResolveEntry == nil {
		t.Fatalf("devin spec.ResolveEntry is nil")
	}

	auth := newDevinAuth("sk-a", "https://a.example.com")

	// The registry path resolves through ResolveEntry (opaque ResolvedEntry);
	// the existing hand case resolves through resolveConfigDevinKey. Both must
	// pick the same concrete entry for this auth.
	re := spec.ResolveEntry(cfg, auth)
	if !re.HasEntry {
		t.Fatalf("registry ResolveEntry found no entry for auth sk-a")
	}
	svc := &Service{cfg: cfg}
	hand := svc.resolveConfigDevinKey(auth)
	if hand == nil {
		t.Fatalf("service resolver found no devin entry for auth sk-a")
	}
	// The opaque Entry carries the same concrete value the hand path returns.
	concrete, ok := re.Entry.(config.DevinKey)
	if !ok {
		t.Fatalf("re.Entry is %T, want config.DevinKey", re.Entry)
	}
	if concrete.APIKey != hand.APIKey || concrete.BaseURL != hand.BaseURL {
		t.Fatalf("resolution mismatch registry=%q/%q hand=%q/%q",
			concrete.APIKey, concrete.BaseURL, hand.APIKey, hand.BaseURL)
	}
	if len(concrete.Models) != len(hand.Models) {
		t.Fatalf("resolved models %d vs hand %d", len(concrete.Models), len(hand.Models))
	}
	if len(re.Excluded) != len(hand.ExcludedModels) {
		t.Fatalf("excluded %d vs hand %d", len(re.Excluded), len(hand.ExcludedModels))
	}

	// AliasModels must equal the hand-rendered asModelAliasEntries(entry.Models)
	// viewed through the opaque ResolvedEntry. The concrete type is
	// []auth.modelAliasEntry (package-private element interface), so assert the
	// rendered length and aliases via reflection.
	aliasSlice := reflect.ValueOf(re.AliasModels)
	if aliasSlice.Kind() != reflect.Slice {
		t.Fatalf("re.AliasModels is %T, want a slice", re.AliasModels)
	}
	if aliasSlice.Len() != len(hand.Models) {
		t.Fatalf("AliasModels %d vs hand models %d", aliasSlice.Len(), len(hand.Models))
	}
	for i := 0; i < aliasSlice.Len(); i++ {
		got := aliasSlice.Index(i).MethodByName("GetAlias").Call(nil)[0].String()
		if got != hand.Models[i].GetAlias() {
			t.Fatalf("AliasModels[%d].Alias=%q hand=%q", i, got, hand.Models[i].GetAlias())
		}
	}

	// BuildConfigModels must equal the existing buildDevinConfigModels on the
	// same resolved entry.
	raw := make([]any, 0, len(concrete.Models))
	for _, m := range concrete.Models {
		raw = append(raw, m)
	}
	fast, _ := spec.BuildConfigModels(raw, spec.Key, spec.Key).([]*registry.ModelInfo)
	handBuilt := buildDevinConfigModels(hand)
	if len(fast) != len(handBuilt) {
		t.Fatalf("BuildConfigModels %d vs buildDevinConfigModels %d", len(fast), len(handBuilt))
	}
	for i := range fast {
		if fast[i].ID != handBuilt[i].ID {
			t.Fatalf("built[%d] fast=%q hand=%q", i, fast[i].ID, handBuilt[i].ID)
		}
		if fast[i].DisplayName != handBuilt[i].DisplayName {
			t.Fatalf("built[%d] display fast=%q hand=%q", i, fast[i].DisplayName, handBuilt[i].DisplayName)
		}
	}
}

func TestDevinRegistryExecutorTypeEquivalence(t *testing.T) {
	spec, ok := providers.Lookup("devin")
	if !ok {
		t.Fatalf("devin spec not registered")
	}
	if spec.NewExecutor == nil {
		t.Fatalf("devin spec.NewExecutor is nil")
	}
	cfg := &config.Config{}
	exec, ok := spec.NewExecutor(providers.ExecutorRequest{Cfg: cfg}).(coreauth.ProviderExecutor)
	if !ok || exec == nil {
		t.Fatalf("spec.NewExecutor did not return a coreauth.ProviderExecutor: %T", spec.NewExecutor(providers.ExecutorRequest{Cfg: cfg}))
	}
	if _, isDevin := exec.(*runtimeexecutor.DevinExecutor); !isDevin {
		t.Fatalf("registry executor type %T, want *executor.DevinExecutor", exec)
	}
	if id := exec.Identifier(); id != "devin" {
		t.Fatalf("executor.Identifier() = %q, want devin", id)
	}
}

func TestDevinRegistryEntriesEmptyConfig(t *testing.T) {
	spec, _ := providers.Lookup("devin")
	if got := spec.Entries(nil); got != nil {
		t.Fatalf("spec.Entries(nil) = %v, want nil", got)
	}
	if got := spec.Entries(&config.Config{}); got != nil {
		t.Fatalf("spec.Entries(empty config) = %v, want nil", got)
	}
	// ResolveEntry against an empty/missing config must report no entry without
	// confusing "no entry" for "resolved nil entry".
	if re := spec.ResolveEntry(nil, newDevinAuth("sk-x", "")); re.HasEntry {
		t.Fatalf("ResolveEntry(nil) HasEntry=true, want false")
	}
	if re := spec.ResolveEntry(&config.Config{}, newDevinAuth("sk-x", "")); re.HasEntry {
		t.Fatalf("ResolveEntry(empty cfg) HasEntry=true, want false")
	}
}
