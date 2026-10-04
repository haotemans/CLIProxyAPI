package mirasim

import (
	"reflect"
	"testing"
)

func TestParseModelRosterIDsShapes(t *testing.T) {
	raw := []byte(`{"data":[
		{"id":"claude-sonnet-4-6","object":"model"},
		{"id":"claude-haiku-4-5-20251001"},
		{"id":"claude-haiku-4-5"},
		"gpt-5.2",
		{"id":"deepseek-v4.1"},
		{"id":"vendor/claude-3-7-sonnet"},
		{"id":"gpt-4o-mini"},
		{"id":"*"},
		{"id":"claude-sonnet-4-6"},
		{"id":" glm-5.3 "},
		{"id":""}
	]}`)
	want := []string{"claude-sonnet-4-6", "claude-haiku-4-5", "gpt-5.2", "deepseek-v4.1", "glm-5.3"}
	if got, err := ParseModelRosterIDs(raw); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseModelRosterIDs = %v, %v; want %v", got, err, want)
	}
}

func TestParseModelRosterIDsModelsFallbackAndDatedTwinKeptAlone(t *testing.T) {
	// "models" shape (no data) + a dated id with no plain twin stays (it IS
	// the account's id for the model).
	raw := []byte(`{"models":["claude-3-5-haiku-20241022","*"]}`)
	got, err := ParseModelRosterIDs(raw)
	if err != nil || len(got) != 1 || got[0] != "claude-3-5-haiku-20241022" {
		t.Fatalf("models fallback = %v, %v", got, err)
	}
}

func TestParseModelRosterIDsEmptyAndJunk(t *testing.T) {
	if _, err := ParseModelRosterIDs([]byte(`{"data":["*","vendor/x"]}`)); err == nil {
		t.Fatal("no servable ids must error so callers keep the last-good roster")
	}
	if _, err := ParseModelRosterIDs([]byte(`not json`)); err == nil {
		t.Fatal("junk must error")
	}
}

func TestModelsFromMetadataRoundTrip(t *testing.T) {
	metadata := map[string]any{ModelsMetadataKey: []any{"gpt-5.2", "claude-sonnet-4-6"}}
	if got := ModelsFromMetadata(metadata); len(got) != 2 || got[0] != "gpt-5.2" {
		t.Fatalf("metadata roster = %v", got)
	}
	if got := ModelsFromMetadata(map[string]any{ModelsMetadataKey: []string{"a", " ", "b"}}); len(got) != 2 || got[1] != "b" {
		t.Fatalf("string slice roster = %v", got)
	}
	if got := ModelsFromMetadata(nil); got != nil {
		t.Fatalf("nil metadata = %v", got)
	}
}
