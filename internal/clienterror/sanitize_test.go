package clienterror

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeDownstreamErrorText_RedactsEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "plain email", input: `usage limit exceeded for user someaccount@gmail.com, retry later`},
		{name: "email inside quotes", input: `account "someaccount@gmail.com" has been disabled`},
		{name: "service account email", input: `caller my-sa@my-project.iam.gserviceaccount.com lacks permission`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDownstreamErrorText(tt.input)
			if strings.Contains(got, "@") {
				t.Fatalf("email leaked in output: %q", got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected redaction marker, got %q", got)
			}
		})
	}
}

func TestSanitizeDownstreamErrorText_KeepsGenericMessages(t *testing.T) {
	tests := []string{
		`"Your input exceeds the context window of this model. Please adjust your input and try again."`,
		`This request would exceed your account's rate limit. Please try again later.`,
		`Our servers are currently overloaded. Please try again later.`,
		`upstream request timeout`,
		`token refresh failed: revoked`,
		`model not found: claude-sonnet-4-6`,
		`All credentials for model gpt-5 are cooling down via provider codex, retry after 20s`,
		`quota exceeded: 250 requests per day, limit window resets at midnight`,
		`request_id=req_6bd9e0d21abc, trace=00-4f2a`,
	}
	for _, input := range tests {
		got := SanitizeDownstreamErrorText(input)
		if got != input {
			t.Fatalf("generic message altered:\n in: %q\nout: %q", input, got)
		}
	}
}

func TestSanitizeDownstreamErrorText_RedactsCredentialMaterial(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		forbidden string
	}{
		{name: "openai key", input: `invalid request with sk-abc123def456ghi789`, forbidden: "sk-abc"},
		{name: "anthropic key", input: "echoed key " + "sk-ant-api03" + "-AbCdEf123456789012 was revoked", forbidden: "sk-ant-api03"},
		{name: "bearer token", input: `upstream said Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig`, forbidden: "eyJhbGci"},
		{name: "authorization header with colon", input: `Authorization: Bearer tok_live_1234567890abcdef`, forbidden: "tok_live"},
		{name: "authorization header with equals", input: `authorization=Basic dXNlcjpwYXNzd29yZA==`, forbidden: "dXNlcjp"},
		{name: "google oauth token", input: `token ya29.a0AfHgMwDsdjkj34jk34kj34kj34 failed`, forbidden: "ya29."},
		{name: "gemini api key", input: `key AIzaSyD4iE-samplekey000000000000001 invalid`, forbidden: "AIzaSyD4iE"},
		{name: "aws access key id", input: `AccessDenied for AKIAIOSFODNN7EXAMPLE`, forbidden: "AKIAIOSFODNN7EXAMPLE"},
		{name: "jwt", input: `bad eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`, forbidden: "eyJzdWI"},
		{name: "secret kv json-ish", input: `{"api_key": "deadbeefcafe12345"}`, forbidden: "deadbeef"},
		{name: "secret kv python-ish", input: `{'access_token': 'deadbeefcafe12345'}`, forbidden: "deadbeef"},
		{name: "secret kv equals", input: `client_secret=deadbeefcafe12345 rejected`, forbidden: "deadbeef"},
		{name: "password kv", input: `password: hunter2hunter2 wrong`, forbidden: "hunter2"},
		{name: "url userinfo", input: `dial https://user:s3cr3t@proxy.internal:8443 refused`, forbidden: "s3cr3t"},
		{name: "url query api key", input: `GET /v1/models?key=AIzaSyB9-realishkey0000000000&lang=en 401`, forbidden: "AIzaSyB9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDownstreamErrorText(tt.input)
			if strings.Contains(got, tt.forbidden) {
				t.Fatalf("credential material %q leaked in output: %q", tt.forbidden, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected redaction marker, got %q", got)
			}
		})
	}
}

func TestSanitizeDownstreamErrorText_RedactsAuthFileNames(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		forbidden string
	}{
		{name: "auth file with email", input: `refresh failed for auths/claude-someaccount@gmail.com.json: 401`, forbidden: "someaccount"},
		{name: "opaque auth file", input: `token file team-prod-antigravity-00042.json is revoked`, forbidden: "team-prod-antigravity-00042.json"},
		{name: "windows path auth file", input: `open D:\weiai\project\CLIProxyAPI\auths\codex-paid.json: access denied`, forbidden: "codex-paid.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDownstreamErrorText(tt.input)
			if strings.Contains(got, tt.forbidden) {
				t.Fatalf("auth file identifier %q leaked in output: %q", tt.forbidden, got)
			}
		})
	}
}

func TestSanitizeDownstreamErrorText_RedactsAccountIDs(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		forbidden string
	}{
		{name: "gcp consumer project number", input: `Quota exceeded for consumer: 'project_number:123456789012'`, forbidden: "123456789012"},
		{name: "project id kv", input: `permission denied on project_id: my-internal-proj-42`, forbidden: "my-internal-proj-42"},
		{name: "aws account id", input: `User arn:aws:iam::111122223333 denied, account ID: 111122223333`, forbidden: "111122223333"},
		{name: "tenant id", input: `token for tenant id contoso-prod-tenant expired`, forbidden: "contoso-prod-tenant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDownstreamErrorText(tt.input)
			if strings.Contains(got, tt.forbidden) {
				t.Fatalf("account identifier %q leaked in output: %q", tt.forbidden, got)
			}
		})
	}
}

func TestSanitizeDownstreamErrorBody_JSONWalk(t *testing.T) {
	input := `{"error":{"message":"quota exceeded for user someaccount@gmail.com","type":"rate_limit_error","account_id":"acc-42","request_id":"req_abc","detail":{"api_key":"sk-secret1234567","tokens":17}}}`
	out := SanitizeDownstreamErrorBody([]byte(input))
	if !json.Valid(out) {
		t.Fatalf("output is not valid JSON: %q", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errNode, ok := decoded["error"].(map[string]any)
	if !ok {
		t.Fatalf("error node missing: %v", decoded)
	}
	if msg, _ := errNode["message"].(string); strings.Contains(msg, "@") || !strings.Contains(msg, "quota exceeded") {
		t.Fatalf("message mishandled: %q", msg)
	}
	if got, _ := errNode["account_id"].(string); got != "[REDACTED]" {
		t.Fatalf("account_id = %q, want [REDACTED]", got)
	}
	if got, _ := errNode["request_id"].(string); got != "req_abc" {
		t.Fatalf("request_id = %q, want preserved req_abc", got)
	}
	detail, ok := errNode["detail"].(map[string]any)
	if !ok {
		t.Fatalf("detail node missing: %v", errNode)
	}
	if got, _ := detail["api_key"].(string); got != "[REDACTED]" {
		t.Fatalf("api_key = %q, want [REDACTED]", got)
	}
	if got := detail["tokens"]; got != float64(17) {
		t.Fatalf("tokens usage counter altered: %v", got)
	}
}

func TestSanitizeDownstreamErrorBody_NonJSON(t *testing.T) {
	input := "slow down, user admin@example.org hit the per-minute cap (30 requests)"
	out := SanitizeDownstreamErrorBody([]byte(input))
	text := string(out)
	if strings.Contains(text, "admin@example.org") {
		t.Fatalf("email leaked: %q", text)
	}
	if !strings.Contains(text, "per-minute cap (30 requests)") {
		t.Fatalf("generic quota detail lost: %q", text)
	}
}

func TestSanitizeDownstreamErrorBody_PreservesLargeNumbers(t *testing.T) {
	input := `{"error":{"message":"too big","sequence_number":9007199254740993}}`
	out := SanitizeDownstreamErrorBody([]byte(input))
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("large integer precision lost: %q", out)
	}
}

func TestSanitizeDownstreamErrorValue_MapShape(t *testing.T) {
	cleaned, ok := SanitizeDownstreamErrorValue(map[string]any{
		"type":    "invalid_request",
		"code":    "cyber_policy",
		"message": "This content was flagged for possible cybersecurity risk.",
		"param":   nil,
	}).(map[string]any)
	if !ok {
		t.Fatal("map shape not preserved")
	}
	if cleaned["type"] != "invalid_request" || cleaned["code"] != "cyber_policy" {
		t.Fatalf("classification fields altered: %v", cleaned)
	}
	if cleaned["message"] != "This content was flagged for possible cybersecurity risk." {
		t.Fatalf("generic message altered: %v", cleaned["message"])
	}
	if param, exists := cleaned["param"]; !exists || param != nil {
		t.Fatalf("param should stay nil-present, got %v", param)
	}
}

func TestSanitizeDownstreamHeaderValue(t *testing.T) {
	if got := SanitizeDownstreamHeaderValue("Authorization", "Bearer tok_live_123456"); got != "[REDACTED]" {
		t.Fatalf("authorization header = %q", got)
	}
	if got := SanitizeDownstreamHeaderValue("X-Api-Key", "abcdef123456"); got != "[REDACTED]" {
		t.Fatalf("x-api-key header = %q", got)
	}
	if got := SanitizeDownstreamHeaderValue("Retry-After", "30"); got != "30" {
		t.Fatalf("retry-after header altered: %q", got)
	}
	if got := SanitizeDownstreamHeaderValue("X-Request-Id", "req-admin@corp.example-123"); strings.Contains(got, "@") {
		t.Fatalf("embedded email leaked through header: %q", got)
	}
}
