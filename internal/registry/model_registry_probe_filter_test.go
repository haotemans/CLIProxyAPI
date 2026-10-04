package registry

import "testing"

// Probe-hidden ids leave every client-facing list assembly immediately; the
// resolver is re-read per call so revives reappear without re-registration.
func TestGetAvailableModelsAppliesProbeHiddenFilter(t *testing.T) {
	t.Cleanup(func() { SetProbeHiddenModelsResolver(nil) })
	r := newTestModelRegistry()
	r.RegisterClient("cline-auth", "cline", []*ModelInfo{
		{ID: "dead-1", OwnedBy: "cline", DisplayName: "Dead"},
		{ID: "busy-1", OwnedBy: "cline", DisplayName: "Busy"},
		{ID: "alive-1", OwnedBy: "cline", DisplayName: "Alive"},
	})

	hidden := map[string]struct{}{"dead-1": {}}
	SetProbeHiddenModelsResolver(func() map[string]struct{} { return hidden })

	ids := modelIDsOf(r.GetAvailableModels("openai"))
	if ids["dead-1"] {
		t.Fatalf("probe-dead model leaked into /v1/models: %v", ids)
	}
	if !ids["busy-1"] || !ids["alive-1"] {
		t.Fatalf("busy/unpunished models must stay listed: %v", ids)
	}

	infos := r.GetAvailableModelInfos()
	foundDead, foundBusy := false, false
	for _, info := range infos {
		if info.ID == "dead-1" {
			foundDead = true
		}
		if info.ID == "busy-1" {
			foundBusy = true
		}
	}
	if foundDead || !foundBusy {
		t.Fatalf("metadata assembly must apply the same filter (dead=%v busy=%v)", foundDead, foundBusy)
	}

	// Cache-bypass check: even after a (cached) first assembly, changing the
	// resolver takes effect on the next call without any registration event.
	hidden = map[string]struct{}{"busy-1": {}}
	ids = modelIDsOf(r.GetAvailableModels("openai"))
	if ids["busy-1"] {
		t.Fatalf("static cache must not freeze probe state: %v", ids)
	}
	if !ids["dead-1"] {
		t.Fatalf("revived model must reappear on the next assembly: %v", ids)
	}

	SetProbeHiddenModelsResolver(nil)
	if got := r.GetAvailableModels("openai"); len(got) != 3 {
		t.Fatalf("clearing the resolver must restore the full list, got %d", len(got))
	}
}

func modelIDsOf(models []map[string]any) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, model := range models {
		if id, ok := model["id"].(string); ok {
			out[id] = true
		}
	}
	return out
}
