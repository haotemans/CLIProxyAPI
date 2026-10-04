package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorSanitizesUpstreamAccountIdentifiers(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusForbidden,
		Error:      errors.New(`{"type":"error","error":{"type":"permission_error","message":"org billing.payments@example-corp.com has no access to claude-opus-4-1"},"request_id":"req_011CSkLive"}`),
	}

	got := handler.toClaudeError(msg)
	if strings.Contains(got.Error.Message, "billing.payments@example-corp.com") {
		t.Fatalf("account email leaked: %q", got.Error.Message)
	}
	if !strings.Contains(got.Error.Message, "has no access to claude-opus-4-1") {
		t.Fatalf("generic diagnostic lost: %q", got.Error.Message)
	}
	if got.Error.Type != "permission_error" {
		t.Fatalf("error.type = %q, want permission_error", got.Error.Type)
	}
}

func TestWriteClaudeErrorResponseSanitizesFreeTextError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream unavailable for quota owner ops-team@internal.example, 30 requests/min cap"),
	})

	body := recorder.Body.String()
	if strings.Contains(body, "ops-team@internal.example") {
		t.Fatalf("account email leaked: %s", body)
	}
	if !strings.Contains(body, "30 requests/min cap") {
		t.Fatalf("generic quota hint lost: %s", body)
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(); !strings.Contains(got, "upstream unavailable") {
		t.Fatalf("generic diagnostic lost: %q", got)
	}
}
