package modelprobe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func mirasimPreflightAuth(t *testing.T, relayURL string) *cliproxyauth.Auth {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("device key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal device key: %v", err)
	}
	return &cliproxyauth.Auth{
		ID:       "mirasim-preflight.json",
		Provider: "mirasim",
		Metadata: map[string]any{
			"access_token":       "at",
			"device_private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
			"relay_url":          relayURL,
			"plan":               "go",
		},
	}
}

func limitsWire(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/limits" || r.Header.Get("x-mirasim-probe") != "usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const limitsExhaustedBody = `{"windows":[{"name":"kimi-k3","budget":40,"used":40,"model_scoped":true}]}`
const limitsHealthyBody = `{"windows":[{"name":"kimi-k3","budget":40,"used":12,"model_scoped":true}]}`

func TestPreflightMarksExhaustedModelLimitedWithoutCall(t *testing.T) {
	server := limitsWire(t, 200, limitsExhaustedBody)
	mock := &captureMockExec{}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"mirasim": mock},
	})
	auth := mirasimPreflightAuth(t, server.URL)
	outcome := engine.ProbeOne(context.Background(), auth, "mirasim", "kimi-k3")
	if outcome.Status != StatusLimited {
		t.Fatalf("outcome = %+v, want limited (window exhausted)", outcome)
	}
	if !strings.Contains(outcome.Error, "/v1/limits") {
		t.Fatalf("error must cite the limits lane: %q", outcome.Error)
	}
	if mock.calls != 0 {
		t.Fatalf("preflight must skip the model call, got %d", mock.calls)
	}
}

func TestPreflightHealthyAndFailOpenProceedToProbe(t *testing.T) {
	// Healthy window: the inference probe decides (executor runs).
	server := limitsWire(t, 200, limitsHealthyBody)
	mock := &captureMockExec{}
	engine := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"mirasim": mock},
	})
	auth := mirasimPreflightAuth(t, server.URL)
	outcome := engine.ProbeOne(context.Background(), auth, "mirasim", "kimi-k3")
	if outcome.Status != StatusUsable || mock.calls != 1 {
		t.Fatalf("healthy window must fall through to the probe: outcome=%+v calls=%d", outcome, mock.calls)
	}

	// Limits lane down: fail open, inference probe still runs.
	serverDown := limitsWire(t, 500, `{"error":"down"}`)
	engineDown := NewEngine(nil, Options{
		MaxParallel:     1,
		DriverOverrides: map[string]RequestExecutor{"mirasim": mock},
	})
	outcome = engineDown.ProbeOne(context.Background(), auth, "mirasim", "kimi-k3")
	_ = serverDown
	if mock.calls != 2 {
		t.Fatalf("limits outage must not block probing: calls=%d", mock.calls)
	}
	_ = outcome
}
