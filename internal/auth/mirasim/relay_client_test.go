package mirasim

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRelayClient_MintsTicketAndSignsControl(t *testing.T) {
	fake := newFakeMirasim(t)
	storage := newTestStorage(t, fake)
	client, err := NewRelayClient(&storage, "")
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	credential, err := client.Credential(context.Background())
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if credential != "ticket-test" {
		t.Fatalf("credential = %q, want device ticket", credential)
	}
	if fake.SeenMint != 1 {
		t.Fatalf("mint calls = %d, want 1", fake.SeenMint)
	}
	if fake.LastMintAuth == "" || !strings.HasPrefix(fake.LastMintAuth, "Bearer ey") {
		t.Fatalf("mint authorization = %q", fake.LastMintAuth)
	}
	// Cached ticket is reused without another mint.
	if _, err := client.Credential(context.Background()); err != nil {
		t.Fatalf("Credential(2): %v", err)
	}
	if fake.SeenMint != 1 {
		t.Fatalf("mint calls after reuse = %d, want 1", fake.SeenMint)
	}

	raw, err := client.FetchLimits(context.Background())
	if err != nil {
		t.Fatalf("FetchLimits: %v", err)
	}
	if fake.SeenLimits != 1 || fake.LastLimits == nil {
		t.Fatalf("limits not called: %d", fake.SeenLimits)
	}
	// The control call is signed and carries no sealed metadata.
	headers := fake.LastLimits
	for _, key := range []string{"X-Mirasim-Device", "X-Mirasim-Ts", "X-Mirasim-Nonce", "X-Mirasim-Sig", "X-Mirasim-Client"} {
		if headers.Get(key) == "" {
			t.Fatalf("signed control call missing %s: %v", key, headers)
		}
	}
	if headers.Get("X-Mirasim-Enc") != "" {
		t.Fatal("control call must not be sealed")
	}
	if got := headers.Get("Authorization"); got != "Bearer ticket-test" {
		t.Fatalf("control Authorization = %q", got)
	}
	var payload struct {
		Limit float64 `json:"limit"`
	}
	if errJson := json.Unmarshal(raw, &payload); errJson != nil || payload.Limit != 100 {
		t.Fatalf("limits body: %v %s", errJson, raw)
	}

	// Session/Account markers for the request metadata surface.
	if !strings.HasPrefix(client.SessionID(), "mirasim_") {
		t.Fatalf("session id = %q", client.SessionID())
	}
}

func TestRelayClient_Mint404FallsBackToAccessToken(t *testing.T) {
	fake := newFakeMirasim(t)
	fake.TicketStatus = 404
	storage := newTestStorage(t, fake)
	client, err := NewRelayClient(&storage, "")
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	credential, err := client.Credential(context.Background())
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if credential != storage.AccessToken {
		t.Fatalf("fallback credential = %q, want access token", credential)
	}
	// The absent-route quiet window holds: another attempt does not re-mint.
	if _, err := client.Credential(context.Background()); err != nil {
		t.Fatalf("Credential(2): %v", err)
	}
	if fake.SeenMint != 1 {
		t.Fatalf("mint attempts = %d, want 1 (quiet window)", fake.SeenMint)
	}
}

func TestRelayClient_StaleAccessTokenSignalsRefresh(t *testing.T) {
	fake := newFakeMirasim(t)
	storage := newTestStorage(t, fake)
	// Force expiry well into the stale lead.
	storage.AccessToken = strings.Replace(storage.AccessToken, ".c2ln", ".c2ln", 1) // keep shape
	storage.Expired = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	client, err := NewRelayClient(&storage, "")
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	if !client.AccessTokenStale() {
		t.Fatal("expired access token must be stale")
	}
	if _, err := client.Credential(context.Background()); err == nil {
		t.Fatal("credential with expired access token must signal refresh")
	}
}

func TestRelayClient_SignHeadersSealMetadataOnly(t *testing.T) {
	fake := newFakeMirasim(t)
	storage := newTestStorage(t, fake)
	client, err := NewRelayClient(&storage, "")
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}

	// Inference path: metadata present → sealed into x-mirasim-enc.
	metadata := map[string]string{
		"x-mirasim-session": client.SessionID(),
		"x-mirasim-agent":   "claude",
		"x-mirasim-call":    NewCallID(),
	}
	headers, err := client.SignHeaders("POST", "/v1/messages", "cred-1", metadata, []byte(`{"model":"claude-sonnet-4"}`))
	if err != nil {
		t.Fatalf("SignHeaders: %v", err)
	}
	if headers.Get("X-Mirasim-Enc") == "" {
		t.Fatal("inference headers must be sealed")
	}
	if headers.Get("X-Mirasim-Client") == "" || headers.Get("Authorization") != "Bearer cred-1" {
		t.Fatalf("headers wrong: %v", headers)
	}
	// Plain metadata headers are removed by sealing.
	if headers.Get("X-Mirasim-Session") != "" || headers.Get("X-Mirasim-Agent") != "" {
		t.Fatal("sealed metadata must not remain unsealed")
	}

	// Control path: nil metadata → no seal.
	control, err := client.SignHeaders("GET", "/v1/limits", "cred-1", nil, nil)
	if err != nil {
		t.Fatalf("SignHeaders(control): %v", err)
	}
	if control.Get("X-Mirasim-Enc") != "" {
		t.Fatal("control headers must never be sealed")
	}
}

func TestAgentForRequest(t *testing.T) {
	if got := AgentForRequest("/v1/responses", nil); got != "codex" {
		t.Fatalf("responses agent = %q", got)
	}
	cases := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4", "claude"},
		{"deepseek-v3", "dsh"},
		{"glm-5.3", "zcode"},
		{"kimi-k3", "kimi"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]string{"model": tc.model})
		if got := AgentForRequest("/v1/messages", body); got != tc.want {
			t.Errorf("model %s agent = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestValidateRemoteWithStorage(t *testing.T) {
	fake := newFakeMirasim(t)
	storage := newTestStorage(t, fake)
	if err := ValidateRemoteWithStorage(context.Background(), &storage, ""); err != nil {
		t.Fatalf("ValidateRemoteWithStorage: %v", err)
	}
	if fake.SeenModels < 1 {
		t.Fatal("validation skipped /v1/models")
	}
}
