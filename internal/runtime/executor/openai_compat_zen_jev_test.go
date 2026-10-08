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

const zenJevFakeResponse = `{"model":"jev-1.13-free","answers":{"alive":{"type":"noul","noul":0.75},"quality":{"type":"score","score":0.99,"confidence":0.98,"legend":{"0":"incoherent","1":"coherent"},"probabilities":{"0":0.01,"1":0.99}}},"usage":{"input_tokens":310,"output_tokens":33},"cost":"0"}`

type zenJevUpstreamCapture struct {
	path        string
	body        []byte
	userAgent   string
	client      string
	requestID   string
	sessionID   string
	contentType string
	accept      string
}

func newZenJevUpstream(t *testing.T, capture *zenJevUpstreamCapture) *httptest.Server {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(zenJevFakeResponse))
	}))
}

func newZenJevAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": baseURL + "/zen/v1",
			"api_key":  "public",
		},
	}
}

func zenJevChatPayload(t *testing.T) []byte {
	t.Helper()
	questions := `{\"alive\":{\"type\":\"noul\",\"question\":\"Is it alive?\",\"instructions\":\"Answer yes when reachable.\"},\"quality\":{\"type\":\"score\",\"question\":\"Rate coherence.\",\"criteria\":[\"incoherent\",\"coherent\"]}}`
	return []byte(`{"model":"jev-1.13-free","messages":[{"role":"system","content":"{\"jev_questions\":` + questions + `}"},{"role":"user","content":"ping"},{"role":"assistant","content":"pong"}]}`)
}

func TestOpenAICompatExecutor_ZenJevExecuteFullFlow(t *testing.T) {
	var capture zenJevUpstreamCapture
	server := newZenJevUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenJevAuth(server.URL)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-1.13-free",
		Payload: zenJevChatPayload(t),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// The jev family reroutes to /systemone on the same base URL.
	if !strings.HasSuffix(capture.path, "/zen/v1/systemone") {
		t.Errorf("upstream path = %q, want .../systemone", capture.path)
	}

	// Free-tier fingerprint headers ride along, like the chat tier.
	if capture.userAgent != opencode.ZenFreeUserAgent {
		t.Errorf("User-Agent = %q, want %q", capture.userAgent, opencode.ZenFreeUserAgent)
	}
	if capture.client != opencode.ZenFreeClientHeader {
		t.Errorf("X-Opencode-Client = %q, want %q", capture.client, opencode.ZenFreeClientHeader)
	}
	if !strings.HasPrefix(capture.requestID, "msg_") || len(capture.requestID) != 30 {
		t.Errorf("X-Opencode-Request = %q, want msg_ fingerprint", capture.requestID)
	}
	if !strings.HasPrefix(capture.sessionID, "ses_") || len(capture.sessionID) != 30 {
		t.Errorf("X-Opencode-Session = %q, want ses_ fingerprint", capture.sessionID)
	}
	if capture.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", capture.contentType)
	}

	// systemone never sees stream:true, tools, or an SSE Accept.
	if capture.accept == "text/event-stream" {
		t.Errorf("Accept = %q, want non-SSE", capture.accept)
	}
	if gjson.GetBytes(capture.body, "stream").Exists() {
		t.Errorf("systemone body must not carry stream: %s", capture.body)
	}
	if gjson.GetBytes(capture.body, "tools").Exists() {
		t.Errorf("systemone body must not carry tools: %s", capture.body)
	}

	// Body is the validated state/questions protocol.
	if got := gjson.GetBytes(capture.body, "model").String(); got != "jev-1.13-free" {
		t.Errorf("upstream model = %q", got)
	}
	if got := gjson.GetBytes(capture.body, "state").String(); got != "user: ping\nassistant: pong" {
		t.Errorf("upstream state = %q", got)
	}
	if got := gjson.GetBytes(capture.body, "questions.alive.type").String(); got != "noul" {
		t.Errorf("questions.alive.type = %q", got)
	}
	if got := gjson.GetBytes(capture.body, "questions.quality.criteria.1").String(); got != "coherent" {
		t.Errorf("questions.quality.criteria.1 = %q", got)
	}

	// The response is a standard chat.completion with mapped usage.
	root := gjson.ParseBytes(resp.Payload)
	if got := root.Get("object").String(); got != "chat.completion" {
		t.Errorf("response object = %q", got)
	}
	if got := root.Get("model").String(); got != "jev-1.13-free" {
		t.Errorf("response model = %q", got)
	}
	if got := root.Get("choices.0.finish_reason").String(); got != "stop" {
		t.Errorf("finish_reason = %q", got)
	}
	content := root.Get("choices.0.message.content").String()
	if !gjson.Valid(content) {
		t.Errorf("assistant content must be the structured answers JSON: %s", content)
	}
	if got := gjson.Get(content, "alive.noul").Float(); got != 0.75 {
		t.Errorf("answers alive.noul = %v", got)
	}
	if got := root.Get("usage.prompt_tokens").Int(); got != 310 {
		t.Errorf("usage.prompt_tokens = %d, want 310", got)
	}
	if got := root.Get("usage.completion_tokens").Int(); got != 33 {
		t.Errorf("usage.completion_tokens = %d, want 33", got)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 343 {
		t.Errorf("usage.total_tokens = %d, want 343", got)
	}
}

func TestOpenAICompatExecutor_ZenJevStreamSequence(t *testing.T) {
	var capture zenJevUpstreamCapture
	server := newZenJevUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenJevAuth(server.URL)

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-1.13-free",
		Payload: zenJevChatPayload(t),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks []string
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}

	// Upstream received exactly one annotated systemone call.
	if !strings.HasSuffix(capture.path, "/zen/v1/systemone") {
		t.Errorf("upstream path = %q, want .../systemone", capture.path)
	}
	if capture.userAgent != opencode.ZenFreeUserAgent {
		t.Errorf("stream User-Agent = %q", capture.userAgent)
	}

	if len(chunks) == 0 {
		t.Fatal("no chunks emitted")
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "chat.completion.chunk") {
		t.Errorf("stream must carry chat.completion.chunk frames: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"stop"`) {
		t.Errorf("stream must carry a stop finish_reason: %s", joined)
	}
	if !strings.Contains(joined, `"prompt_tokens":310`) {
		t.Errorf("stream must carry mapped usage: %s", joined)
	}
	// OpenAI passthrough strips the data: prefix; the [DONE] marker terminates
	// the translator param and yields no chunk, so the wire [DONE] is asserted
	// on the helper level instead.
	if got := gjson.Get(chunks[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Errorf("first chunk delta.role = %q, want assistant; chunk=%s", got, chunks[0])
	}
}

func TestOpenAICompatExecutor_ZenJevMissingQuestions400(t *testing.T) {
	var capture zenJevUpstreamCapture
	server := newZenJevUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenJevAuth(server.URL)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-1.13-free",
		Payload: []byte(`{"model":"jev-1.13-free","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("want 400 for missing jev_questions")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("want 400 status error, got %v", err)
	}
	if !strings.Contains(err.Error(), "jev_questions") {
		t.Errorf("error must explain the jev_questions contract: %v", err)
	}
	// The upstream must never be called on a contract violation.
	if len(capture.body) != 0 {
		t.Errorf("upstream must not receive a request on 400, got %s", capture.body)
	}
}

func TestOpenAICompatExecutor_ZenJevUnknownType400(t *testing.T) {
	var capture zenJevUpstreamCapture
	server := newZenJevUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := newZenJevAuth(server.URL)

	payload := []byte(`{"model":"jev-1.13-free","messages":[{"role":"system","content":"{\"jev_questions\":{\"q\":{\"type\":\"rank\",\"question\":\"q?\"}}}"},{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-1.13-free",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("want 400 for unknown question type")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("want 400 status error, got %v", err)
	}
	if len(capture.body) != 0 {
		t.Errorf("upstream must not receive a request on 400, got %s", capture.body)
	}
}

// Paid keys keep the plain chat path even when the model id is jev-shaped.
func TestOpenAICompatExecutor_ZenJevPaidKeyKeepsChatPath(t *testing.T) {
	var capture zenFreeUpstreamCapture
	server := newZenFreeUpstream(t, &capture)
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL + "/zen/v1",
			"api_key":  "sk-paid-jev",
		},
	}

	_, _ = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-1.13-free",
		Payload: []byte(`{"model":"jev-1.13-free","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})

	// Paid keys hit the chat completions handler (the zenFreeUpstream fake
	// only serves SSE there) and keep the plain compat identity.
	if capture.userAgent != "cli-proxy-openai-compat" {
		t.Errorf("paid key User-Agent = %q, want cli-proxy-openai-compat", capture.userAgent)
	}
	if capture.client != "" || capture.requestID != "" || capture.sessionID != "" {
		t.Errorf("paid key must not send fingerprint headers, got client=%q req=%q ses=%q", capture.client, capture.requestID, capture.sessionID)
	}
}
