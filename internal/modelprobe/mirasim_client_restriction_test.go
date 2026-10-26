package modelprobe

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

const mirasimClientRestriction = "this client is not supported; use the mirasim client or Claude Code"

func TestMirasimClientRestrictionClassification(t *testing.T) {
	for _, code := range []int{401, 403} {
		if got := classifyProbeError(relayStatusError{code: code, body: mirasimClientRestriction}); got != StatusNotAvailable {
			t.Fatalf("client restriction with HTTP %d = %s", code, got)
		}
	}
	for _, message := range []string{"invalid access token", "this client is not supported"} {
		if got := classifyProbeError(relayStatusError{code: 401, body: message}); got != StatusAuthError {
			t.Fatalf("unrelated auth failure = %s", got)
		}
	}
}

func TestMirasimLegacyProbeFiltersClientRestrictionsAndRecovers(t *testing.T) {
	section := &Section{PerModel: map[string]*ModelOutcome{
		"good":       {Status: StatusUsable},
		"restricted": {Status: StatusAuthError, Error: mirasimClientRestriction},
		"expired":    {Status: StatusAuthError, Error: "invalid access token"},
		"limited":    {Status: StatusLimited},
	}}
	models := []*registry.ModelInfo{{ID: "good"}, {ID: "restricted"}, {ID: "expired"}, {ID: "limited"}, {ID: "unprobed"}}
	got := FilterPrunedBySection(section, models)
	if len(got) != 4 {
		t.Fatalf("filtered models = %v, want only the client-restricted model removed", got)
	}
	for _, model := range got {
		if model.ID == "restricted" {
			t.Fatal("legacy client restriction remains advertised")
		}
	}
	if section.PerModel["restricted"].Status != StatusAuthError || len(section.Pruned) != 0 {
		t.Fatal("reading legacy outcomes must not rewrite credential metadata")
	}
	section.PerModel["restricted"] = &ModelOutcome{Status: StatusUsable}
	if got := FilterPrunedBySection(section, models); len(got) != 5 {
		t.Fatal("a later successful probe must restore the model")
	}
}
