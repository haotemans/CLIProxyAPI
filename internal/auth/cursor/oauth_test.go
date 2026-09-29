package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE error: %v", err)
	}
	if verifier == "" {
		t.Fatal("verifier is empty")
	}
	// 96 bytes raw URL-safe base64 (no padding) = 128 chars.
	if len(verifier) != 128 {
		t.Fatalf("verifier length = %d, want 128", len(verifier))
	}
	// challenge must be the base64url sha256 of the verifier.
	h := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(h[:])
	if challenge != want {
		t.Fatalf("challenge = %q, want %q", challenge, want)
	}

	verifier2, challenge2, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE second call error: %v", err)
	}
	if verifier == verifier2 || challenge == challenge2 {
		t.Fatal("two GeneratePKCE calls produced identical values")
	}
}

func TestGenerateAuthParams(t *testing.T) {
	params, err := GenerateAuthParams()
	if err != nil {
		t.Fatalf("GenerateAuthParams error: %v", err)
	}
	if params.Verifier == "" || params.Challenge == "" {
		t.Fatal("verifier/challenge missing")
	}
	if !strings.Contains(params.LoginURL, "challenge="+params.Challenge) {
		t.Fatalf("LoginURL %q missing challenge", params.LoginURL)
	}
	if !strings.Contains(params.LoginURL, "uuid="+params.UUID) {
		t.Fatalf("LoginURL %q missing uuid", params.LoginURL)
	}
	if !strings.Contains(params.LoginURL, "redirectTarget=cli") {
		t.Fatalf("LoginURL %q missing redirectTarget=cli", params.LoginURL)
	}
	// UUID shape: 8-4-4-4-12 hex groups.
	parts := strings.Split(params.UUID, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID %q does not have 5 groups", params.UUID)
	}
	wantLens := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != wantLens[i] {
			t.Fatalf("UUID group %d = %q (len %d), want len %d", i, part, len(part), wantLens[i])
		}
	}
}

// makeTestJWT builds an unsigned JWT with the given claims payload.
func makeTestJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestParseJWTSub(t *testing.T) {
	token := makeTestJWT(map[string]any{"sub": "auth0|user_2xABC", "exp": 1893456000})
	if got := ParseJWTSub(token); got != "auth0|user_2xABC" {
		t.Fatalf("ParseJWTSub = %q, want %q", got, "auth0|user_2xABC")
	}
	if got := ParseJWTSub("not-a-jwt"); got != "" {
		t.Fatalf("ParseJWTSub(invalid) = %q, want empty", got)
	}
	if got := ParseJWTSub(makeTestJWT(map[string]any{"email": "a@b.c"})); got != "" {
		t.Fatalf("ParseJWTSub(no sub) = %q, want empty", got)
	}
}

func TestSubToShortHash(t *testing.T) {
	got := SubToShortHash("auth0|user_2xABC")
	if len(got) != 8 {
		t.Fatalf("SubToShortHash length = %d, want 8", len(got))
	}
	if got != SubToShortHash("auth0|user_2xABC") {
		t.Fatal("SubToShortHash is not deterministic")
	}
	if got == SubToShortHash("auth0|user_other") {
		t.Fatal("different subs hashed identically")
	}
	if SubToShortHash("") != "" {
		t.Fatal("SubToShortHash(\"\") should be empty")
	}
}

func TestGetTokenExpiry(t *testing.T) {
	// exp = 2030-01-01T00:00:00Z; safety margin subtracts 5 minutes.
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	token := makeTestJWT(map[string]any{"exp": float64(exp)})
	got := GetTokenExpiry(token)
	want := time.Unix(exp, 0).Add(-5 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("GetTokenExpiry = %v, want %v", got, want)
	}

	// Malformed token falls back to roughly one hour from now.
	before := time.Now()
	fallback := GetTokenExpiry("bad.token.value")
	if fallback.Before(before.Add(55*time.Minute)) || fallback.After(before.Add(65*time.Minute)) {
		t.Fatalf("fallback expiry %v not within expected window", fallback)
	}
}

func TestCredentialFileName(t *testing.T) {
	cases := []struct {
		label, subHash, want string
	}{
		{"work", "a3f8b2c1", "cursor.work.json"},
		{"", "a3f8b2c1", "cursor.a3f8b2c1.json"},
		{"", "", "cursor.json"},
		{"  ", "  ", "cursor.json"},
	}
	for _, tc := range cases {
		if got := CredentialFileName(tc.label, tc.subHash); got != tc.want {
			t.Errorf("CredentialFileName(%q, %q) = %q, want %q", tc.label, tc.subHash, got, tc.want)
		}
	}
}

func TestDisplayLabel(t *testing.T) {
	if got := DisplayLabel("work", "a3f8b2c1"); got != "Cursor work" {
		t.Errorf("DisplayLabel label = %q", got)
	}
	if got := DisplayLabel("", "a3f8b2c1"); got != "Cursor a3f8b2c1" {
		t.Errorf("DisplayLabel subHash = %q", got)
	}
	if got := DisplayLabel("", ""); got != "Cursor User" {
		t.Errorf("DisplayLabel default = %q", got)
	}
}

func TestTokenPairJSON(t *testing.T) {
	var tokens TokenPair
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"accessToken":"at","refreshToken":"rt"}`)), &tokens); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tokens.AccessToken != "at" || tokens.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v", tokens)
	}
}
