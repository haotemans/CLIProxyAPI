package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// mirasimOAuthTestWire stands in for relay.mirasim.ai: it answers the device
// session mint, the signed /v1/messages route, and captures headers per route.
type mirasimOAuthTestWire struct {
	server *httptest.Server

	lastMintAuth  string
	lastMsgHeader http.Header
	lastMsgBody   []byte
	lastMsgPath   string
	mintCalls     int
}

func newMirasimOAuthTestWire(t *testing.T) *mirasimOAuthTestWire {
	t.Helper()
	wire := &mirasimOAuthTestWire{}
	wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/device/session":
			wire.mintCalls++
			wire.lastMintAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "oauth-ticket-1", "expiresIn": 600})
		case "/v1/messages":
			wire.lastMsgPath = r.URL.Path
			wire.lastMsgHeader = r.Header.Clone()
			wire.lastMsgBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-4","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(wire.server.Close)
	return wire
}

// mirasimOAuthTestAuth builds an OAuth auth record with a live access token
// (JWT exp ahead) and a generated device identity pointed at the wire.
func mirasimOAuthTestAuth(t *testing.T, relayURL string) *cliproxyauth.Auth {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"sub": "mirasim-user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	accessToken := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
	deviceKey, err := mirasimauth.NewDeviceKey()
	if err != nil {
		t.Fatalf("NewDeviceKey: %v", err)
	}
	return &cliproxyauth.Auth{
		Provider: "mirasim",
		Metadata: map[string]any{
			"type":               "mirasim",
			"auth_kind":          "oauth",
			"access_token":       accessToken,
			"refresh_token":      "seed-refresh-token",
			"expired":            time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"device_private_key": string(deviceKey),
			"relay_url":          relayURL,
			"admin_url":          relayURL,
		},
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
}

func TestMirasimOAuthExecutor_SignedClaudeMessagesRoundTrip(t *testing.T) {
	wire := newMirasimOAuthTestWire(t)
	exec := NewMirasimExecutor(&config.Config{})
	auth := mirasimOAuthTestAuth(t, wire.server.URL)
	payload := []byte(`{"model":"claude-sonnet-4","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	resp, errExecute := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := gjson.GetBytes([]byte(resp.Payload), "content.0.text").String(); got != "ok" && gjson.GetBytes(resp.Payload, "choices").Array() != nil && gjson.GetBytes(resp.Payload, "content").Array() == nil {
		t.Fatalf("unexpected response shape: %s", resp.Payload)
	}

	if wire.mintCalls != 1 {
		t.Fatalf("mint calls = %d, want 1", wire.mintCalls)
	}
	if !strings.HasPrefix(wire.lastMintAuth, "Bearer ey") {
		t.Fatalf("mint called with wrong authorization: %q", wire.lastMintAuth)
	}
	if wire.lastMsgPath != "/v1/messages" {
		t.Fatalf("messages path = %q", wire.lastMsgPath)
	}
	headers := wire.lastMsgHeader
	if got := headers.Get("Authorization"); got != "Bearer oauth-ticket-1" {
		t.Fatalf("request Authorization = %q, want the device ticket", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key must not be sent: %q", got)
	}
	// On the wire only the envelope and the client version travel in clear:
	// signature and metadata live inside the mrs-seal-v1 envelope (plugin
	// vector: the sealed plaintext includes device/ts/nonce/sig).
	for _, key := range []string{"X-Mirasim-Client", "X-Mirasim-Enc"} {
		if headers.Get(key) == "" {
			t.Fatalf("signed request missing %s: %v", key, headers)
		}
	}
	for _, key := range []string{"X-Mirasim-Device", "X-Mirasim-Ts", "X-Mirasim-Nonce", "X-Mirasim-Sig", "X-Mirasim-Session", "X-Mirasim-Agent", "X-Mirasim-Call"} {
		if headers.Get(key) != "" {
			t.Fatalf("sealed header %s must not remain unsealed on the wire", key)
		}
	}
	if !gjson.GetBytes(wire.lastMsgBody, "messages").IsArray() {
		t.Fatalf("upstream body missing messages: %s", wire.lastMsgBody)
	}
}

func TestMirasimOAuthExecutor_RefreshRotatesMetadata(t *testing.T) {
	// Admin service for refresh: /auth/refresh rotates tokens; /auth/me reports
	// a changed plan so the schedule must refresh.
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
		claims, _ := json.Marshal(map[string]any{
			"sub":  "mirasim-user-1",
			"exp":  time.Now().Add(time.Hour).Unix(),
			"plan": "pro",
		})
		switch r.URL.Path {
		case "/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "operator@example.com", "plan": "pro"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln",
				"refresh_token": "rotated-refresh-token",
				"expires_in":    3600,
			})
		}
	}))
	t.Cleanup(admin.Close)

	auth := mirasimOAuthTestAuth(t, admin.URL)
	// Inside the 15-minute refresh lead so the schedule must rotate now.
	auth.Metadata["expired"] = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	exec := NewMirasimOAuthExecutor(&config.Config{})
	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed == nil || refreshed.Metadata == nil {
		t.Fatal("refresh returned no auth")
	}
	if !strings.HasPrefix(refreshed.Metadata["access_token"].(string), "ey") {
		t.Fatalf("access token not rotated: %v", refreshed.Metadata["access_token"])
	}
	if refreshed.Metadata["refresh_token"] != "rotated-refresh-token" {
		t.Fatalf("refresh token not rotated: %v", refreshed.Metadata["refresh_token"])
	}
	if plan, _ := refreshed.Metadata["plan"].(string); plan != "pro" {
		t.Fatalf("plan not updated: %v", refreshed.Metadata["plan"])
	}
	expired, _ := refreshed.Metadata["expired"].(string)
	if expired == "" {
		t.Fatal("expired timestamp not recorded on refresh")
	}
}

func TestMirasimBaseURLRequiresBaseURLForAPIKeys(t *testing.T) {
	exec := NewMirasimExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "mirasim", Attributes: map[string]string{"api_key": "k"}}
	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-sonnet-4", Payload: []byte(`{}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err == nil {
		t.Fatal("api-key mirasim credential without base_url must fail")
	}
}
