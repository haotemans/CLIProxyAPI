package opencode

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

var zenFreeIDPattern = regexp.MustCompile(`^(msg|ses)_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func TestNewZenFreeFingerprintsShape(t *testing.T) {
	now := time.UnixMilli(1_791_394_469_000) // arbitrary fixed instant
	msg := NewZenFreeMessageFingerprint(now)
	ses := NewZenFreeSessionFingerprint(now)
	if !zenFreeIDPattern.MatchString(msg) {
		t.Fatalf("message fingerprint %q does not match %s", msg, zenFreeIDPattern)
	}
	if !zenFreeIDPattern.MatchString(ses) {
		t.Fatalf("session fingerprint %q does not match %s", ses, zenFreeIDPattern)
	}
	if !strings.HasPrefix(msg, "msg_") || !strings.HasPrefix(ses, "ses_") {
		t.Fatalf("wrong prefixes: msg=%q ses=%q", msg, ses)
	}
}

func TestNewZenFreeFingerprintsDistinct(t *testing.T) {
	now := time.Now()
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		id := NewZenFreeMessageFingerprint(now)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate message fingerprint at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
	msg := NewZenFreeMessageFingerprint(now)
	ses := NewZenFreeSessionFingerprint(now)
	if msg == ses {
		t.Fatalf("message and session fingerprints must differ, both %q", msg)
	}
}

func TestZenFreeFingerprintTimestampFresh(t *testing.T) {
	before := time.Now()
	msg := NewZenFreeMessageFingerprint(time.Now())
	after := time.Now()

	encoded, err := strconv.ParseUint(msg[4:16], 16, 64)
	if err != nil {
		t.Fatalf("message fingerprint hex segment not parseable: %v", err)
	}
	ms := int64(encoded >> zenFreeIDTimestampShift)
	if ms < before.UnixMilli() || ms > after.UnixMilli() {
		t.Fatalf("message fingerprint timestamp %d not within [%d, %d]", ms, before.UnixMilli(), after.UnixMilli())
	}
}

func TestZenFreeFingerprintTimestampOrdering(t *testing.T) {
	older := time.UnixMilli(1_700_000_000_000)
	newer := time.UnixMilli(1_800_000_000_000)

	oldMsg := NewZenFreeMessageFingerprint(older)
	newMsg := NewZenFreeMessageFingerprint(newer)
	if oldMsg[4:16] >= newMsg[4:16] {
		t.Fatalf("ascending msg hex not ordered: %q >= %q", oldMsg, newMsg)
	}

	oldSes := NewZenFreeSessionFingerprint(older)
	newSes := NewZenFreeSessionFingerprint(newer)
	if oldSes[4:16] <= newSes[4:16] {
		t.Fatalf("descending ses hex not reverse-ordered: %q <= %q", oldSes, newSes)
	}
}

func TestZenFreeSessionFingerprintDecodable(t *testing.T) {
	// The sink decodes the timestamp as 2^48-1 - bits (opencode's ~ on the
	// 48-bit window); our simplified encoding must round-trip.
	now := time.UnixMilli(1_791_394_469_000)
	ses := NewZenFreeSessionFingerprint(now)
	encoded, err := strconv.ParseUint(ses[4:16], 16, 64)
	if err != nil {
		t.Fatalf("session fingerprint hex segment not parseable: %v", err)
	}
	recovered := (zenFreeIDTimestampMask - encoded) >> zenFreeIDTimestampShift
	if recovered != uint64(now.UnixMilli()) {
		t.Fatalf("session fingerprint recovered ms %d, want %d", recovered, now.UnixMilli())
	}
}

func TestIsZenFreeFingerprintAuth(t *testing.T) {
	tests := []struct {
		provider string
		apiKey   string
		want     bool
	}{
		{"opencode-go", "public", true},
		{"opencode", "public", true},
		{"opencode-go", "PUBLIC", false},
		{"opencode-go", " public ", false},
		{"opencode-go", "sk-real-key", false},
		{"other-provider", "public", false},
		{"openai-compatibility", "public", false},
		{"", "public", false},
		{"opencode-go", "", false},
	}
	for _, tc := range tests {
		if got := IsZenFreeFingerprintAuth(tc.provider, tc.apiKey); got != tc.want {
			t.Errorf("IsZenFreeFingerprintAuth(%q, %q) = %v, want %v", tc.provider, tc.apiKey, got, tc.want)
		}
	}
}

func TestApplyZenFreeFingerprintHeaders(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), "POST", "http://example.invalid/v1/chat/completions", nil)
	ApplyZenFreeFingerprintHeaders(context.Background(), req)

	if got := req.Header.Get("User-Agent"); got != ZenFreeUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, ZenFreeUserAgent)
	}
	if got := req.Header.Get("X-Opencode-Client"); got != ZenFreeClientHeader {
		t.Errorf("X-Opencode-Client = %q, want %q", got, ZenFreeClientHeader)
	}
	if got := req.Header.Get("X-Opencode-Request"); !strings.HasPrefix(got, "msg_") || !zenFreeIDPattern.MatchString(got) {
		t.Errorf("X-Opencode-Request = %q, want msg_* fingerprint", got)
	}
	if got := req.Header.Get("X-Opencode-Session"); !strings.HasPrefix(got, "ses_") || !zenFreeIDPattern.MatchString(got) {
		t.Errorf("X-Opencode-Session = %q, want ses_* fingerprint", got)
	}
}

func TestApplyZenFreeFingerprintHeadersPinnedSession(t *testing.T) {
	ctx := WithZenFreeSessionFingerprint(context.Background(), "ses_fe5ee891a87evEEmHCg9qD0T1i")
	req := httptest.NewRequestWithContext(ctx, "POST", "http://example.invalid/v1/chat/completions", nil)
	ApplyZenFreeFingerprintHeaders(ctx, req)
	if got := req.Header.Get("X-Opencode-Session"); got != "ses_fe5ee891a87evEEmHCg9qD0T1i" {
		t.Errorf("pinned session not used, got %q", got)
	}
}

func TestApplyZenFreeFingerprintHeadersOverwriteUserHeaders(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), "POST", "http://example.invalid/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "custom-agent/9.9")
	req.Header.Set("X-Opencode-Client", "web")
	ApplyZenFreeFingerprintHeaders(context.Background(), req)
	if got := req.Header.Get("User-Agent"); got != ZenFreeUserAgent {
		t.Errorf("user header must be overwritten, User-Agent = %q", got)
	}
	if got := req.Header.Get("X-Opencode-Client"); got != ZenFreeClientHeader {
		t.Errorf("user header must be overwritten, X-Opencode-Client = %q", got)
	}
}

func TestApplyZenFreePayloadEnforcement(t *testing.T) {
	body := []byte(`{"model":"fledge-alpha-free","messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyZenFreePayloadEnforcement(body)

	if !gjson.GetBytes(out, "stream").Bool() {
		t.Error("stream was not forced true")
	}
	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() || len(tools.Array()) != 2 {
		t.Fatalf("tools = %s, want exactly 2 injected stubs", tools.Raw)
	}
	if !HasZenFreeFunctionTool(out, "bash") || !HasZenFreeFunctionTool(out, "read") {
		t.Errorf("bash/read stubs missing: %s", tools.Raw)
	}
	if b := gjson.GetBytes(out, "tools.0.function.name").String(); b != "bash" {
		t.Errorf("first stub = %q, want bash", b)
	}
	if r := gjson.GetBytes(out, "tools.1.function.name").String(); r != "read" {
		t.Errorf("second stub = %q, want read", r)
	}
	// Stub schemas carry the documented command/path parameters.
	if !gjson.GetBytes(out, "tools.0.function.parameters.required.0").Exists() {
		t.Errorf("bash stub lacks required parameters: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
	if !json.Valid([]byte(tools.Raw)) {
		t.Errorf("tools not valid JSON: %s", tools.Raw)
	}
}

func TestApplyZenFreePayloadEnforcementExistingBash(t *testing.T) {
	before := []byte(`{"stream":false,"tools":[{"type":"function","function":{"name":"bash","description":"user bash","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}]}`)
	out := ApplyZenFreePayloadEnforcement(before)

	if !gjson.GetBytes(out, "stream").Bool() {
		t.Error("stream was not forced true")
	}
	tools := gjson.GetBytes(out, "tools")
	if got := len(tools.Array()); got != 2 {
		t.Fatalf("tools count = %d, want 2 (user bash + stub read)", got)
	}
	first := gjson.GetBytes(out, "tools.0")
	if first.Get("function.description").String() != "user bash" {
		t.Errorf("user bash overwritten: %s", first.Raw)
	}
	if !first.Get("function.parameters.properties.cmd").Exists() {
		t.Errorf("user bash schema changed: %s", first.Raw)
	}
	if gjson.GetBytes(out, "tools.1.function.name").String() != "read" {
		t.Errorf("read stub should be appended after user bash: %s", tools.Raw)
	}
}

func TestApplyZenFreePayloadEnforcementBothPresent(t *testing.T) {
	before := []byte(`{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"edit"}}]}`)
	out := ApplyZenFreePayloadEnforcement(before)
	if got := len(gjson.GetBytes(out, "tools").Array()); got != 3 {
		t.Fatalf("tools count = %d, want 3 unchanged", got)
	}
}

func TestApplyZenFreePayloadEnforcementNeverDuplicates(t *testing.T) {
	once := ApplyZenFreePayloadEnforcement([]byte(`{"model":"m"}`))
	twice := ApplyZenFreePayloadEnforcement(once)
	if got := len(gjson.GetBytes(twice, "tools").Array()); got != 2 {
		t.Fatalf("second pass duplicated stubs: tools count = %d", got)
	}
}

func TestApplyZenFreePayloadEnforcementInvalidJSON(t *testing.T) {
	broken := []byte(`{not json`)
	if got := ApplyZenFreePayloadEnforcement(broken); string(got) != string(broken) {
		t.Errorf("invalid JSON must pass through untouched, got %s", got)
	}
}
