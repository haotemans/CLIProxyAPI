package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

// Upstream error bodies that pass through WriteErrorResponse must not leak
// account emails, account IDs, or credential material to downstream callers,
// while generic diagnostics (request IDs, quota hints, classifications) stay.
func TestWriteErrorResponse_SanitizesRawUpstreamJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	upstream := `{"error":{"message":"usage limit exceeded for account owner.mgmt@gmail.com, retry at 12:00. quota 250/250 requests used.","type":"rate_limit_error","account_id":"acc-mgr-42","request_id":"req_live_123"}}`
	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New(upstream),
	})

	body := recorder.Body.Bytes()
	if !json.Valid(body) {
		t.Fatalf("body is not valid JSON: %s", body)
	}
	if strings.Contains(string(body), "owner.mgmt@gmail.com") {
		t.Fatalf("account email leaked: %s", body)
	}
	if strings.Contains(string(body), "acc-mgr-42") {
		t.Fatalf("account id leaked: %s", body)
	}
	if got := gjson.GetBytes(body, "error.request_id").String(); got != "req_live_123" {
		t.Fatalf("request_id lost or altered: %q", got)
	}
	if got := gjson.GetBytes(body, "error.type").String(); got != "rate_limit_error" {
		t.Fatalf("error type lost: %q", got)
	}
	message := gjson.GetBytes(body, "error.message").String()
	if !strings.Contains(message, "quota 250/250 requests used") {
		t.Fatalf("generic quota detail lost: %q", message)
	}
	if !strings.Contains(message, "usage limit exceeded") {
		t.Fatalf("generic diagnostic lost: %q", message)
	}
}

func TestWriteErrorResponse_SanitizesNonJSONUpstreamText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	antKey := "sk-ant-api03" + "-ZZZ987654321"
	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream 502 for claude-user777@example.com (" + antKey + "): token expired, slow down"),
	})

	body := string(recorder.Body.Bytes())
	if strings.Contains(body, "user777@example.com") {
		t.Fatalf("account email leaked: %s", body)
	}
	if strings.Contains(body, "sk-ant-api03") || strings.Contains(body, "ZZZ987654321") {
		t.Fatalf("upstream credential fragment leaked: %s", body)
	}
	if !strings.Contains(body, "token expired") {
		t.Fatalf("generic diagnostic lost: %s", body)
	}
	if got := gjson.GetBytes([]byte(body), "error.type").String(); got != "server_error" {
		t.Fatalf("error envelope type = %q, want server_error", got)
	}
}

func TestBuildErrorResponseBodyWithError_RedactsAuthFileNames(t *testing.T) {
	body := BuildErrorResponseBody(http.StatusUnauthorized, "refresh failed: auths/codex-paid-team.json revoked")
	text := string(body)
	if strings.Contains(text, "codex-paid-team.json") {
		t.Fatalf("auth file name leaked: %s", text)
	}
	if !strings.Contains(text, "refresh failed") {
		t.Fatalf("generic diagnostic lost: %s", text)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunk_SanitizesNestedError(t *testing.T) {
	errText := `{"error":{"type":"invalid_request","code":"message_too_big","message":"request from admin@corp.example too large for this account","email":"admin@corp.example","request_id":"req_x1"}}`
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, errText, 0)

	var payload struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(payload.Error["message"].(string), "@") {
		t.Fatalf("email leaked in message: %v", payload.Error["message"])
	}
	if got := payload.Error["email"]; got != "[REDACTED]" {
		t.Fatalf("email field = %v, want [REDACTED]", got)
	}
	if got := payload.Error["request_id"]; got != "req_x1" {
		t.Fatalf("request_id altered: %v", got)
	}
	if got := payload.Error["code"]; got != "message_too_big" {
		t.Fatalf("code altered: %v", got)
	}
}
