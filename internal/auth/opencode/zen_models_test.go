package opencode

import (
	"reflect"
	"testing"
)

func TestIsZenFreeAPIKey_MatchesExactly(t *testing.T) {
	if !IsZenFreeAPIKey("public") {
		t.Fatal("literal public must select the anonymous free tier")
	}
	for _, invalid := range []string{"PUBLIC", "Public", " public", "public ", "p ublic", ""} {
		if IsZenFreeAPIKey(invalid) {
			t.Fatalf("%q must NOT select the anonymous free tier", invalid)
		}
	}
}

func TestIsZenFreeModelID_Suffix(t *testing.T) {
	if !IsZenFreeModelID("space-bunny-free") || !IsZenFreeModelID("Nemotron-3-Ultra-FREE") {
		t.Fatal("-free suffixed ids must match")
	}
	for id := range map[string]bool{"kimi-k3": true, "grok-code-fast-1": true, "free": true, "": true, "space-bunny-freex": true} {
		if IsZenFreeModelID(id) {
			t.Fatalf("%q must not match the free suffix rule", id)
		}
	}
}

func TestParseZenFreeModelIDs_FiltersSortsDedupes(t *testing.T) {
	raw := []byte(`{"object":"list","data":[
		{"id":"kimi-k3","object":"model"},
		{"id":"mimo-v2.5-free","object":"model"},
		{"id":"space-bunny-free","object":"model"},
		{"id":"Space-Bunny-FREE","object":"model"},
		{"id":"  jev-1.13-free  ","object":"model"},
		{"id":"","object":"model"}
	]}`)
	want := []string{"jev-1.13-free", "mimo-v2.5-free", "space-bunny-free"}
	if got := ParseZenFreeModelIDs(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseZenFreeModelIDs = %v, want %v", got, want)
	}
}

func TestParseZenFreeModelIDs_ToleratesBareArrayAndJunk(t *testing.T) {
	if got := ParseZenFreeModelIDs([]byte(`[{"id":"fledge-alpha-free"},{"id":"gpt-5"}]`)); !reflect.DeepEqual(got, []string{"fledge-alpha-free"}) {
		t.Fatalf("bare array = %v", got)
	}
	if got := ParseZenFreeModelIDs([]byte(`not json`)); got != nil {
		t.Fatalf("junk = %v, want nil", got)
	}
}

func TestZenFreeCatalogURL(t *testing.T) {
	if got := ZenFreeCatalogURL("https://opencode.ai/zen/v1/"); got != "https://opencode.ai/zen/v1/models" {
		t.Fatalf("join = %s", got)
	}
	if got := ZenFreeCatalogURL(""); got != ZenFreeDefaultBaseURL+"/models" {
		t.Fatalf("empty base = %s", got)
	}
}
