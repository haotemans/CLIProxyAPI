package providers

import (
	"testing"
)

func TestRegisterLookupCaseInsensitive(t *testing.T) {
	spec := Spec{Key: "Nova"}
	Register(spec)
	t.Cleanup(func() { Unregister("nova") })

	if _, ok := Lookup("nova"); !ok {
		t.Fatalf("Lookup(\"nova\") missed after Register with Key \"Nova\"")
	}
	if _, ok := Lookup("NOVA"); !ok {
		t.Fatalf("Lookup(\"NOVA\") missed; lookup must be case-insensitive")
	}
	if _, ok := Lookup(" Nova "); !ok {
		t.Fatalf("Lookup must trim whitespace")
	}
	got, _ := Lookup("nova")
	if got.Key != "nova" {
		t.Fatalf("Spec.Key normalized to %q, want canonical lowercase \"nova\"", got.Key)
	}
}

func TestRegisterLabelsResolveToSameSpec(t *testing.T) {
	Register(Spec{Key: "nova", Labels: []string{"Nova-AI", "nova.ai"}})
	t.Cleanup(func() { Unregister("nova") })

	for _, key := range []string{"nova", "nova-ai", "NOVA.AI"} {
		spec, ok := Lookup(key)
		if !ok {
			t.Fatalf("Lookup(%q) missed for registered label", key)
		}
		if spec.Key != "nova" {
			t.Fatalf("Lookup(%q).Key = %q, want canonical \"nova\"", key, spec.Key)
		}
	}
	// Unregister by canonical key must drop label bindings too.
	Unregister("nova")
	if _, ok := Lookup("nova-ai"); ok {
		t.Fatalf("label binding survived Unregister of canonical key")
	}
}

func TestRegisterOverwrite(t *testing.T) {
	Register(Spec{Key: "dup", Baseline: false})
	t.Cleanup(func() { Unregister("dup") })
	Register(Spec{Key: "dup", Baseline: true})

	got, ok := Lookup("DUP")
	if !ok {
		t.Fatalf("Lookup missed after overwrite")
	}
	if !got.Baseline {
		t.Fatalf("overwrite semantics broken: want last write to win")
	}
}

func TestAllDeduplicatesLabels(t *testing.T) {
	Register(Spec{Key: "solo"})
	Register(Spec{Key: "multi", Labels: []string{"multi-a", "multi-b"}})
	t.Cleanup(func() {
		Unregister("solo")
		Unregister("multi")
	})

	all := All()
	counts := make(map[string]int)
	foundSolo, foundMulti := false, false
	for _, spec := range all {
		counts[spec.Key]++
		if spec.Key == "solo" {
			foundSolo = true
		}
		if spec.Key == "multi" {
			foundMulti = true
		}
	}
	if !foundSolo || !foundMulti {
		t.Fatalf("All() = %+v, want both solo and multi present", all)
	}
	if counts["multi"] != 1 {
		t.Fatalf("All() returned multi %d times, want exactly once despite labels", counts["multi"])
	}
	if counts["solo"] != 1 {
		t.Fatalf("All() returned solo %d times, want exactly once", counts["solo"])
	}
}

func TestLookupMissAndEmpty(t *testing.T) {
	if _, ok := Lookup("definitely-not-registered-p1"); ok {
		t.Fatalf("Lookup hit for unregistered key")
	}
	if _, ok := Lookup(""); ok {
		t.Fatalf("Lookup hit for empty key")
	}
	if _, ok := Lookup("   "); ok {
		t.Fatalf("Lookup hit for whitespace key")
	}
}

type stubEntry struct {
	apiKey  string
	baseURL string
	prefix  string
	proxy   string
}

func (e stubEntry) GetAPIKey() string   { return e.apiKey }
func (e stubEntry) GetBaseURL() string  { return e.baseURL }
func (e stubEntry) GetPrefix() string   { return e.prefix }
func (e stubEntry) GetProxyURL() string { return e.proxy }

func TestAsEntriesFiltersTypedElements(t *testing.T) {
	raw := []any{stubEntry{apiKey: "k1"}, "not-an-entry", stubEntry{apiKey: "k2"}, 42}
	out := AsEntries[stubEntry](raw)
	if len(out) != 2 {
		t.Fatalf("AsEntries kept %d elements, want 2 typed ones", len(out))
	}
	if out[0].apiKey != "k1" || out[1].apiKey != "k2" {
		t.Fatalf("AsEntries order/content mismatch: %+v", out)
	}
	if got := AsEntries[stubEntry](nil); got != nil {
		t.Fatalf("AsEntries(nil) = %v, want nil", got)
	}
	if got := AsEntries[stubEntry]([]any{"x"}); got != nil {
		t.Fatalf("AsEntries with no typed elements = %v, want nil", got)
	}
}

type modelStub struct {
	name  string
	alias string
}

func (m modelStub) GetName() string  { return m.name }
func (m modelStub) GetAlias() string { return m.alias }

type modelEntryCarrier struct {
	models []any
}

func (c modelEntryCarrier) GetConfigModels() []any { return c.models }

func TestAsModelsExtractsTypedSliceFromEntry(t *testing.T) {
	entry := modelEntryCarrier{models: []any{modelStub{name: "a"}, "junk", modelStub{name: "b", alias: "B"}}}
	out := AsModels[modelStub](entry)
	if len(out) != 2 {
		t.Fatalf("AsModels kept %d models, want 2", len(out))
	}
	if out[1].alias != "B" {
		t.Fatalf("AsModels content mismatch: %+v", out)
	}
	if got := AsModels[modelStub]("not-an-entry"); got != nil {
		t.Fatalf("AsModels(non-carrier) = %v, want nil", got)
	}
	if got := AsModels[modelStub](modelEntryCarrier{}); got != nil {
		t.Fatalf("AsModels(empty carrier) = %v, want nil", got)
	}
}
