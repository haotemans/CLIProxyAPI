package mirasim

import (
	"context"
	"testing"
)

// The limits lane only serves accounts when the official client's probe
// marker rides along as a plain header (control calls sign empty metadata).
func TestFetchLimitsSendsQuotaProbeHeader(t *testing.T) {
	fake := newFakeMirasim(t)
	storage := newTestStorage(t, fake)
	client, err := NewRelayClient(&storage, "")
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	body, err := client.FetchLimits(context.Background())
	if err != nil {
		t.Fatalf("FetchLimits: %v", err)
	}
	if fake.SeenLimits == 0 {
		t.Fatal("relay never saw the limits call")
	}
	if got := fake.LastLimits.Get("x-mirasim-probe"); got != "usage" {
		t.Fatalf("x-mirasim-probe = %q, want usage", got)
	}
	if len(body) == 0 {
		t.Fatal("limits body must pass through")
	}
	// The models lane must NOT carry the marker (worries about stale lanes).
	if got := fake.LastLimits.Get("x-mirasim-sig"); got == "" {
		t.Fatal("signed control headers must still be present")
	}
}
