package opencode

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsZenMuseContributorModelID(t *testing.T) {
	for _, hit := range []string{
		"muse-spark-1.3-contributor-free",
		"MUSE-SPARK-2.0-Contributor-FREE",
		"  muse-anything-contributor-free  ",
	} {
		if !IsZenMuseContributorModelID(hit) {
			t.Fatalf("%q must select the muse contributor-free family", hit)
		}
	}
	for _, miss := range []string{
		"",
		"muse-spark-1.3",         // paid muse id, no contributor marker
		"muse-spark-1.3-free",    // chat-style free suffix, no marker
		"space-contributor-free", // marker without muse prefix
		"fledge-alpha-free",
		"jev-1.13-free",
		"musex-spark-contributor-free", // prefix must be exactly "muse-"
	} {
		if IsZenMuseContributorModelID(miss) {
			t.Fatalf("%q must NOT select the muse contributor-free family", miss)
		}
	}
}

func museToolNames(payload []byte) (names []string) {
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if name := tool.Get("name").String(); name != "" {
			names = append(names, "flat:"+name)
			return true
		}
		if name := tool.Get("function.name").String(); name != "" {
			names = append(names, "nested:"+name)
		}
		return true
	})
	return names
}

func museToolType(payload []byte, flatName string) string {
	var typ string
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if strings.EqualFold(tool.Get("name").String(), flatName) {
			typ = tool.Get("type").String()
			return false
		}
		return true
	})
	return typ
}

func TestApplyZenMuseResponsesEnforcement_InjectsStreamAndStubs(t *testing.T) {
	out := ApplyZenMuseResponsesEnforcement([]byte(`{"model":"muse-spark-1.3-contributor-free","input":"hi"}`))
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream must be forced true: %s", out)
	}
	names := museToolNames(out)
	if len(names) != 2 || names[0] != "flat:bash" || names[1] != "flat:read" {
		t.Fatalf("tools = %v, want [flat:bash flat:read]", names)
	}
	if typ := museToolType(out, "bash"); typ != "function" {
		t.Fatalf("bash stub type = %q, want responses-shaped function", typ)
	}
	// Responses stubs carry their schema at the top level, never nested.
	if gjson.GetBytes(out, "tools.0.function").Exists() {
		t.Fatalf("responses stub must not use the chat nested function wrapper: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
	if !gjson.GetBytes(out, "tools.0.parameters").Exists() {
		t.Fatalf("responses stub must carry top-level parameters: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
}

func TestApplyZenMuseResponsesEnforcement_KeepsExistingResponsesTool(t *testing.T) {
	payload := []byte(`{"model":"muse-spark-1.3-contributor-free","input":"hi","tools":[{"type":"function","name":"bash","description":"my bash","parameters":{"type":"object","properties":{}}}]}`)
	out := ApplyZenMuseResponsesEnforcement(payload)
	names := museToolNames(out)
	if len(names) != 2 || names[0] != "flat:bash" || names[1] != "flat:read" {
		t.Fatalf("tools = %v, want user bash kept then read stub appended", names)
	}
	if desc := gjson.GetBytes(out, "tools.0.description").String(); desc != "my bash" {
		t.Fatalf("user bash schema must be preserved, got description %q", desc)
	}
}

func TestApplyZenMuseResponsesEnforcement_ChatShapedToolBlocksStub(t *testing.T) {
	// A chat-shaped {"type":"function","function":{"name":"bash",...}} entry is
	// an explicit user tool with that name: the array must grow (read stub
	// only) but the entry itself must pass through unshadowed.
	payload := []byte(`{"model":"muse-spark-1.3-contributor-free","input":"hi","tools":[{"type":"function","function":{"name":"bash","description":"chat bash","parameters":{"type":"object"}}}]}`)
	out := ApplyZenMuseResponsesEnforcement(payload)
	names := museToolNames(out)
	if len(names) != 2 || names[0] != "nested:bash" || names[1] != "flat:read" {
		t.Fatalf("tools = %v, want chat bash kept then read stub appended", names)
	}
	if HasZenMuseResponsesFunctionTool(out, "bash") {
		t.Fatal("chat-shaped bash must not satisfy the strict Responses detection")
	}
	if !HasZenMuseResponsesFunctionTool(out, "read") {
		t.Fatal("injected read stub must satisfy the strict Responses detection")
	}
}

func TestApplyZenMuseResponsesEnforcement_InvalidJSON(t *testing.T) {
	in := []byte(`not json`)
	if out := ApplyZenMuseResponsesEnforcement(in); !bytes.Equal(out, in) {
		t.Fatalf("invalid JSON must pass through untouched")
	}
}

func TestHasZenMuseResponsesFunctionTool(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"function","name":"bash"},{"type":"function","function":{"name":"read"}},{"type":"web_search_preview"}]}`)
	if !HasZenMuseResponsesFunctionTool(payload, "BASH") {
		t.Fatal("responses-shaped bash must match case-insensitively")
	}
	if HasZenMuseResponsesFunctionTool(payload, "read") {
		t.Fatal("chat-shaped read must not match the strict Responses detection")
	}
	if HasZenMuseResponsesFunctionTool(payload, "web_search_preview") {
		t.Fatal("non-function tool type must not match")
	}
	if HasZenMuseResponsesFunctionTool(payload, "") {
		t.Fatal("empty name must not match")
	}
}

func TestParseZenMuseResponsesUsage(t *testing.T) {
	in, out, ok := ParseZenMuseResponsesUsage([]byte(`{"object":"response","usage":{"input_tokens":12,"output_tokens":34}}`))
	if !ok || in != 12 || out != 34 {
		t.Fatalf("top-level usage = (%d,%d,%v)", in, out, ok)
	}
	in, out, ok = ParseZenMuseResponsesUsage([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":9}}}`))
	if !ok || in != 7 || out != 9 {
		t.Fatalf("nested usage = (%d,%d,%v)", in, out, ok)
	}
	if _, _, ok = ParseZenMuseResponsesUsage([]byte(`{"object":"response"}`)); ok {
		t.Fatal("missing usage must report ok=false")
	}
	if _, _, ok = ParseZenMuseResponsesUsage([]byte(`{"usage":{}}`)); ok {
		t.Fatal("usage without token counters must report ok=false")
	}
}

const zenMuseFakeStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"muse-spark-1.3-contributor-free","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

data: {"type":"response.output_text.delta","delta":"lo"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"muse-spark-1.3-contributor-free","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}

data: [DONE]

`

func TestAggregateZenMuseResponsesStream_AssemblesCompleted(t *testing.T) {
	var raw bytes.Buffer
	final, err := AggregateZenMuseResponsesStream(bytes.NewReader([]byte(zenMuseFakeStream)), &raw)
	if err != nil {
		t.Fatalf("aggregate error: %v", err)
	}
	if got := gjson.GetBytes(final, "id").String(); got != "resp_1" {
		t.Fatalf("final id = %q", got)
	}
	if got := gjson.GetBytes(final, "status").String(); got != "completed" {
		t.Fatalf("final status = %q", got)
	}
	if got := gjson.GetBytes(final, "output.0.content.0.text").String(); got != "Hello" {
		t.Fatalf("final text = %q", got)
	}
	if in, out, ok := ParseZenMuseResponsesUsage(final); !ok || in != 5 || out != 2 {
		t.Fatalf("final usage = (%d,%d,%v)", in, out, ok)
	}
	// Byte-identical replay for the stream view.
	if raw.String() != zenMuseFakeStream {
		t.Fatalf("raw replay must forward every line byte-identical:\n%q", raw.String())
	}
}

func TestAggregateZenMuseResponsesStream_IncompleteAndFailedAreTerminal(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"incomplete", `data: {"type":"response.incomplete","response":{"id":"resp_x","status":"incomplete"}}` + "\n\n"},
		{"failed", `data: {"type":"response.failed","response":{"id":"resp_y","status":"failed","error":{"code":"x","message":"y"}}}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final, err := AggregateZenMuseResponsesStream(strings.NewReader(tc.body), nil)
			if err != nil {
				t.Fatalf("aggregate error: %v", err)
			}
			if got := gjson.GetBytes(final, "response.id").String(); got == "" {
				t.Fatalf("terminal envelope must be returned as a whole: %s", final)
			}
		})
	}
}

func TestAggregateZenMuseResponsesStream_Errors(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no terminal event", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"},
		{"done is not terminal", "data: [DONE]\n\n"},
		{"invalid json frame", "data: {oops\n\n"},
		{"completed without response", "data: {\"type\":\"response.completed\"}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AggregateZenMuseResponsesStream(strings.NewReader(tc.body), nil); err == nil {
				t.Fatal("expected an aggregation error")
			}
		})
	}
}
