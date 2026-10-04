// Sanitizers for upstream error material that is about to cross the
// downstream boundary. Upstream providers sometimes echo account-identifying
// content (account emails, account/project IDs, credential file names,
// token fragments) in their error payloads. That content must not leak to
// downstream callers who only hold a client API key; server-side logs keep
// the original text, so sanitization is applied only at response write-out.
package clienterror

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const redactedMarker = "[REDACTED]"

var (
	// Emails are the most common account identifier embedded in upstream error messages.
	downstreamEmailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)

	// Userinfo inside URLs may carry proxy or API credentials.
	downstreamURLUserinfoPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://)[^/\s:@]+(:[^/\s@]+)?@`)

	// Authorization header fragments echoed by upstream debug payloads.
	downstreamAuthHeaderPattern = regexp.MustCompile(`(?i)\b((?:x-)?authorization\s*[:=]\s*)\S[^\r\n;,]*`)

	// Bearer/Basic tokens embedded free-form in messages. The minimum length keeps
	// ordinary words (e.g. "basic auth") intact.
	downstreamBearerPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)

	// Well-known API key shapes: OpenAI (sk-), Anthropic (sk-ant-), GitHub (gh*_),
	// Google OAuth tokens (ya29.), Gemini keys (AIza), AWS access key IDs (AKIA),
	// Slack tokens (xox-) and JWTs (eyJ...).
	downstreamKeyShapePattern = regexp.MustCompile(`\b(?:sk-(?:ant-)?[A-Za-z0-9._~+/=-]{6,}|gh[pousr]_[A-Za-z0-9]{16,}|ya29\.[A-Za-z0-9._~+/=-]{8,}|AIza[A-Za-z0-9_\-]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9\-]{8,}|eyJ[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{4,}(?:\.[A-Za-z0-9_\-]*)?)\b`)

	// Credential-shaped key/value pairs in free text (JSON-ish or python-dict style).
	downstreamSecretKVPattern = regexp.MustCompile(`(?i)\b((?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|id[ _-]?token|session[ _-]?token|client[ _-]?secret|secret[ _-]?key|private[ _-]?key|password|credentials?)\b\s*["']?\s*[:=]\s*["']?)[^\s"',\r\n;}]+`)

	// Account-identifying key/value pairs. The key is kept so the message shape
	// (which failure, which dimension) stays readable.
	downstreamAccountKVPattern = regexp.MustCompile(`(?i)\b((?:project[ _]?(?:id|number)|account[ _]?(?:id|number)|tenant[ _]?id|organization[ _]?(?:id|number)|consumer|subscription[ _]?id|customer[ _]?id)\s*["']?\s*[:=]\s*["']?)[A-Za-z0-9_][A-Za-z0-9._\-]*(?::[A-Za-z0-9_][A-Za-z0-9._\-]*)?`)

	// Same account keys in loose prose form ("tenant id contoso-prod", no colon).
	// The value must contain a digit or a dash so ordinary words ("required",
	// "invalid") are not swallowed.
	downstreamAccountProsePattern = regexp.MustCompile(`(?i)\b((?:project[ _]?(?:id|number)|account[ _]?(?:id|number)|tenant[ _]?id|organization[ _]?(?:id|number)|consumer|subscription[ _]?id|customer[ _]?id)\s+)[A-Za-z0-9._]*[-0-9][A-Za-z0-9._:\-]*`)

	// AWS ARNs embed the 12-digit account ID.
	downstreamARNPattern = regexp.MustCompile(`(?i)\b(arn:aws[a-z0-9\-]*(?::[a-z0-9\-]*)*:)(\d{12})`)

	// Secret-bearing query parameters embedded in echoed URLs.
	downstreamQuerySecretPattern = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_.\-]*(?:key|token|secret|password|auth|sig|signature)=)[^&\s,"'\r\n;]+`)

	// Local auth file names (e.g. "claude-user@gmail.com.json"). Basename-shaped
	// tokens only; directory layout and non-credential files are not touched.
	downstreamAuthFilePattern = regexp.MustCompile(`(?i)\b[\w@.+\-]{3,}\.json\b`)
)

// SanitizeDownstreamErrorText redacts account identifiers (emails, account or
// project IDs, local credential file names) and credential material
// (authorization headers, bearer tokens, API key shapes, secret key/value
// pairs) from free-form upstream error text. Generic diagnostics — model
// availability, rate limits, upstream 5xx, request IDs and quota numbers —
// are intentionally untouched.
func SanitizeDownstreamErrorText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	out := downstreamURLUserinfoPattern.ReplaceAllString(text, "${1}"+redactedMarker+"@")
	out = downstreamAuthHeaderPattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamKeyShapePattern.ReplaceAllString(out, redactedMarker)
	out = downstreamBearerPattern.ReplaceAllString(out, "${1} "+redactedMarker)
	out = downstreamSecretKVPattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamAccountKVPattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamAccountProsePattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamARNPattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamQuerySecretPattern.ReplaceAllString(out, "${1}"+redactedMarker)
	out = downstreamEmailPattern.ReplaceAllString(out, redactedMarker)
	out = downstreamAuthFilePattern.ReplaceAllString(out, redactedMarker)
	return out
}

// isSensitiveDownstreamErrorKey reports whether a JSON field name in an
// upstream error payload carries account-identifying or credential material
// and must be redacted whole. Usage counters (tokens, token_count, ...) are
// explicitly kept: they describe the request, not the account.
func isSensitiveDownstreamErrorKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	if k == "" {
		return false
	}
	if k == "tokens" || strings.HasSuffix(k, "_tokens") ||
		strings.Contains(k, "token_count") || strings.Contains(k, "token_limit") || strings.Contains(k, "token_usage") {
		return false
	}
	switch k {
	case "email", "mail",
		"account", "account_id", "accountid",
		"user", "user_id", "userid", "username", "user_name", "principal", "sub",
		"tenant", "tenant_id", "organization", "organization_id", "org_id",
		"project", "project_id", "project_number",
		"customer", "customer_id", "subscription", "subscription_id", "owner",
		"token", "access_token", "refresh_token", "id_token", "session_token", "api_token", "auth_token",
		"secret", "client_secret", "private_key", "password", "passwd", "api_key", "apikey", "client_key",
		"authorization", "credential", "credentials", "bearer",
		"file", "filename", "file_name", "filepath", "file_path", "path",
		"auth_file", "credential_file", "key_file":
		return true
	}
	return strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "_password") ||
		strings.HasSuffix(k, "_api_key") ||
		strings.HasSuffix(k, "_email") ||
		strings.HasSuffix(k, "_account_id")
}

// sanitizeDownstreamErrorValue is the recursive worker behind
// SanitizeDownstreamErrorValue. It reports whether anything was redacted so
// callers can preserve the exact original representation when it is clean.
func sanitizeDownstreamErrorValue(value any) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		changed := false
		cleaned := make(map[string]any, len(v))
		for key, item := range v {
			if isSensitiveDownstreamErrorKey(key) {
				if str, isStr := item.(string); !isStr || str != redactedMarker {
					changed = true
				}
				cleaned[key] = redactedMarker
				continue
			}
			cleanedItem, itemChanged := sanitizeDownstreamErrorValue(item)
			if itemChanged {
				changed = true
			}
			cleaned[key] = cleanedItem
		}
		if !changed {
			return value, false
		}
		return cleaned, true
	case []any:
		changed := false
		cleaned := make([]any, len(v))
		for i, item := range v {
			cleanedItem, itemChanged := sanitizeDownstreamErrorValue(item)
			if itemChanged {
				changed = true
			}
			cleaned[i] = cleanedItem
		}
		if !changed {
			return value, false
		}
		return cleaned, true
	case string:
		cleaned := SanitizeDownstreamErrorText(v)
		if cleaned == v {
			return value, false
		}
		return cleaned, true
	default:
		return value, false
	}
}

// SanitizeDownstreamErrorValue recursively sanitizes a decoded JSON value:
// fields with credential- or account-identifying names are replaced with a
// redaction marker, and plain strings pass through SanitizeDownstreamErrorText.
// Non-string scalars (including json.Number) are returned unchanged.
func SanitizeDownstreamErrorValue(value any) any {
	sanitized, _ := sanitizeDownstreamErrorValue(value)
	return sanitized
}

// SanitizeDownstreamErrorBody sanitizes an upstream error response body that is
// forwarded downstream. Valid JSON keeps its structure with sensitive content
// redacted; anything else is treated as free text. Returns the input unchanged
// when there is nothing to redact, so clean upstream payloads keep their
// original byte-level representation.
func SanitizeDownstreamErrorBody(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body
	}
	if !json.Valid(trimmed) {
		return []byte(SanitizeDownstreamErrorText(string(body)))
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return body
	}
	sanitized, changed := sanitizeDownstreamErrorValue(value)
	if !changed {
		return body
	}
	out, errMarshal := json.Marshal(sanitized)
	if errMarshal != nil {
		return body
	}
	return out
}

// SanitizeDownstreamHeaderValue redacts an upstream header value destined for a
// downstream error payload. Credential-carrying headers are replaced with a
// redaction marker; all other headers are returned unchanged.
func SanitizeDownstreamHeaderValue(key, value string) string {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lowerKey, "authorization"),
		strings.Contains(lowerKey, "api-key"),
		strings.Contains(lowerKey, "apikey"),
		strings.Contains(lowerKey, "token"),
		strings.Contains(lowerKey, "secret"),
		strings.Contains(lowerKey, "session"),
		strings.Contains(lowerKey, "credential"):
		return redactedMarker
	default:
		return SanitizeDownstreamErrorText(value)
	}
}
