package usagestats

import "testing"

func TestDefaultPricerLongestPrefix(t *testing.T) {
	p := NewPricer(nil)
	cases := []struct {
		model string
	}{
		{"claude-sonnet-4-5"},
		{"claude-opus-4-6"},
		{"gpt-5-codex-max"},
		{"gemini-2.5-pro-exp"},
		{"grok-4.1-fast-reasoning-x"},
	}
	for _, tc := range cases {
		if _, ok := p.PriceFor(tc.model); !ok {
			t.Fatalf("no default price for %s", tc.model)
		}
	}
	if _, ok := p.PriceFor("unknown-model-xyz"); ok {
		t.Fatal("unknown model must have no price")
	}
}

func TestPricerComputedCost(t *testing.T) {
	p := NewPricer(nil)
	got := p.Cost("", "claude-sonnet-4-5", 1_000_000, 1_000_000)
	want := 3.0 + 15.0
	if got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	if got := p.Cost("claude-opus-4-1", "", 500_000, 100_000); got != 15 {
		t.Fatalf("fallback-to-model cost = %v, want 15", got)
	}
	if got := p.Cost("unknown-abc", "also-unknown", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("unknown models must price 0, got %v", got)
	}
}

func TestPricerCustomOverridesWin(t *testing.T) {
	p := NewPricer(map[string]Price{
		"claude":      {Input: 99, Output: 99},
		"gpt-5-codex": {Input: 1, Output: 3},
	})
	// Longest prefix: claude-sonnet-4-5 matches "claude" override (user) over longer default.
	if price, ok := p.PriceFor("claude-sonnet-4-5"); !ok || price.Input != 99 {
		t.Fatalf("user override should win on claude prefix: %+v ok=%v", price, ok)
	}
	if price, ok := p.PriceFor("gpt-5-codex-x"); !ok || price.Input != 1 || price.Output != 3 {
		t.Fatalf("custom gpt override = %+v ok=%v", price, ok)
	}
	// Longer builtin prefix still wins when it beats the user's shorter one? User's
	// rules win on equal pattern; longer builtin matches specific models more precisely.
	if price, ok := p.PriceFor("gpt-5-pro-x"); !ok || price.Input != 5 {
		t.Fatalf("builtin longer prefix fallback = %+v ok=%v", price, ok)
	}

	p.UpdateOverrides(map[string]Price{"claude": {Input: 1, Output: 1}})
	if price, ok := p.PriceFor("claude-opus-4-6"); !ok || price.Input != 1 {
		t.Fatalf("UpdateOverrides refresh = %+v", price)
	}
}

func TestPricerZenFreeZeroPrices(t *testing.T) {
	t.Cleanup(func() { RegisterZenFreeModelPrices(nil) })
	p := NewPricer(nil)

	RegisterZenFreeModelPrices([]string{"space-bunny-free", "deepseek-v4-flash-free"})
	price, ok := p.PriceFor("space-bunny-free")
	if !ok || price.Input != 0 || price.Output != 0 {
		t.Fatalf("zen free model price = %+v ok=%v, want exact zero", price, ok)
	}
	if !IsZeroPricedModel("Space-Bunny-FREE") {
		t.Fatal("zero-priced lookup must normalize case")
	}
	if !ok {
		t.Fatal("registered id must resolve")
	}
	// Unrelated prefixes keep resolving from defaults.
	if price, okDefault := p.PriceFor("claude-sonnet-4-5"); !okDefault || price.Input != 3 {
		t.Fatalf("defaults still resolve: %+v ok=%v", price, okDefault)
	}
	// A user override beats the zero-priced free set.
	custom := NewPricer(map[string]Price{"deepseek-v4-flash-free": {Input: 1, Output: 2}})
	if price, okCustom := custom.PriceFor("deepseek-v4-flash-free"); !okCustom || price.Input != 1 {
		t.Fatalf("user override must win over free zero: %+v ok=%v", price, okCustom)
	}
	// Re-sync with a shrunk feed drops the vanished id back to unpaid/no price.
	RegisterZenFreeModelPrices([]string{"mimo-v2.5-free"})
	if IsZeroPricedModel("space-bunny-free") {
		t.Fatal("vanished feed id must leave the zero-priced set")
	}
	if _, okGone := p.PriceFor("space-bunny-free"); okGone {
		t.Fatal("vanished id must not resolve a price anymore")
	}
}
