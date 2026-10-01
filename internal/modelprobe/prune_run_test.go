package modelprobe

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func pruneRunMetadata() map[string]any {
	return map[string]any{
		MetadataKey: &Section{
			CheckedAt: "2026-10-01T00:00:00Z",
			Usable:    []string{"good"},
			Pruned:    []string{"not-available"},
			PerModel: map[string]*ModelOutcome{
				"good":          {Status: StatusUsable},
				"not-available": {Status: StatusNotAvailable},
				"busy":          {Status: StatusLimited},
				"down":          {Status: StatusUnreachable},
				"rejected":      {Status: StatusAuthError},
				"healed":        {Status: StatusUsable},
			},
			PruneRun: &PruneRun{
				At:      "2026-10-01T00:00:00Z",
				Removed: []string{"not-available", "busy", "down", "rejected", "healed"},
			},
		},
	}
}

func TestFilterPrunedForAuthAggressivePruneRun(t *testing.T) {
	models := []*registry.ModelInfo{
		{ID: "good"},
		{ID: "not-available"},
		{ID: "busy"},
		{ID: "down"},
		{ID: "rejected"},
		{ID: "healed"},
		{ID: "unprobed"},
	}
	filtered := FilterPrunedForAuth(pruneRunMetadata(), models)
	kept := map[string]bool{}
	for _, model := range filtered {
		kept[model.ID] = true
	}
	if !kept["good"] || !kept["unprobed"] {
		t.Fatalf("usable and unprobed models must stay: %+v", kept)
	}
	for _, gone := range []string{"not-available", "busy", "down", "rejected"} {
		if kept[gone] {
			t.Fatalf("aggressively removed non-usable %s must stay hidden", gone)
		}
	}
	// Self-healing: an aggressively removed id whose latest outcome turned
	// usable re-enters the catalog; the marker stays as audit only.
	if !kept["healed"] {
		t.Fatalf("removed-but-now-usable model must re-enter: %+v", kept)
	}
}

func TestFilterPrunedForAuthConservativeUnchanged(t *testing.T) {
	metadata := map[string]any{
		MetadataKey: &Section{
			CheckedAt: "2026-10-01T00:00:00Z",
			Usable:    []string{"good"},
			Pruned:    []string{"not-available"},
			PerModel: map[string]*ModelOutcome{
				"good":          {Status: StatusUsable},
				"not-available": {Status: StatusNotAvailable},
				"busy":          {Status: StatusLimited},
			},
		},
	}
	filtered := FilterPrunedForAuth(metadata, []*registry.ModelInfo{{ID: "good"}, {ID: "not-available"}, {ID: "busy"}})
	if len(filtered) != 2 || filtered[0].ID != "good" || filtered[1].ID != "busy" {
		t.Fatalf("conservative filter = %+v, want pruned-only removal", filtered)
	}
}

func TestMergeSectionsPreservesPruneRunAsAudit(t *testing.T) {
	previous := &Section{
		CheckedAt: "2026-10-01T00:00:00Z",
		Usable:    []string{"good"},
		PerModel: map[string]*ModelOutcome{
			"good": {Status: StatusUsable},
			"busy": {Status: StatusLimited},
		},
		PruneRun: &PruneRun{At: "2026-10-01T00:00:00Z", Removed: []string{"busy"}},
	}
	// A conservative scheduled cycle must not erase the manual marker.
	fresh := &Section{
		CheckedAt: "2026-10-01T06:00:00Z",
		Usable:    []string{"good"},
		PerModel: map[string]*ModelOutcome{
			"good": {Status: StatusUsable},
		},
	}
	merged := MergeSections(previous, fresh)
	if merged.PruneRun == nil || len(merged.PruneRun.Removed) != 1 || merged.PruneRun.Removed[0] != "busy" {
		t.Fatalf("scheduled merge dropped prune_run: %+v", merged.PruneRun)
	}
	// A new aggressive run replaces the marker.
	next := &Section{
		CheckedAt: "2026-10-01T12:00:00Z",
		Usable:    []string{"good"},
		PerModel: map[string]*ModelOutcome{
			"good": {Status: StatusUsable},
		},
		PruneRun: &PruneRun{At: "2026-10-01T12:00:00Z", Removed: []string{}},
	}
	replaced := MergeSections(merged, next)
	if replaced.PruneRun == nil || replaced.PruneRun.At != "2026-10-01T12:00:00Z" {
		t.Fatalf("aggressive run must replace the marker: %+v", replaced.PruneRun)
	}
}

func TestDecodeSectionNormalizesPruneRun(t *testing.T) {
	raw := map[string]any{
		"checked_at": "2026-10-01T00:00:00Z",
		"prune_run": map[string]any{
			"at":      "2026-10-01T00:00:00Z",
			"removed": []any{" GPT-4O ", "gpt-4o", "", "busy-429"},
		},
	}
	section, err := decodeSection(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if section.PruneRun == nil {
		t.Fatal("prune_run lost on decode")
	}
	got := section.PruneRun.Removed
	if len(got) != 2 || got[0] != "gpt-4o" || got[1] != "busy-429" {
		t.Fatalf("removed normalization = %+v", got)
	}
}
