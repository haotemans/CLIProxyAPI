package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	opencode "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/opencode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const zenMuseFakeUpstreamStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_muse_1","object":"response","created_at":1791394469,"model":"muse-spark-1.3-contributor-free","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" from muse"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_muse_1","object":"response","created_at":1791394469,"status":"completed","model":"muse-spark-1.3-contributor-free","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello from muse"}]}],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}}

`

type zenMuseUpstreamCapture struct {
	path        string
	body        []byte
	userAgent   string
	client      string
	requestID   string
	sessionID   string
	contentType string
	accept      string
}

func newZenMuseUpstream(t *testing.T, capture *zenMuseUpstreamCapture, streamBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.path = r.URL.Path
		capture.body = body
		capture.userAgent = r.Header.Get("User-Agent")
		capture.client = r.Header.Get("X-Opencode-Client")
		capture.requestID = r.Header.Get("X-Opencode-Request")
		capture.sessionID = r.Header.Get("X-Opencode-Session")
		capture.contentType = r.Header.Get("Content-Type")
		capture.accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamBody))
	}))
}

func newZenMuseAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url":        baseURL + "/zen/v1",
			"api_key":         "public",
			"header:X-Custom": "custom-value",
		},
	}
}

func zenMuseResponsesPayload() []byte {
	return []byte(`{"model":"muse-spark-1.3-contributor-free","input":"hello","tools":[{"type":"function","name":"bash","description":"user bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`)
}

func assertZenMuseUpstreamBasics(t *testing.T, capture zenMuseUpstreamCapture) {
	t.Helper()
	if !strings.HasSuffix(capture.path, "/responses") {
		t.Errorf("upstream path = %q, want .../responses (not /chat/completions)", capture.path)
	}
	if capture.userAgent != opencode.ZenFreeUserAgent {
		t.Errorf("User-Agent = %q, want %q", capture.userAgent, opencode.ZenFreeUserAgent)
	}
	if capture.client != opencode.ZenFreeClientHeader {
		t.Errorf("X-Opencode-Client = %q, want %q", capture.client, opencode.ZenFreeClientHeader)
	}
	if !strings.HasPrefix(capture.requestID, "msg_") || len(capture.requestID) != 30 {
		t.Errorf("X-Opencode-Request = %q, want msg_ fingerprint (len 30)", capture.requestID)
	}
	if !strings.HasPrefix(capture.sessionID, "ses_") || len(capture.sessionID) != 30 {
		t.Errorf("X-Opencode-Session = %q, want ses_ fingerprint (len 30)", capture.sessionID)
	}
	if capture.accept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", capture.accept)
	}
	if capture.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", capture.contentType)
	}
}

func assertZenMuseUpstreamBody(t *testing.T, body []byte) {
	t.Helper()
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Errorf("upstream body stream = %s, want true; body=%s", gjson.GetBytes(body, "stream").Raw, body)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "muse-spark-1.3-contributor-free" {
		t.Errorf("upstream model = %q", got)
	}
	// Responses-shaped stubs only: top-level name, never the chat wrapper.
	if !opencode.HasZenMuseResponsesFunctionTool(body, "bash") {
		t.Errorf("bash responses tool missing in upstream body: %s", gjson.GetBytes(body, "tools").Raw)
	}
	if !opencode.HasZenMuseResponsesFunctionTool(body, "read") {
		t.Errorf("read responses stub missing in upstream body: %s", gjson.GetBytes(body, "tools").Raw)
	}
	if gjson.GetBytes(body, "tools.0.function").Exists() {
		t.Errorf("upstream tools must use the Responses top-level form, got chat nested wrapper: %s", gjson.GetBytes(body, "tools.0").Raw)
	}
	if desc := gjson.GetBytes(body, "tools.0.description").String(); desc != "user bash" {
		t.Errorf("user bash tool must be preserved, got description %q", desc)
	}
	if gjson.GetBytes(body, "messages").Exists() {
		t.Errorf("responses path must never synthesize chat messages: %s", body)
	}
}

func TestOpenAICompatExecutor_ZenMuseNonStreamAggregates(t *testing.T) {
	var capture zenMuseUpstreamCapture
	server := newZenMuseUpstream(t, &capture, zenMuseFakeUpstreamStream)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	resp, err := executor.Execute(context.Background(), newZenMuseAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: zenMuseResponsesPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	assertZenMuseUpstreamBasics(t, capture)
	assertZenMuseUpstreamBody(t, capture.body)

	// The client asked for a non-stream response; the upstream SSE was folded
	// into the terminal Responses object.
	if ct := resp.Headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("response Content-Type = %q, want application/json", ct)
	}
	root := gjson.ParseBytes(resp.Payload)
	if got := root.Get("id").String(); got != "resp_muse_1" {
		t.Errorf("response id = %q", got)
	}
	if got := root.Get("status").String(); got != "completed" {
		t.Errorf("response status = %q", got)
	}
	if got := root.Get("output.0.content.0.text").String(); got != "Hello from muse" {
		t.Errorf("output text = %q", got)
	}
	if got := root.Get("usage.input_tokens").Int(); got != 11 {
		t.Errorf("usage.input_tokens = %d", got)
	}
	if !root.Get("usage.output_tokens_details.reasoning_tokens").Exists() {
		t.Errorf("EnsureResponsesUsageDetails must backfill reasoning_tokens: %s", root.Get("usage").Raw)
	}
}

func TestOpenAICompatExecutor_ZenMuseStreamForwardsEvents(t *testing.T) {
	var capture zenMuseUpstreamCapture
	server := newZenMuseUpstream(t, &capture, zenMuseFakeUpstreamStream)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	stream, err := executor.ExecuteStream(context.Background(), newZenMuseAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: zenMuseResponsesPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var sb strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		sb.Write(chunk.Payload)
		sb.WriteString("\n")
	}
	wire := sb.String()

	assertZenMuseUpstreamBasics(t, capture)
	assertZenMuseUpstreamBody(t, capture.body)

	// The Responses SSE events flow through byte-identical (typed frames are
	// never rewritten), and the channel is closed deterministically by
	// appending [DONE] to the terminal response.completed frame even though
	// the fake upstream never sent one.
	for _, want := range []string{
		"event: response.created",
		`data: {"type":"response.created"`,
		"Hello",
		" from muse",
		"event: response.completed",
		`"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}`,
		"data: [DONE]",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("stream wire missing %q:\n%s", want, wire)
		}
	}
	if idxDone, idxCompleted := strings.Index(wire, "data: [DONE]"), strings.Index(wire, `"type":"response.completed"`); idxDone < idxCompleted {
		t.Errorf("[DONE] must follow the terminal response.completed frame:\n%s", wire)
	}
}

func TestOpenAICompatExecutor_ZenMuseStreamTruncatedFails(t *testing.T) {
	truncated := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cut\"}}\n\n"
	var capture zenMuseUpstreamCapture
	server := newZenMuseUpstream(t, &capture, truncated)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	stream, err := executor.ExecuteStream(context.Background(), newZenMuseAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: zenMuseResponsesPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("a stream without response.completed must surface an error")
	}
	if se, ok := streamErr.(statusErr); !ok || se.code != http.StatusBadGateway {
		t.Fatalf("stream error = %v, want 502 statusErr", streamErr)
	}
}

func TestOpenAICompatExecutor_ZenMuseRejectsChatChannel(t *testing.T) {
	// A chat-format caller (e.g. a new-api chat channel pointed at the muse
	// family by mistake) must get an explicit 400 naming the Responses
	// protocol instead of an opaque upstream failure.
	var capture zenMuseUpstreamCapture
	server := newZenMuseUpstream(t, &capture, zenMuseFakeUpstreamStream)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	chatPayload := []byte(`{"model":"muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), newZenMuseAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: chatPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	})
	if err == nil {
		t.Fatal("chat-format Execute must be refused")
	}
	se, ok := err.(statusErr)
	if !ok || se.code != http.StatusBadRequest {
		t.Fatalf("chat refusal = %v, want 400 statusErr", err)
	}
	if !strings.Contains(se.msg, "Responses") || !strings.Contains(se.msg, opencode.ZenMuseContractExample) {
		t.Fatalf("chat refusal must explain the Responses contract, got: %s", se.msg)
	}
	if len(capture.body) != 0 {
		t.Fatalf("refused request must never reach upstream, got body: %s", capture.body)
	}

	_, err = executor.ExecuteStream(context.Background(), newZenMuseAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: chatPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err == nil {
		t.Fatal("chat-format ExecuteStream must be refused")
	}
	if se, ok = err.(statusErr); !ok || se.code != http.StatusBadRequest {
		t.Fatalf("stream chat refusal = %v, want 400 statusErr", err)
	}
}

func TestOpenAICompatExecutor_ZenMuseDoesNotClaimOtherPaths(t *testing.T) {
	// Exclusivity guard: a PAID opencode credential (not the literal "public"
	// key) must keep the unchanged paid chat path for the same model id, and a
	// non-muse free chat model must keep the unchanged chat free path — only
	// muse contributor ids + free credentials engage the /responses split.
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	paidAuth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL + "/zen/v1",
			"api_key":  "sk-real-paid-key",
		},
	}
	if _, err := executor.Execute(context.Background(), paidAuth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor-free",
		Payload: []byte(`{"model":"muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("paid chat path error: %v", err)
	}
	if gjson.GetBytes(capture.body, "messages").Exists() == false {
		t.Fatalf("paid path must forward the chat payload to /chat/completions untouched, got: %s", capture.body)
	}
	if gjson.GetBytes(capture.body, "tools").Exists() {
		t.Fatalf("paid path must not receive zen free tool stubs: %s", capture.body)
	}
}
