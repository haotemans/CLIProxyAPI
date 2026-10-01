package modelprobe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// relayWire emulates one OpenAI chat-completions relay provider's upstream
// with per-scenario responses; it captures the probed request to prove the
// driver issues the minimal OpenAI-shaped probe through the credential's
// base-url with its key.
type relayWire struct {
	t *testing.T

	server *httptest.Server

	status   int
	answer   string
	seenAuth string
	seenBody []byte
	seenUA   string
}

func newRelayWire(t *testing.T, status int, answer string) *relayWire {
	t.Helper()
	wire := &relayWire{t: t, status: status, answer: answer}
	wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.seenAuth = r.Header.Get("Authorization")
		wire.seenUA = r.Header.Get("User-Agent")
		wire.seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(wire.status)
		_, _ = w.Write([]byte(wire.answer))
	}))
	t.Cleanup(wire.server.Close)
	return wire
}

const relayProbeRequestIDs = "deepseek/deepseek-v4.1-flash"

func relayAuthFor(t *testing.T, provider, baseURL string) *cliproxyauth.Auth {
	t.Helper()
	return &cliproxyauth.Auth{
		ID:       provider + "-wire.json",
		Provider: provider,
		Attributes: map[string]string{
			"api_key":  provider + "-key",
			"base_url": baseURL,
		},
	}
}

func engineWithFactories(t *testing.T) *Engine {
	t.Helper()
	engine := NewEngine(&config.Config{}, Options{NowFunc: func() time.Time { return time.Unix(0, 0) }})
	return engine
}

func TestRelayProbeDrivers_OutcomeMatrix(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		status    int
		answer    string
		want      Status
		catalogue string
	}{
		{"commandcode usable 200", "commandcode", http.StatusOK, `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, StatusUsable, relayProbeRequestIDs},
		{"commandcode quota 429", "commandcode", http.StatusTooManyRequests, `{"error":"quota exceeded"}`, StatusLimited, relayProbeRequestIDs},
		{"commandcode auth 401", "commandcode", http.StatusUnauthorized, `{"error":"bad key"}`, StatusAuthError, relayProbeRequestIDs},
		{"commandcode unknown model 404", "commandcode", http.StatusNotFound, `{"error":"model not listed for this key"}`, StatusNotAvailable, relayProbeRequestIDs},
		{"commandcode upstream 502", "commandcode", http.StatusBadGateway, `{"error":"bad gateway"}`, StatusUnreachable, relayProbeRequestIDs},
		{"opencode-go usable 200", "opencode-go", http.StatusOK, `{"id":"g","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, StatusUsable, "kimi-k3"},
		{"opencode-go tier deny text", "opencode-go", http.StatusForbidden, `{"error":"model not available in your plan"}`, StatusNotAvailable, "kimi-k3"},
		{"opencode-go auth 403", "opencode-go", http.StatusForbidden, `{"error":"forbidden for this account"}`, StatusAuthError, "kimi-k3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := newRelayWire(t, tc.status, tc.answer)
			engine := engineWithFactories(t)
			auth := relayAuthFor(t, tc.provider, wire.server.URL)
			outcome := engine.ProbeOne(context.Background(), auth, tc.provider, tc.catalogue)
			if outcome.Status != tc.want {
				t.Fatalf("status = %q, want %q (err %q)", outcome.Status, tc.want, outcome.Error)
			}
			if wire.seenAuth != "Bearer "+tc.provider+"-key" {
				t.Fatalf("Authorization = %q", wire.seenAuth)
			}
			if wire.seenUA == "" {
				t.Fatal("missing User-Agent on probe request")
			}
			body := string(wire.seenBody)
			if !strings.Contains(body, tc.catalogue) {
				t.Fatalf("probe body missing model %q: %s", tc.catalogue, body)
			}
		})
	}
}

// Fake catalog feeds: the probe engine probes candidates from the resources
// registries' static lists for these providers by default — prove the
// catalogs stay sane (models that make sense to probe).
func TestRelayCatalog_StaticModelsAreCandidates(t *testing.T) {
	cc := registry.GetCommandcodeModels()
	if len(cc) == 0 {
		t.Fatal("commandcode static model list is empty")
	}
	og := registry.GetOpencodeGoModels()
	if len(og) == 0 {
		t.Fatal("opencode-go static model list is empty")
	}
}

func TestRelayProbeCredentialCycle(t *testing.T) {
	// Full CredentialCycle with the real driver — a success+deny mix must
	// prune the denied model and keep the usable one, catalog_size recorded.
	wire := &relayWireT{t: t}
	wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "not-a-model") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model not listed for this key"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(wire.server.Close)

	engine := engineWithFactories(t)
	auth := relayAuthFor(t, "commandcode", wire.server.URL)
	section := engine.CredentialCycle(context.Background(), auth, "commandcode", []string{"deepseek/deepseek-v4.1-flash", "not-a-model"})
	if section == nil {
		t.Fatal("cycle produced no section")
	}
	if section.CatalogSize != 2 {
		t.Fatalf("catalog_size = %d, want 2", section.CatalogSize)
	}
	if len(section.Usable) != 1 || section.Usable[0] != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("usable = %+v", section.Usable)
	}
	if len(section.Pruned) != 1 || section.Pruned[0] != "not-a-model" {
		t.Fatalf("pruned = %+v, want [not-a-model]", section.Pruned)
	}
}

type relayWireT struct {
	t      *testing.T
	server *httptest.Server
}
