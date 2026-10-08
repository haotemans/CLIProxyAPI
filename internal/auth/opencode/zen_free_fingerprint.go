package opencode

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ZenFreeUserAgent spoofs the opencode CLI release for the anonymous free
// tier. Paid keys are never cloaked: only the exact "public" key selects
// this path.
const ZenFreeUserAgent = "opencode/1.18.31"

// ZenFreeClientHeader is the x-opencode-client value the CLI sends; the zen
// console's free-tier gate rejects requests that do not look like opencode.
const ZenFreeClientHeader = "cli"

// ZenFreeProviderKeys are the executor provider keys whose credentials may
// carry the anonymous "public" key. Plain openai-compatibility entries are
// excluded by design: their default cli-proxy-openai-compat identity must
// stay untouched.
var ZenFreeProviderKeys = map[string]struct{}{
	"opencode-go": {},
	"opencode":    {},
}

const (
	// zenFreeIDTimestampMask is opencode's 48-bit (6-byte) ULID-style ID window.
	zenFreeIDTimestampMask = uint64(0xFFFFFFFFFFFF)
	// zenFreeIDTimestampShift is the 4-bit counter nibble opencode appends to
	// the timestamp bits of its ascending IDs (packages/opencode/src/id/id.ts).
	zenFreeIDTimestampShift = 4
	zenFreeIDSuffixChars    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	zenFreeIDSuffixLen      = 14
)

type zenFreeSessionContextKey struct{}

// WithZenFreeSessionFingerprint pins the ses_ ID for one logical proxy
// session on the request context, so every upstream attempt (and failover
// retry against the same auth) shares it, like a long-lived CLI session.
// Tests may freeze the clock by wrapping this with a fixed context.
func WithZenFreeSessionFingerprint(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, zenFreeSessionContextKey{}, id)
}

// ZenFreeSessionFingerprintFromContext returns the pinned session fingerprint
// or mints a fresh one when none is pinned. The fallback is per-call on
// purpose: never reuse a process-wide value across client sessions.
func ZenFreeSessionFingerprintFromContext(ctx context.Context) string {
	if ctx != nil {
		if id, ok := ctx.Value(zenFreeSessionContextKey{}).(string); ok {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				return trimmed
			}
		}
	}
	return NewZenFreeSessionFingerprint(time.Now())
}

// IsZenFreeFingerprintAuth reports whether an opencode-go/opencode credential
// is the anonymous free tier and should be cloaked: the (already trimmed)
// api_key attribute is exactly the literal "public". Whitespace-padded or
// differently cased keys stay paid and are never cloaked.
func IsZenFreeFingerprintAuth(provider, apiKey string) bool {
	_, ok := ZenFreeProviderKeys[strings.ToLower(strings.TrimSpace(provider))]
	return ok && IsZenFreeAPIKey(apiKey)
}

// NewZenFreeMessageFingerprint returns an ascending opencode request ID,
// "msg_" + 12 lowercase hex digits + 14 random base62 chars. The timestamp
// nibble is (ms << 4), so lexicographic order follows wall-clock time; the
// sink parses only the timestamp (the counter nibble is opencode-internal
// ordering).
func NewZenFreeMessageFingerprint(now time.Time) string {
	return zenFreeFingerprintID("msg", zenFreeEncodingBits(now, false))
}

// NewZenFreeSessionFingerprint returns a descending opencode session ID,
// "ses_" + 12 lowercase hex digits + 14 random base62 chars. opencode encodes
// the bitwise-not of the ascending bits; the simplified, sink-decodable form
// is (2^48-1) - (ms << 4), which keeps sessions reverse-sorted by creation
// time.
func NewZenFreeSessionFingerprint(now time.Time) string {
	return zenFreeFingerprintID("ses", zenFreeEncodingBits(now, true))
}

func zenFreeEncodingBits(now time.Time, descending bool) uint64 {
	ms := uint64(now.UnixMilli()) & (zenFreeIDTimestampMask >> zenFreeIDTimestampShift)
	encoded := ms << zenFreeIDTimestampShift
	if descending {
		encoded = zenFreeIDTimestampMask - encoded
	}
	return encoded & zenFreeIDTimestampMask
}

func zenFreeFingerprintID(prefix string, bits uint64) string {
	var suffix [zenFreeIDSuffixLen]byte
	rnd := make([]byte, zenFreeIDSuffixLen)
	if _, err := rand.Read(rnd); err != nil {
		// crypto/rand never fails on supported platforms; fall back to the
		// timestamp bits so the ID stays structurally valid.
		for i := range suffix {
			suffix[i] = zenFreeIDSuffixChars[byte(bits>>uint(8*(i%6)))%62]
		}
	} else {
		for i, b := range rnd {
			suffix[i] = zenFreeIDSuffixChars[b%62]
		}
	}
	return fmt.Sprintf("%s_%012x%s", prefix, bits, suffix)
}

// ApplyZenFreeFingerprintHeaders overwrites the spoofed identity headers on
// an outgoing free-tier request. It must run after
// ApplyCustomHeadersFromAttrs so neither user-configured nor client-supplied
// headers can displace the fingerprint the sink validates. The ses_ value is
// resolved from context (pinned per proxy session) or minted fresh.
func ApplyZenFreeFingerprintHeaders(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", ZenFreeUserAgent)
	req.Header.Set("X-Opencode-Client", ZenFreeClientHeader)
	req.Header.Set("X-Opencode-Request", NewZenFreeMessageFingerprint(time.Now()))
	req.Header.Set("X-Opencode-Session", ZenFreeSessionFingerprintFromContext(ctx))
}

// ApplyZenFreePayloadEnforcement forces the business-payload shape the
// anonymous zen tier requires on every upstream call: stream:true plus the
// bash/read function tools the opencode CLI always ships. The tools array
// grows, never replaces: an existing user-defined "bash"/"read" keeps its own
// schema, so the client's real tools and assistant tool-call history replay
// stay intact.
func ApplyZenFreePayloadEnforcement(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "stream", true)
	if tools := gjson.GetBytes(payload, "tools"); !tools.Exists() || !tools.IsArray() {
		payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[]`))
	}
	payload = ensureZenFreeStubTool(payload, "bash", zenFreeStubBash)
	payload = ensureZenFreeStubTool(payload, "read", zenFreeStubRead)
	return payload
}

const (
	zenFreeStubBash = `{"type":"function","function":{"name":"bash","description":"run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string","description":"the shell command to run"}},"required":["command"],"additionalProperties":false}}}`
	zenFreeStubRead = `{"type":"function","function":{"name":"read","description":"read a file from disk","parameters":{"type":"object","properties":{"path":{"type":"string","description":"the file path to read"}},"required":["path"],"additionalProperties":false}}}`
)

// HasZenFreeFunctionTool reports whether payload.tools already declares a
// function tool under the given name (any spelling: nested OpenAI shape or a
// bare name entry both count as an explicit user choice that must not be
// shadowed by a stub).
func HasZenFreeFunctionTool(payload []byte, name string) bool {
	return zenFreeToolIndex(payload, name) >= 0
}

func zenFreeToolIndex(payload []byte, name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return -1
	}
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return -1
	}
	found := -1
	tools.ForEach(func(i, tool gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(tool.Get("function.name").String()), name) ||
			strings.EqualFold(strings.TrimSpace(tool.Get("name").String()), name) {
			found = int(i.Int())
			return false
		}
		return true
	})
	return found
}

func ensureZenFreeStubTool(payload []byte, name string, stub string) []byte {
	if zenFreeToolIndex(payload, name) >= 0 {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, "tools.-1", []byte(stub))
	if err != nil {
		return payload
	}
	return updated
}
