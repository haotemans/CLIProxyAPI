package modelprobe

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func authWithOutcomeRows(provider string, rows map[string]Status) *cliproxyauth.Auth {
	per := make(map[string]*ModelOutcome, len(rows))
	usable := make([]string, 0, len(rows))
	pruned := make([]string, 0, len(rows))
	for id, status := range rows {
		per[id] = &ModelOutcome{Status: status}
		switch status {
		case StatusUsable:
			usable = append(usable, id)
		case StatusNotAvailable:
			pruned = append(pruned, id)
		}
	}
	return &cliproxyauth.Auth{
		ID:       provider + "-auth.json",
		Provider: provider,
		Metadata: map[string]any{
			MetadataKey: &Section{CheckedAt: "2026-10-01T00:00:00Z", Usable: usable, Pruned: pruned, PerModel: per},
		},
	}
}

func idsOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

func TestHiddenModelIDsClassification(t *testing.T) {
	auth := authWithOutcomeRows("cline", map[string]Status{
		"dead-1":     StatusNotAvailable, // tier-denied: dead
		"dead-2":     StatusNotAvailable,
		"busy-1":     StatusLimited,     // busy: kept visible (may recover)
		"flaky-1":    StatusUnreachable, // transient: kept visible
		"bad-auth-1": StatusAuthError,   // credential-level: kept visible
		"alive-1":    StatusUsable,
	})
	hidden := HiddenModelIDs([]*cliproxyauth.Auth{auth})
	for _, id := range []string{"dead-1", "dead-2"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("%s must be hidden: %v", id, idsOf(hidden))
		}
	}
	for _, id := range []string{"busy-1", "flaky-1", "bad-auth-1", "alive-1"} {
		if _, ok := hidden[id]; ok {
			t.Fatalf("%s must stay visible: %v", id, idsOf(hidden))
		}
	}
}

func TestHiddenModelIDsNeverProbedAndUnprobedCredentials(t *testing.T) {
	// Rows for dead-1 only; any model with no rows anywhere stays visible.
	probed := authWithOutcomeRows("cline", map[string]Status{"dead-1": StatusNotAvailable})
	unprobed := &cliproxyauth.Auth{ID: "other.json", Provider: "cline"}
	hidden := HiddenModelIDs([]*cliproxyauth.Auth{probed, unprobed})
	if _, ok := hidden["dead-1"]; !ok {
		t.Fatalf("probed-dead model must hide: %v", idsOf(hidden))
	}
	if len(hidden) != 1 {
		t.Fatalf("never-probed models must default visible, hidden = %v", idsOf(hidden))
	}
}

func TestHiddenModelIDsMixedProviderBlockedRows(t *testing.T) {
	// A credential in a partial clampdown records provider_blocked on some
	// rows while older rows still carry stale recoverable states. Each
	// provider_blocked row is unservable on its own even though the
	// credential as a whole is not yet flagged blocked.
	auth := authWithOutcomeRows("cline", map[string]Status{
		"clamped-1": StatusProviderBlocked,
		"clamped-2": StatusProviderBlocked,
		"stale-1":   StatusAuthError, // older round, still classed recoverable
		"busy-1":    StatusLimited,   // busy: kept visible
		"alive-1":   StatusUsable,
	})
	hidden := HiddenModelIDs([]*cliproxyauth.Auth{auth})
	for _, id := range []string{"clamped-1", "clamped-2"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("provider_blocked row %s must hide: %v", id, idsOf(hidden))
		}
	}
	for _, id := range []string{"stale-1", "busy-1", "alive-1"} {
		if _, ok := hidden[id]; ok {
			t.Fatalf("%s must stay visible: %v", id, idsOf(hidden))
		}
	}
}

func TestHiddenModelIDsProviderBlockedAndSalvage(t *testing.T) {
	blockedRows := map[string]Status{"m-1": StatusProviderBlocked, "m-2": StatusProviderBlocked, "m-3": StatusProviderBlocked}
	blocked := authWithOutcomeRows("cline", blockedRows)
	healthy := authWithOutcomeRows("cline-other", map[string]Status{"m-3": StatusUsable, "m-4": StatusUsable})
	hidden := HiddenModelIDs([]*cliproxyauth.Auth{blocked, healthy})
	// Provider-blocked credential cannot serve m-1/m-2 and nothing else covers them.
	for _, id := range []string{"m-1", "m-2"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("blocked-only model %s must hide: %v", id, idsOf(hidden))
		}
	}
	// Another healthy credential salvages m-3; unprobed m-4 stays.
	for _, id := range []string{"m-3", "m-4"} {
		if _, ok := hidden[id]; ok {
			t.Fatalf("%s must stay visible via another credential: %v", id, idsOf(hidden))
		}
	}

	// Revive path: same credentials once the blocked phase clears; nothing
	// m-* remains hidden because the section no longer reports blocked.
	revivedBlocked := authWithOutcomeRows("cline", map[string]Status{"m-1": StatusUsable, "m-2": StatusLimited})
	hiddenAfter := HiddenModelIDs([]*cliproxyauth.Auth{revivedBlocked, healthy})
	if len(hiddenAfter) != 0 {
		t.Fatalf("after revive nothing must stay hidden, got %v", idsOf(hiddenAfter))
	}
}
