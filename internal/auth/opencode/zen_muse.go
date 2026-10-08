package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// zenMuseModelPrefix names the OpenCode Zen "muse" contributor family.
	// Detection is by id prefix on purpose (aligned with IsZenJevModelID) so
	// new muse generations are picked up without code changes.
	zenMuseModelPrefix = "muse-"
	// zenMuseContributorMarker flags the community-contributed muse weights.
	// The anonymous free tier serves them and requires BOTH the muse prefix
	// and this marker; a paid muse id without it must never take the
	// contributor-free /responses branch.
	zenMuseContributorMarker = "contributor-free"

	// ZenMuseResponsesPath joins the OpenAI Responses endpoint against a zen
	// base URL (the same root the anonymous chat tier uses). The muse
	// contributor family speaks /v1/responses, not /chat/completions.
	ZenMuseResponsesPath = "/responses"
)

// IsZenMuseContributorModelID reports whether a model id belongs to the
// OpenCode Zen "muse" contributor family served on the anonymous free tier.
// Both the muse prefix and the contributor-free marker are mandatory so paid
// muse ids and the other free model families (chat *-free, jev-*) keep their
// existing, mutually exclusive paths.
func IsZenMuseContributorModelID(id string) bool {
	lower := strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(lower, zenMuseModelPrefix) && strings.Contains(lower, zenMuseContributorMarker)
}

// ZenMuseContractExample is reused verbatim in every 400 body so clients
// pointed at the wrong protocol get a copy-pastable request shape instead of
// a bare refusal.
const ZenMuseContractExample = `muse contributor models on the anonymous OpenCode Zen tier only speak the OpenAI Responses protocol. Call POST /v1/responses (not /v1/chat/completions).
Example:
{"model":"muse-spark-1.3-contributor-free","input":"hello","stream":true}`

// ApplyZenMuseResponsesEnforcement forces the business-payload shape the
// anonymous zen tier requires on the /responses path: stream:true plus the
// bash/read function tools the opencode CLI always ships. It parallels
// ApplyZenFreePayloadEnforcement but the stubs follow the Responses form —
// top-level {"type":"function","name":...} without the chat nested
// "function" wrapper — because the upstream payload already IS a Responses
// request on this path. Existing tools named bash/read are preserved in
// either spelling (Responses top-level name or chat nested function.name):
// the tools array grows, never replaces.
func ApplyZenMuseResponsesEnforcement(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "stream", true)
	if tools := gjson.GetBytes(payload, "tools"); !tools.Exists() || !tools.IsArray() {
		payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[]`))
	}
	payload = ensureZenMuseStubTool(payload, "bash", zenMuseStubBash)
	payload = ensureZenMuseStubTool(payload, "read", zenMuseStubRead)
	return payload
}

const (
	zenMuseStubBash = `{"type":"function","name":"bash","description":"run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string","description":"the shell command to run"}},"required":["command"],"additionalProperties":false}}`
	zenMuseStubRead = `{"type":"function","name":"read","description":"read a file from disk","parameters":{"type":"object","properties":{"path":{"type":"string","description":"the file path to read"}},"required":["path"],"additionalProperties":false}}`
)

// HasZenMuseResponsesFunctionTool reports whether payload.tools declares a
// Responses-shaped function tool under the given name (a top-level
// {"type":"function","name":...} entry). Unlike the chat detection the type
// discipline is enforced; a chat-shaped entry (only function.name set, no
// top-level name) does NOT count for the Responses stub decision, so the
// strict Responses injection logic can rely on this alone.
func HasZenMuseResponsesFunctionTool(payload []byte, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "function") &&
			strings.EqualFold(strings.TrimSpace(tool.Get("name").String()), name) {
			return true
		}
	}
	return false
}

func ensureZenMuseStubTool(payload []byte, name, stub string) []byte {
	// The broad name check (any spelling, same rule as the chat free tier)
	// decides whether an explicit user tool exists, and only the
	// Responses-shaped stub is ever appended. A chat-shaped entry therefore
	// blocks injection instead of being shadowed, which keeps transported
	// chat tools visible upstream exactly as authored.
	if HasZenFreeFunctionTool(payload, name) {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, "tools.-1", []byte(stub))
	if err != nil {
		return payload
	}
	return updated
}

// ParseZenMuseResponsesUsage reads input/output token counters from
// Responses usage shapes: a top-level usage object (the aggregated terminal
// response JSON) or the usage nested under .response (a response.completed
// event frame).
func ParseZenMuseResponsesUsage(body []byte) (inputTokens, outputTokens int64, ok bool) {
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		usage = gjson.GetBytes(body, "response.usage")
	}
	if !usage.IsObject() {
		return 0, 0, false
	}
	if !usage.Get("input_tokens").Exists() && !usage.Get("output_tokens").Exists() {
		return 0, 0, false
	}
	inputTokens = usage.Get("input_tokens").Int()
	outputTokens = usage.Get("output_tokens").Int()
	return inputTokens, outputTokens, true
}

// AggregateZenMuseResponsesStream scans the upstream /responses SSE once.
// When rawLines is non-nil every raw wire line is forwarded byte-identical
// (the stream view the client replays), and the final Responses object is
// assembled as soon as a response.completed (or terminal response.incomplete
// / response.failed) event arrives. The returned body is nil while the
// stream never reaches a terminal event; [DONE] alone is not terminal and a
// truncated stream is an error.
func AggregateZenMuseResponsesStream(body io.Reader, rawLines *bytes.Buffer) ([]byte, error) {
	agg := &zenMuseResponsesAccumulator{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800) // 50MB: same cap as the chat SSE scanner
	event := ""
	var dataLines [][]byte
	flush := func() error {
		err := agg.flush(event, dataLines)
		event = ""
		dataLines = nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if rawLines != nil {
			rawLines.Write(line)
			rawLines.WriteByte('\n')
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("event:")) {
			event = strings.TrimSpace(string(trimmed[len("event:"):]))
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			dataLines = append(dataLines, bytes.Clone(bytes.TrimSpace(trimmed[len("data:"):])))
			continue
		}
		// Comments, id:, retry: and plain garbage carry no semantics for the
		// aggregate; the raw replay already forwards them unchanged.
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("zen muse responses stream: read stream: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if !agg.completed {
		return nil, fmt.Errorf("zen muse responses stream: upstream closed before response.completed")
	}
	return agg.final, nil
}

// zenMuseResponsesAccumulator folds the SSE frames of a /responses stream
// into the terminal response.completed JSON.
type zenMuseResponsesAccumulator struct {
	final     []byte
	completed bool
}

func (a *zenMuseResponsesAccumulator) flush(event string, dataLines [][]byte) error {
	if a.completed || len(dataLines) == 0 {
		return nil
	}
	data := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	if !json.Valid(data) {
		return fmt.Errorf("zen muse responses stream: invalid JSON frame (%d bytes)", len(data))
	}
	// The event field only classifies frames whose JSON carries no type,
	// which the zen upstream always emits; nothing is ever rewritten.
	payloadEvent := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "type").String()))
	if payloadEvent == "" {
		payloadEvent = strings.ToLower(event)
	}
	switch payloadEvent {
	case "response.completed":
		final := gjson.GetBytes(data, "response")
		if !final.IsObject() {
			return fmt.Errorf("zen muse responses stream: response.completed carried no response object")
		}
		a.final = bytes.Clone([]byte(final.Raw))
		a.completed = true
	case "response.incomplete", "response.failed":
		// The upstream surfaces a terminal object under these statuses when
		// the request is truncated or refused mid-stream. Hand the whole event
		// envelope to the client; it is still a valid terminal Responses body.
		a.final = bytes.Clone(data)
		a.completed = true
	}
	return nil
}
