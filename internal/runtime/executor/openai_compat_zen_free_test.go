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

const zenFreeFakeStream = `data: {"id":"chatcmpl-zen-1","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"chatcmpl-zen-1","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-zen-1","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"delta":{"content":" from zen"}}]}

data: {"id":"chatcmpl-zen-1","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}

data: [DONE]

`

type zenFreeUpstreamCapture struct {
	body        []byte
	userAgent   string
	client      string
	requestID   string
	sessionID   string
	contentType string
	accept      string
}

func newZenFreeUpstream(t *testing.T, capture *zenFreeUpstreamCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.body = body
		capture.userAgent = r.Header.Get("User-Agent")
		capture.client = r.Header.Get("X-Opencode-Client")
		capture.requestID = r.Header.Get("X-Opencode-Request")
		capture.sessionID = r.Header.Get("X-Opencode-Session")
		capture.contentType = r.Header.Get("Content-Type")
		capture.accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(zenFreeFakeStream))
	}))
}

func newZenFreeAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url":        baseURL + "/zen/v1",
			"api_key":         "public",
			"header:X-Custom": "custom-value",
		},
	}
}

func TestOpenAICompatExecutor_ZenFreeNonStreamAggregates(t *testing.T) {
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenFreeAuth(server.URL)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "fledge-alpha-free",
		Payload: []byte(`{"model":"fledge-alpha-free","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Fingerprint headers reached the upstream and won over the compat default.
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

	// Payload configuration: forced stream:true and bash/read stubs injected.
	if !gjson.GetBytes(capture.body, "stream").Bool() {
		t.Errorf("upstream body stream = %s, want true; body=%s", gjson.GetBytes(capture.body, "stream").Raw, capture.body)
	}
	if !opencode.HasZenFreeFunctionTool(capture.body, "bash") {
		t.Errorf("bash stub missing in upstream body: %s", gjson.GetBytes(capture.body, "tools").Raw)
	}
	if !opencode.HasZenFreeFunctionTool(capture.body, "read") {
		t.Errorf("read stub missing in upstream body: %s", gjson.GetBytes(capture.body, "tools").Raw)
	}

	// Client asked for a non-stream response; the SSE was folded into one
	// chat.completion carrying the concatenated content and the usage.
	root := gjson.ParseBytes(resp.Payload)
	if got := root.Get("object").String(); got != "chat.completion" {
		t.Errorf("response object = %q, want chat.completion", got)
	}
	if got := root.Get("choices.0.message.content").String(); got != "Hello from zen" {
		t.Errorf("aggregated content = %q", got)
	}
	if got := root.Get("choices.0.finish_reason").String(); got != "stop" {
		t.Errorf("finish_reason = %q, want stop", got)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 14 {
		t.Errorf("usage.total_tokens = %d, want 14", got)
	}
}

func TestOpenAICompatExecutor_ZenFreePreservesUserBash(t *testing.T) {
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenFreeAuth(server.URL)

	payload := []byte(`{"model":"fledge-alpha-free","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"bash","description":"user bash","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "fledge-alpha-free",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	tools := gjson.GetBytes(capture.body, "tools")
	if got := len(tools.Array()); got != 2 {
		t.Fatalf("tools count = %d, want 2 (user bash + stub read); tools=%s", got, tools.Raw)
	}
	first := gjson.GetBytes(capture.body, "tools.0")
	if got := first.Get("function.description").String(); got != "user bash" {
		t.Errorf("existing bash tool was overwritten: %s", first.Raw)
	}
}

func TestOpenAICompatExecutor_ZenFreeStreamKeepsFingerprint(t *testing.T) {
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenFreeAuth(server.URL)

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "fledge-alpha-free",
		Payload: []byte(`{"model":"fledge-alpha-free","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	// Drain the channel so the goroutine completes.
	for range stream.Chunks {
	}

	if capture.userAgent != opencode.ZenFreeUserAgent {
		t.Errorf("stream User-Agent = %q, want %q", capture.userAgent, opencode.ZenFreeUserAgent)
	}
	if !strings.HasPrefix(capture.requestID, "msg_") {
		t.Errorf("stream X-Opencode-Request = %q, want msg_ fingerprint", capture.requestID)
	}
	if !opencode.HasZenFreeFunctionTool(capture.body, "bash") {
		t.Errorf("stream body missing bash stub: %s", gjson.GetBytes(capture.body, "tools").Raw)
	}
	if !gjson.GetBytes(capture.body, "stream").Bool() {
		t.Errorf("stream body must keep stream:true; body=%s", capture.body)
	}
}

func TestOpenAICompatExecutor_PaidOpenAICompatUntouched(t *testing.T) {
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL + "/zen/go/v1",
			"api_key":  "sk-paid-key",
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1",
		Payload: []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	// Paid keys must not aggregate the SSE into a completion: the upstream
	// receives the plain compat identity and the raw SSE is handed back to
	// the translator (which yields an unusable body on this fake stream).
	_ = err
	if capture.userAgent != "cli-proxy-openai-compat" {
		t.Errorf("paid key User-Agent = %q, want cli-proxy-openai-compat", capture.userAgent)
	}
	if capture.client != "" {
		t.Errorf("paid key must not send X-Opencode-Client, got %q", capture.client)
	}
	if capture.requestID != "" || capture.sessionID != "" {
		t.Errorf("paid key must not send fingerprint headers, got req=%q ses=%q", capture.requestID, capture.sessionID)
	}
	if gjson.GetBytes(capture.body, "stream").Exists() && gjson.GetBytes(capture.body, "stream").Bool() {
		t.Errorf("paid key body stream forced true: %s", capture.body)
	}
	tools := gjson.GetBytes(capture.body, "tools")
	if tools.Exists() && len(tools.Array()) > 0 {
		t.Errorf("paid key must not receive tool stubs: %s", tools.Raw)
	}
}
