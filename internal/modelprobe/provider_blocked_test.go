package modelprobe

import (
	"errors"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type probeTestError struct {
	code int
	msg  string
}

func (e *probeTestError) Error() string   { return e.msg }
func (e *probeTestError) StatusCode() int { return e.code }

func TestClassifyProbeError_ProviderBlocked(t *testing.T) {
	blockedErr := &probeTestError{code: 401, msg: `cline: status 401: {"error":"You are not using the latest version of Cline. Please upgrade to continue"}`}
	if got := classifyProbeError(blockedErr); got != StatusProviderBlocked {
		t.Fatalf("blocked phase with marker text classified as %q, want %q", got, StatusProviderBlocked)
	}

	plainErr := &probeTestError{code: 401, msg: "cline: status 401: token expired"}
	if got := classifyProbeError(plainErr); got != StatusAuthError {
		t.Fatalf("expired token 401 classified as %q, want %q", got, StatusAuthError)
	}

	named := errors.New("model not available in your plan")
	if got := classifyProbeError(named); got != StatusNotAvailable {
		t.Fatalf("not-available marker classified as %q, want %q", got, StatusNotAvailable)
	}
}

func TestSection_IsProviderBlocked(t *testing.T) {
	blocked := &Section{PerModel: map[string]*ModelOutcome{
		"a": {Status: StatusProviderBlocked, Error: "401 latest version of Cline"},
		"b": {Status: StatusProviderBlocked, Error: "401 latest version of Cline"},
	}}
	if !blocked.IsProviderBlocked() {
		t.Fatal("all-blocked section must be marked blocked")
	}

	mixed := &Section{PerModel: map[string]*ModelOutcome{
		"a": {Status: StatusProviderBlocked},
		"b": {Status: StatusProviderBlocked},
		"c": {Status: StatusUsable},
	}}
	if mixed.IsProviderBlocked() {
		t.Fatal("mixed section must not be marked blocked (recovery happens on any success)")
	}

	empty := &Section{PerModel: map[string]*ModelOutcome{}}
	if empty.IsProviderBlocked() {
		t.Fatal("empty section must never be blocked")
	}

	authErr := &Section{PerModel: map[string]*ModelOutcome{
		"a": {Status: StatusAuthError},
	}}
	if authErr.IsProviderBlocked() {
		t.Fatal("auth_error section must not be confused with block phase")
	}
}

func TestMergeSections_ClearsBlockOnRecoveryAndKeepsCatalogSize(t *testing.T) {
	previous := &Section{
		CheckedAt:   "2026-10-01T00:00:00Z",
		CatalogSize: 10,
		PerModel: map[string]*ModelOutcome{
			"a": {Status: StatusProviderBlocked},
			"b": {Status: StatusProviderBlocked},
		},
	}
	fresh := &Section{
		CheckedAt:   "2026-10-02T00:00:00Z",
		Usable:      []string{"a"},
		CatalogSize: 10,
		PerModel: map[string]*ModelOutcome{
			"a": {Status: StatusUsable},
			"b": {Status: StatusProviderBlocked},
		},
	}
	merged := MergeSections(previous, fresh)
	if merged == nil {
		t.Fatal("merge returned nil")
	}
	if merged.IsProviderBlocked() {
		t.Fatal("a recovered model must clear the block mark (auto-revive semantics)")
	}
	if merged.CatalogSize != 10 {
		t.Fatalf("catalog size = %d, want 10", merged.CatalogSize)
	}
	if got := merged.PerModel["b"].Status; got != StatusProviderBlocked {
		t.Fatalf("untouched model b = %q", got)
	}
	// A later fully-blocked cycle re-marks everything.
	second := MergeSections(merged, &Section{
		CheckedAt: "2026-10-03T00:00:00Z",
		PerModel: map[string]*ModelOutcome{
			"a": {Status: StatusProviderBlocked},
			"b": {Status: StatusProviderBlocked},
		},
	})
	if !second.IsProviderBlocked() {
		t.Fatal("a fully-blocked later cycle must re-mark the block phase")
	}
}

func TestSummaryForAuth_BlockedFields(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "a.json", Metadata: map[string]any{}}
	section := &Section{
		CheckedAt:   "2026-10-01T00:00:00Z",
		CatalogSize: 7,
		PerModel: map[string]*ModelOutcome{
			"m1": {Status: StatusProviderBlocked},
			"m2": {Status: StatusProviderBlocked},
		},
	}
	auth.Metadata[MetadataKey] = section
	out := SummaryForAuth(auth)
	if out["catalog_size"] != 7 {
		t.Fatalf("catalog_size = %v", out["catalog_size"])
	}
	if out["status"] != string(StatusProviderBlocked) {
		t.Fatalf("blocked marker = %v", out["status"])
	}
	if out["pruned"] != 0 || out["usable"] != 0 {
		t.Fatalf("blocked must not prune or fake-usable: %+v", out)
	}
}
