package modelprobe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const relayAntiProbeText = `this request asks for at most one token of output, so it is read as an availability probe rather than work. Use GET /v1/limits to check availability; it costs no upstream call and is not rate limited`

func TestMirasimProbePayloadIsWorkShaped(t *testing.T) {
	payload := buildProbePayloadForProvider("mirasim", "kimi-k3")
	var body struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload invalid: %v", err)
	}
	if body.MaxTokens < 8 {
		t.Fatalf("mirasim max_tokens = %d, want >= 8 (work shape)", body.MaxTokens)
	}
	if body.MaxTokens != mirasimProbeMaxTokens {
		t.Fatalf("mirasim max_tokens = %d, want %d", body.MaxTokens, mirasimProbeMaxTokens)
	}
	payloadOther := buildProbePayloadForProvider("cursor", "composer-2")
	var other struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(payloadOther, &other)
	if other.MaxTokens != 1 {
		t.Fatalf("default probe budget must stay 1 token, got %d", other.MaxTokens)
	}
}

func TestClassifyMirasimRelayTexts(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Status
	}{
		{"anti probe shape", relayStatusError{code: 400, body: relayAntiProbeText}, StatusUnknown},
		{"deprecated id zh", relayStatusError{code: 429, body: `这个账号暂时不能使用 deepseek-v4-flash，请换用其他模型`}, StatusNotAvailable},
		{"deprecated id en", relayStatusError{code: 429, body: `not available to this account right now; switch models.`}, StatusNotAvailable},
		{"deprecated id zh alt", relayStatusError{code: 400, body: `暂时不可用，请换用其他模型`}, StatusNotAvailable},
		{"zen capacity stays busy", relayStatusError{code: 503, body: `Model is unavailable`}, StatusLimited},
		{"legacy not supported", relayStatusError{code: 400, body: `model "claude-3-5-haiku-20241022" is not supported at this time`}, StatusNotAvailable},
		{"account 403 stays auth", relayStatusError{code: 403, body: `forbidden`, padLength: 64}, StatusAuthError},
		{"403 with not-available text", relayStatusError{code: 403, body: `model not enabled for tier`}, StatusNotAvailable},
	}
	for _, tc := range cases {
		if got := classifyProbeError(tc.err); got != tc.want {
			t.Fatalf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type relayStatusError struct {
	code      int
	body      string
	padLength int
}

func (e relayStatusError) Error() string {
	if e.padLength > len(e.body) {
		return e.body + strings.Repeat(".", e.padLength-len(e.body))
	}
	return e.body
}
func (e relayStatusError) StatusCode() int { return e.code }

// Outcome matrix integration: an unreadable probe must not record any row.
func TestSynthesizeSectionDropsUnknownOutcomes(t *testing.T) {
	checked := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	section := synthesizeSection(checked, map[string]ModelOutcome{
		"kimi-k3":        {Status: StatusUnknown, Error: relayAntiProbeText},
		"glm-5.3-flash":  {Status: StatusUsable},
		"deepseek-flash": {Status: StatusUnknown},
		"dead-legacy":    {Status: StatusNotAvailable},
	})
	if _, any := section.PerModel["kimi-k3"]; any {
		t.Fatalf("unknown rows must not land in PerModel: %+v", section.PerModel)
	}
	if _, any := section.PerModel["deepseek-flash"]; any {
		t.Fatalf("unknown rows must not land in PerModel: %+v", section.PerModel)
	}
	if len(section.Usable) != 1 || section.Usable[0] != "glm-5.3-flash" {
		t.Fatalf("usable = %+v", section.Usable)
	}
	if len(section.Pruned) != 1 || section.Pruned[0] != "dead-legacy" {
		t.Fatalf("pruned = %+v", section.Pruned)
	}
}

// Scheduler layer: an anti-probe rejection leaves the model unprobed
// (visible, re-evaluated later), never dead.
func TestSchedulerKeepsUnknownModelVisible(t *testing.T) {
	mock := &mockExec{handlers: map[string]error{
		"kimi-k3": errors.New(relayAntiProbeText),
	}}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"cursor": mock},
	})
	auth := &cliproxyauth.Auth{
		ID:       "cursor-mirasim-shape.json",
		Provider: "cursor",
		Metadata: map[string]any{"access_token": "at"},
	}
	scheduler := NewScheduler(engine, &Store{AuthDir: t.TempDir()},
		func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		func(*cliproxyauth.Auth) []string { return []string{"kimi-k3"} },
		SchedulerOptions{},
	)
	if probed := scheduler.probeAuth(context.Background(), auth, 1); !probed {
		t.Fatal("cycle must run the probe")
	}
	section := ReadSection(auth.Metadata)
	if section == nil {
		t.Fatal("section must exist after the cycle")
	}
	if _, present := section.PerModel["kimi-k3"]; present {
		t.Fatalf("anti-probe outcome must not record a row: %+v", section.PerModel)
	}
	if len(section.Pruned) != 0 {
		t.Fatalf("nothing pruned by an unreadable probe: %+v", section.Pruned)
	}
	if len(section.Usable) != 0 {
		t.Fatalf("nothing usable either: %+v", section.Usable)
	}
	// The model stays in the candidate pool for the next cycle (unprobed
	// semantics re-probe immediately rather than backing off).
	if probed := scheduler.probeAuth(context.Background(), auth, 2); !probed {
		t.Fatal("the unprobed model remains due next cycle")
	}
}

// The work-shaped budget actually reaches the wire for mirasim probes.
func TestProbeOneSendsMirasimWorkShape(t *testing.T) {
	mock := &captureMockExec{}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"mirasim": mock},
	})
	auth := &cliproxyauth.Auth{ID: "mirasim-shape.json", Provider: "mirasim", Metadata: map[string]any{"access_token": "at"}}
	outcome := engine.ProbeOne(context.Background(), auth, "mirasim", "kimi-k3")
	if outcome.Status != StatusUsable {
		t.Fatalf("outcome = %+v", outcome)
	}
	if strings.Count(mock.lastPayload, `"max_tokens":16`) != 1 {
		t.Fatalf("mirasim probe payload must carry 16-token budget: %s", mock.lastPayload)
	}
}

type captureMockExec struct {
	lastPayload string
	calls       int
}

func (m *captureMockExec) Execute(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	m.calls++
	m.lastPayload = string(req.Payload)
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)}, nil
}
