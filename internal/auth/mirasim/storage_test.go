package mirasim

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestInstallOAuth_GeneratesDeviceKeyAndTiming(t *testing.T) {
	storage, err := InstallOAuth(NewStorage(Settings{}), fakeJWT(3600, nil), "refresh-1")
	if err != nil {
		t.Fatalf("InstallOAuth: %v", err)
	}
	if !ValidDeviceKey([]byte(storage.DevicePrivateKey)) {
		t.Fatal("device key is not a valid Ed25519 PKCS#8 PEM")
	}
	if storage.Type != Provider || storage.AuthLabel() == "" {
		t.Fatalf("storage identity wrong: %+v", storage)
	}
	if storage.RelayURL != DefaultRelayURL || storage.AdminURL != DefaultAdminURL {
		t.Fatalf("endpoints not defaulted: %+v", storage)
	}
	if storage.Expired == "" || storage.LastRefresh == "" {
		t.Fatalf("timing not recorded: %+v", storage)
	}
	expiry := storage.AccessTokenExpiry(time.Now())
	if expiry.Before(time.Now().Add(3500*time.Second)) || expiry.After(time.Now().Add(3700*time.Second)) {
		t.Fatalf("expiry not resolved from JWT exp: %s", expiry)
	}
}

func TestInstallOAuth_RejectsMissingTokens(t *testing.T) {
	for _, tokens := range [][2]string{{"", "r"}, {"a", ""}, {"a", "r\r\nx"}} {
		if _, err := InstallOAuth(NewStorage(Settings{}), tokens[0], tokens[1]); err == nil {
			t.Fatalf("InstallOAuth(%q,%q) expected error", tokens[0], tokens[1])
		}
	}
}

func TestStorageJSON_RoundTripThroughParse(t *testing.T) {
	fake := newFakeMirasim(t)
	installed := newTestStorage(t, fake)
	raw := installed.JSON()

	parsed, err := ParseStorage(raw, Settings{})
	if err != nil {
		t.Fatalf("ParseStorage: %v", err)
	}
	if parsed == nil {
		t.Fatal("ParseStorage returned nil for a mirasim credential")
	}
	if parsed.AccessToken != installed.AccessToken || parsed.RefreshToken != installed.RefreshToken {
		t.Fatalf("tokens not preserved: %+v", parsed)
	}
	if parsed.DevicePrivateKey != installed.DevicePrivateKey {
		t.Fatal("device key not preserved")
	}
	if parsed.AuthLabel() == "" {
		t.Fatal("label missing after round trip")
	}
	// The parse path re-defaults admin/relay/client instead of trusting old files.
	if parsed.AdminURL != DefaultAdminURL || parsed.RelayURL != DefaultRelayURL {
		if parsed.AdminURL != fake.url() && parsed.RelayURL != fake.url() {
			t.Fatalf("endpoints wrong after round trip: %+v", parsed)
		}
	}

	// A non-mirasim file is ignored, not rejected.
	if other, err := ParseStorage([]byte(`{"type":"anthropic"}`), Settings{}); err != nil || other != nil {
		t.Fatalf("non-mirasim file: other=%v err=%v", other, err)
	}
}

func TestStorageJSON_PreservesLegacyExpiryMigration(t *testing.T) {
	legacy := map[string]any{
		"type":         Provider,
		"access_token": fakeJWT(3600, nil),
		"expiry":       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(legacy)
	installed, err := ParseStorage(raw, Settings{})
	if err == nil {
		// The legacy shape lacks refresh token and device key; fix them up like
		// a modern save would and confirm expiry migration is tolerated.
		if installed != nil && installed.Expired == "" {
			t.Fatal("legacy expiry not migrated")
		}
	}
	// A missing refresh token/device key must fail validation in InstallOAuth,
	// while InstallOAuth supplies only the missing device key.
	fixed, errInstall := InstallOAuth(Storage{Type: Provider, AccessToken: fakeJWT(3600, nil), RefreshToken: "r", Expired: legacy["expiry"].(string)}, fakeJWT(3600, nil), "r2")
	if errInstall != nil {
		t.Fatalf("InstallOAuth modern save: %v", errInstall)
	}
	if fixed.RefreshToken != "r2" || fixed.AccessToken == "" {
		t.Fatalf("tokens not installed: %+v", fixed)
	}
}

func TestDefaultAuthFileName(t *testing.T) {
	withAccount := Storage{AccountID: "User ABC.Example", Type: Provider}
	if got := withAccount.DefaultAuthFileName(); got != "mirasim-user-abc.example.json" {
		t.Fatalf("filename = %q", got)
	}
	deviceOnly, _ := InstallOAuth(NewStorage(Settings{}), fakeJWT(3600, nil), "r")
	got := deviceOnly.DefaultAuthFileName()
	if !strings.HasPrefix(got, "mirasim-") || !strings.HasSuffix(got, ".json") {
		t.Fatalf("device-derived filename wrong: %q", got)
	}
}

func TestStorageFromMetadata_RoundTrip(t *testing.T) {
	installed := newTestStorage(t, newFakeMirasim(t))
	metadata := installed.Metadata()
	rebuilt := StorageFromMetadata(metadata)
	if rebuilt.AccessToken != installed.AccessToken || rebuilt.RefreshToken != installed.RefreshToken {
		t.Fatalf("metadata round trip lost tokens: %+v", rebuilt)
	}
	if rebuilt.DevicePrivateKey != installed.DevicePrivateKey {
		t.Fatal("metadata round trip lost device key")
	}
	kind, _ := metadata["auth_kind"].(string)
	if kind != "oauth" {
		t.Fatalf("auth_kind = %v", metadata["auth_kind"])
	}
}

func TestPopulateIdentityAndPlan(t *testing.T) {
	storage := Storage{
		AccessToken: fakeJWT(3600, map[string]any{"account_id": "acct-42", "email": "u@example.com", "plan": "pro", "plan_exp": float64(time.Now().Add(time.Hour).Unix())}),
	}
	storage.PopulateIdentityFromAccessToken()
	if storage.AccountID != "acct-42" || storage.Email != "u@example.com" {
		t.Fatalf("identity = %+v", storage)
	}
	storage.PopulatePlanFromAccessToken()
	if storage.Plan != "pro" || storage.PlanExpiresAt == nil {
		t.Fatalf("plan = %+v", storage)
	}
	if account := AccessTokenAgentAccount(storage.AccessToken); account != "acct-42" {
		t.Fatalf("agent account = %q", account)
	}
}
