package executor

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor/proto"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newCursorTestTokens(access, refresh string) map[string]any {
	return map[string]any{
		"type":          "cursor",
		"access_token":  access,
		"refresh_token": refresh,
	}
}

func TestCursorAccessTokenHelpers(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: newCursorTestTokens("at-1", "rt-1")}
	if got := cursorAccessToken(auth); got != "at-1" {
		t.Fatalf("cursorAccessToken = %q, want at-1", got)
	}
	if got := cursorRefreshToken(auth); got != "rt-1" {
		t.Fatalf("cursorRefreshToken = %q, want rt-1", got)
	}
	if got := cursorAccessToken(nil); got != "" {
		t.Fatalf("cursorAccessToken(nil) = %q", got)
	}
	if got := cursorAccessToken(&cliproxyauth.Auth{}); got != "" {
		t.Fatalf("cursorAccessToken(empty) = %q", got)
	}
}

func TestClassifyCursorError(t *testing.T) {
	if got := classifyCursorError(nil); got != nil {
		t.Fatalf("classifyCursorError(nil) = %v", got)
	}

	plain := errors.New("something odd")
	if got := classifyCursorError(plain); got != plain {
		t.Fatalf("unclassified error should pass through, got %v", got)
	}

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"quota", errors.New("rate limit exceeded"), http.StatusTooManyRequests},
		{"connect_quota", &cursorproto.ConnectError{Code: "resource_exhausted", Message: "quota"}, http.StatusTooManyRequests},
		{"connect_auth", &cursorproto.ConnectError{Code: "unauthenticated", Message: "bad token"}, http.StatusUnauthorized},
		{"connect_perm", &cursorproto.ConnectError{Code: "permission_denied", Message: "nope"}, http.StatusForbidden},
		{"connect_unavailable", &cursorproto.ConnectError{Code: "unavailable", Message: "down"}, http.StatusServiceUnavailable},
		{"connect_internal", &cursorproto.ConnectError{Code: "internal", Message: "boom"}, http.StatusInternalServerError},
		{"connect_unknown", &cursorproto.ConnectError{Code: "whatever", Message: "x"}, http.StatusBadGateway},
		{"h2_rst", errors.New("h2: RST_STREAM code=8"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCursorError(tc.err)
			se, ok := got.(interface{ StatusCode() int })
			if !ok {
				t.Fatalf("classified error lacks StatusCode(): %v", got)
			}
			if se.StatusCode() != tc.want {
				t.Fatalf("StatusCode = %d, want %d", se.StatusCode(), tc.want)
			}
		})
	}
}

func TestParseOpenAIRequestBasic(t *testing.T) {
	payload := []byte(`{
		"model": "composer-2",
		"stream": true,
		"messages": [
			{"role": "system", "content": "sys"},
			{"role": "user", "content": "hello"}
		]
	}`)
	parsed := parseOpenAIRequest(payload)
	if parsed.Model != "composer-2" {
		t.Errorf("Model = %q", parsed.Model)
	}
	if !parsed.Stream {
		t.Error("Stream should be true")
	}
	if parsed.SystemPrompt != "sys" {
		t.Errorf("SystemPrompt = %q", parsed.SystemPrompt)
	}
	if parsed.UserText != "hello" {
		t.Errorf("UserText = %q", parsed.UserText)
	}
	if len(parsed.Turns) != 0 {
		t.Errorf("Turns = %d, want 0", len(parsed.Turns))
	}
}

func TestParseOpenAIRequestMultiTurn(t *testing.T) {
	payload := []byte(`{
		"model": "composer-2",
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "second"},
			{"role": "user", "content": "third"}
		]
	}`)
	parsed := parseOpenAIRequest(payload)
	if parsed.UserText != "third" {
		t.Errorf("UserText = %q, want third", parsed.UserText)
	}
	if len(parsed.Turns) != 1 {
		t.Fatalf("Turns = %d, want 1", len(parsed.Turns))
	}
	if parsed.Turns[0].UserText != "first" || parsed.Turns[0].AssistantText != "second" {
		t.Errorf("Turn = %+v", parsed.Turns[0])
	}
	// No system prompt in payload -> default filled in.
	if parsed.SystemPrompt == "" {
		t.Error("SystemPrompt should fall back to a default")
	}
}

func TestParseOpenAIRequestToolResults(t *testing.T) {
	payload := []byte(`{
		"model": "composer-2",
		"messages": [
			{"role": "user", "content": "run the tool"},
			{"role": "assistant", "content": "calling tool"},
			{"role": "tool", "tool_call_id": "call_1", "content": "tool output"}
		]
	}`)
	parsed := parseOpenAIRequest(payload)
	if len(parsed.ToolResults) != 1 {
		t.Fatalf("ToolResults = %d, want 1", len(parsed.ToolResults))
	}
	if parsed.ToolResults[0].ToolCallId != "call_1" || parsed.ToolResults[0].Content != "tool output" {
		t.Errorf("ToolResult = %+v", parsed.ToolResults[0])
	}
}

func TestFlattenConversationIntoUserText(t *testing.T) {
	parsed := &parsedOpenAIRequest{
		UserText: "current question",
		Turns: []cursorproto.TurnData{
			{UserText: "old q", AssistantText: "old a"},
		},
		ToolResults: []toolResultInfo{{ToolCallId: "c1", Content: "result body"}},
	}
	flattenConversationIntoUserText(parsed)
	if len(parsed.Turns) != 0 || len(parsed.ToolResults) != 0 {
		t.Fatal("flatten should clear turns and tool results")
	}
	for _, want := range []string{"USER: old q", "ASSISTANT: old a", "TOOL_RESULT (call_id: c1): result body", "Current request: current question"} {
		if !strings.Contains(parsed.UserText, want) {
			t.Errorf("flattened UserText missing %q; got %q", want, parsed.UserText)
		}
	}
}

func TestExtractClaudeCodeSessionId(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"sess-123\",\"device_id\":\"dev-1\"}"}}`)
	if got := extractClaudeCodeSessionId(payload); got != "sess-123" {
		t.Fatalf("session id = %q, want sess-123", got)
	}
	if got := extractClaudeCodeSessionId([]byte(`{"model":"x"}`)); got != "" {
		t.Fatalf("session id (missing metadata) = %q", got)
	}
}

func TestDeriveConversationIdStability(t *testing.T) {
	id1 := deriveConversationId("key", "sess-1", "system prompt")
	id2 := deriveConversationId("key", "sess-1", "system prompt")
	if id1 != id2 {
		t.Fatal("conversation id is not deterministic")
	}
	if id1 == deriveConversationId("key", "sess-2", "system prompt") {
		t.Fatal("different sessions must not collide")
	}
	if id1 == deriveConversationId("other-key", "sess-1", "system prompt") {
		t.Fatal("different api keys must not collide")
	}
	// UUID shape NNNNNNNN-NNNN-NNNN-NNNN-NNNNNNNNNNNN.
	if len(id1) != 36 || strings.Count(id1, "-") != 4 {
		t.Fatalf("conversation id %q is not UUID-shaped", id1)
	}
	// The volatile cch= segment of a Claude Code system prompt must not affect the id.
	a := deriveConversationId("key", "", "You are Claude. cch=aaaa; rest")
	b := deriveConversationId("key", "", "You are Claude. cch=bbbb; rest")
	if a != b {
		t.Fatalf("cch variance changed conversation id: %q vs %q", a, b)
	}
}

func TestCursorTokenUsage(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(800)
	u.addOutput(10)
	u.addOutput(5)
	in, out := u.get()
	if in != 200 {
		t.Errorf("input estimate = %d, want 200", in)
	}
	if out != 15 {
		t.Errorf("output tokens = %d, want 15", out)
	}
}

func TestDecodeMcpArgsToJSON(t *testing.T) {
	if got := decodeMcpArgsToJSON(nil); got != "{}" {
		t.Fatalf("empty args = %q", got)
	}
	// Raw JSON fallback values are preserved as decoded values.
	got := decodeMcpArgsToJSON(map[string][]byte{"cmd": []byte(`"ls -la"`)})
	if got != `{"cmd":"ls -la"}` {
		t.Fatalf("args = %q", got)
	}
}

func TestParseCursorModelsResponse(t *testing.T) {
	// Build a tiny GetUsableModelsResponse: field 1 (models, submessage) x2.
	entry := func(id, name string) []byte {
		var b []byte
		b = append(b, 0x0a, byte(len(id)))
		b = append(b, id...)
		b = append(b, 0x22, byte(len(name)))
		b = append(b, name...)
		return b
	}
	e1 := entry("composer-2", "Composer 2")
	e2 := entry("cursor-small", "")
	var body []byte
	for _, e := range [][]byte{e1, e2} {
		body = append(body, 0x0a, byte(len(e)))
		body = append(body, e...)
	}

	models := parseCursorModelsResponse(body)
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
	if models[0].ID != "composer-2" || models[0].DisplayName != "Composer 2" {
		t.Errorf("model[0] = %+v", models[0])
	}
	if models[1].ID != "cursor-small" {
		t.Errorf("model[1].ID = %q", models[1].ID)
	}

	// Same payload wrapped in a Connect frame must parse identically.
	framed := cursorproto.FrameConnectMessage(body, 0)
	if got := parseCursorModelsResponse(framed); len(got) != 2 {
		t.Fatalf("framed models = %d, want 2", len(got))
	}
}

func TestGetCursorFallbackModels(t *testing.T) {
	models := getCursorFallbackModels()
	if len(models) == 0 {
		t.Fatal("fallback models empty")
	}
	for _, m := range models {
		if m.ID == "" || m.Type != cursorAuthType {
			t.Errorf("bad fallback model: %+v", m)
		}
	}
}
