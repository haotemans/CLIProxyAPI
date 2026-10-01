// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): credential storage model.
package mirasim

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StorageVersion is the persisted credential schema revision.
const StorageVersion = 1

// opaqueAccessTokenLifetime is the conservative expiry fallback for opaque
// access tokens (no expires_in, no JWT exp).
const opaqueAccessTokenLifetime = 30 * time.Minute

// Storage is the mirasim OAuth credential persisted under auth-dir.
type Storage struct {
	StorageVersion   int            `json:"storage_version,omitempty"`
	Type             string         `json:"type"`
	AccessToken      string         `json:"access_token,omitempty"`
	RefreshToken     string         `json:"refresh_token,omitempty"`
	Expired          string         `json:"expired,omitempty"`
	LastRefresh      string         `json:"last_refresh,omitempty"`
	AccountID        string         `json:"account_id,omitempty"`
	Email            string         `json:"email,omitempty"`
	Plan             string         `json:"plan,omitempty"`
	PlanExpiresAt    *int64         `json:"plan_exp,omitempty"`
	ProfileCheckedAt string         `json:"profile_checked_at,omitempty"`
	DevicePrivateKey string         `json:"device_private_key,omitempty"`
	RelayURL         string         `json:"relay_url,omitempty"`
	AdminURL         string         `json:"admin_url,omitempty"`
	ClientVersion    string         `json:"client_version,omitempty"`
	Raw              map[string]any `json:"-"`
}

// NewStorage returns empty storage with the configured endpoints applied.
func NewStorage(settings Settings) Storage {
	settings = settings.Normalize()
	return Storage{
		StorageVersion: StorageVersion,
		Type:           Provider,
		RelayURL:       settings.RelayURL,
		AdminURL:       settings.AdminURL,
		ClientVersion:  settings.ClientVersion,
	}
}

// ParseStorage decodes and validates a mirasim auth file. It returns nil
// without error when the file is not a mirasim credential.
func ParseStorage(raw []byte, defaults Settings) (*Storage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decode Mirasim auth: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(stringValue(probe["type"])), Provider) {
		return nil, nil
	}
	if err := migrateStorageMap(probe); err != nil {
		return nil, err
	}
	normalizedRaw, err := json.Marshal(probe)
	if err != nil {
		return nil, fmt.Errorf("normalize Mirasim auth: %w", err)
	}
	var storage Storage
	if err := json.Unmarshal(normalizedRaw, &storage); err != nil {
		return nil, fmt.Errorf("decode Mirasim auth: %w", err)
	}
	storage.Raw = probe
	settings := defaults.Normalize()
	storage.AccessToken = strings.TrimSpace(storage.AccessToken)
	storage.RefreshToken = strings.TrimSpace(storage.RefreshToken)
	if strings.TrimSpace(storage.RelayURL) == "" {
		storage.RelayURL = settings.RelayURL
	}
	if strings.TrimSpace(storage.AdminURL) == "" {
		storage.AdminURL = settings.AdminURL
	}
	// Client version describes this executable's protocol, not the account;
	// old snapshots must not pin upgraded code to an obsolete version.
	storage.ClientVersion = settings.ClientVersion
	storage.RelayURL = strings.TrimRight(storage.RelayURL, "/")
	storage.AdminURL = strings.TrimRight(storage.AdminURL, "/")
	if storage.Expired == "" {
		storage.Expired = ResolveAccessTokenExpiry(storage.AccessToken, 0, time.Now()).Format(time.RFC3339)
	}
	storage.PopulatePlanFromAccessToken()
	if err := storage.Validate(); err != nil {
		return nil, err
	}
	return &storage, nil
}

// migrateStorageMap upgrades only self-contained OAuth JSON. The obsolete
// credential-directory representation is rejected rather than imported.
func migrateStorageMap(values map[string]any) error {
	version, err := parseStorageVersion(values["storage_version"])
	if err != nil {
		return fmt.Errorf("decode Mirasim auth storage_version: %w", err)
	}
	if version > StorageVersion {
		return fmt.Errorf("unsupported Mirasim auth storage_version %d (current %d)", version, StorageVersion)
	}
	if strings.TrimSpace(stringValue(values["expired"])) == "" {
		if legacyExpiry := strings.TrimSpace(stringValue(values["expiry"])); legacyExpiry != "" {
			values["expired"] = legacyExpiry
		}
	}
	delete(values, "expiry")
	delete(values, "credential_dir")
	delete(values, "credential_mode")
	values["storage_version"] = StorageVersion
	values["auth_kind"] = "oauth"
	return nil
}

func parseStorageVersion(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case float64:
		version := int(typed)
		if typed != float64(version) || version < 0 {
			return 0, fmt.Errorf("must be a non-negative integer")
		}
		return version, nil
	case json.Number:
		version, err := strconv.Atoi(string(typed))
		if err != nil || version < 0 {
			return 0, fmt.Errorf("must be a non-negative integer")
		}
		return version, nil
	default:
		return 0, fmt.Errorf("must be a non-negative integer")
	}
}

// InstallOAuth builds the self-contained storage after a login: it persists
// the OAuth tokens and a freshly generated Ed25519 device identity (unless a
// credential file already supplies one).
func InstallOAuth(storage Storage, accessToken, refreshToken string) (Storage, error) {
	var err error
	if accessToken, err = normalizeStoredSecret("access token", accessToken); err != nil {
		return Storage{}, err
	}
	if refreshToken, err = normalizeStoredSecret("refresh token", refreshToken); err != nil {
		return Storage{}, err
	}
	keyPEM := strings.TrimSpace(storage.DevicePrivateKey)
	if keyPEM == "" || !ValidDeviceKey([]byte(keyPEM)) {
		generated, err := NewDeviceKey()
		if err != nil {
			return Storage{}, err
		}
		keyPEM = strings.TrimSpace(string(generated))
	}
	storage.AccessToken = accessToken
	storage.RefreshToken = refreshToken
	storage.DevicePrivateKey = keyPEM
	storage.RecordTokenTiming(accessToken, 0, time.Now())
	storage.PopulatePlanFromAccessToken()
	if err := storage.Validate(); err != nil {
		return Storage{}, err
	}
	return storage, nil
}

func normalizeStoredSecret(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("Mirasim %s is missing", label)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("Mirasim %s contains an invalid control character", label)
	}
	if len(value) > 64<<10 {
		return "", fmt.Errorf("Mirasim %s is unexpectedly large", label)
	}
	return value, nil
}

// NewDeviceKey generates the Ed25519 device identity persisted per credential.
func NewDeviceKey() ([]byte, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Mirasim device private key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal Mirasim device private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ValidDeviceKey reports whether raw holds one Ed25519 PKCS#8 PEM block.
func ValidDeviceKey(raw []byte) bool {
	block, rest := pem.Decode(raw)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return false
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	return ok && len(privateKey) == ed25519.PrivateKeySize
}

// DeviceFingerprint derives the stable, collision-resistant identity a
// credential file is named after when the token carries no account claims.
func DeviceFingerprint(keyPEM string) string {
	block, _ := pem.Decode([]byte(strings.TrimSpace(keyPEM)))
	if block != nil {
		if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if privateKey, ok := parsed.(ed25519.PrivateKey); ok {
				if publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public()); err == nil {
					digest := sha256.Sum256(publicDER)
					return base64.RawURLEncoding.EncodeToString(digest[:12])
				}
			}
		}
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(keyPEM)))
	return base64.RawURLEncoding.EncodeToString(digest[:12])
}

// Validate checks the complete in-memory credential without touching disk.
func (s Storage) Validate() error {
	if _, err := normalizeStoredSecret("access token", s.AccessToken); err != nil {
		return err
	}
	if _, err := normalizeStoredSecret("refresh token", s.RefreshToken); err != nil {
		return err
	}
	if strings.TrimSpace(s.DevicePrivateKey) == "" {
		return fmt.Errorf("Mirasim device private key is missing")
	}
	if !ValidDeviceKey([]byte(strings.TrimSpace(s.DevicePrivateKey))) {
		return fmt.Errorf("Mirasim device private key is not a valid Ed25519 PKCS#8 PEM")
	}
	return nil
}

// JSON renders the persisted credential file body.
func (s Storage) JSON() []byte {
	out := make(map[string]any, len(s.Raw)+12)
	for k, v := range s.Raw {
		out[k] = v
	}
	delete(out, "credential_dir")
	delete(out, "credential_mode")
	delete(out, "expiry")
	out["storage_version"] = StorageVersion
	out["type"] = Provider
	setOrDelete(out, "access_token", strings.TrimSpace(s.AccessToken))
	setOrDelete(out, "refresh_token", strings.TrimSpace(s.RefreshToken))
	setOrDelete(out, "expired", strings.TrimSpace(s.Expired))
	setOrDelete(out, "last_refresh", strings.TrimSpace(s.LastRefresh))
	setOrDelete(out, "account_id", strings.TrimSpace(s.AccountID))
	setOrDelete(out, "email", strings.TrimSpace(s.Email))
	setOrDelete(out, "plan", strings.TrimSpace(s.Plan))
	if s.PlanExpiresAt == nil {
		delete(out, "plan_exp")
	} else {
		out["plan_exp"] = *s.PlanExpiresAt
	}
	setOrDelete(out, "profile_checked_at", strings.TrimSpace(s.ProfileCheckedAt))
	setOrDelete(out, "device_private_key", strings.TrimSpace(s.DevicePrivateKey))
	setOrDelete(out, "relay_url", strings.TrimSpace(s.RelayURL))
	setOrDelete(out, "admin_url", strings.TrimSpace(s.AdminURL))
	setOrDelete(out, "client_version", strings.TrimSpace(s.ClientVersion))
	out["auth_kind"] = "oauth"
	raw, _ := json.Marshal(out)
	return raw
}

// Metadata renders the runtime metadata snapshot carried on the auth record.
func (s Storage) Metadata() map[string]any {
	metadata := map[string]any{
		"storage_version":          StorageVersion,
		"type":                     Provider,
		"auth_kind":                "oauth",
		"access_token":             strings.TrimSpace(s.AccessToken),
		"refresh_token":            strings.TrimSpace(s.RefreshToken),
		"expired":                  strings.TrimSpace(s.Expired),
		"last_refresh":             strings.TrimSpace(s.LastRefresh),
		"account_id":               strings.TrimSpace(s.AccountID),
		"email":                    strings.TrimSpace(s.Email),
		"plan":                     strings.TrimSpace(s.Plan),
		"profile_checked_at":       strings.TrimSpace(s.ProfileCheckedAt),
		"device_private_key":       s.DevicePrivateKey,
		"relay_url":                strings.TrimSpace(s.RelayURL),
		"admin_url":                strings.TrimSpace(s.AdminURL),
		"client_version":           strings.TrimSpace(s.ClientVersion),
		"refresh_interval_seconds": int64(ProfileRefreshInterval / time.Second),
	}
	if s.PlanExpiresAt != nil {
		metadata["plan_exp"] = *s.PlanExpiresAt
	}
	return metadata
}

// StorageFromMetadata rebuilds storage from an auth record's metadata
// (used by the executor and refresh paths, which never read the file).
func StorageFromMetadata(metadata map[string]any) Storage {
	get := func(key string) string {
		if v, ok := metadata[key].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	var planExp *int64
	switch v := metadata["plan_exp"].(type) {
	case float64:
		n := int64(v)
		planExp = &n
	case json.Number:
		if n, err := v.Int64(); err == nil {
			planExp = &n
		}
	}
	return Storage{
		StorageVersion:   StorageVersion,
		Type:             Provider,
		AccessToken:      get("access_token"),
		RefreshToken:     get("refresh_token"),
		Expired:          get("expired"),
		LastRefresh:      get("last_refresh"),
		AccountID:        get("account_id"),
		Email:            get("email"),
		Plan:             get("plan"),
		PlanExpiresAt:    planExp,
		ProfileCheckedAt: get("profile_checked_at"),
		DevicePrivateKey: get("device_private_key"),
		RelayURL:         get("relay_url"),
		AdminURL:         get("admin_url"),
		ClientVersion:    get("client_version"),
	}
}

// NormalizeEndpoints defaults empty endpoint fields (executor-side safety).
func (s *Storage) NormalizeEndpoints() {
	if strings.TrimSpace(s.RelayURL) == "" {
		s.RelayURL = DefaultRelayURL
	}
	if strings.TrimSpace(s.AdminURL) == "" {
		s.AdminURL = DefaultAdminURL
	}
	if strings.TrimSpace(s.ClientVersion) == "" {
		s.ClientVersion = DefaultClientVersion
	}
	s.RelayURL = strings.TrimRight(s.RelayURL, "/")
	s.AdminURL = strings.TrimRight(s.AdminURL, "/")
}

// RecordTokenTiming stores the conventional OAuth timestamps after login or
// refresh: expires_in wins, then JWT exp, then the opaque fallback.
func (s *Storage) RecordTokenTiming(accessToken string, expiresIn int64, now time.Time) {
	if s == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	s.Expired = ResolveAccessTokenExpiry(accessToken, expiresIn, now).Format(time.RFC3339)
	s.LastRefresh = now.Format(time.RFC3339)
}

// ResolveAccessTokenExpiry chooses an expiry without treating an opaque token
// as immediately expired, which would otherwise cause a refresh loop.
func ResolveAccessTokenExpiry(accessToken string, expiresIn int64, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	if expiresIn > 0 {
		return now.Add(time.Duration(expiresIn) * time.Second)
	}
	if expiry := jwtExpiry(accessToken); !expiry.IsZero() {
		return expiry.UTC()
	}
	return now.Add(opaqueAccessTokenLifetime)
}

// AccessTokenExpiry returns the persisted expiry, or derives one when absent.
func (s Storage) AccessTokenExpiry(now time.Time) time.Time {
	if parsed, ok := parseTimestamp(s.Expired); ok {
		return parsed
	}
	return ResolveAccessTokenExpiry(s.AccessToken, 0, now)
}

// PopulateIdentityFromAccessToken copies stable account fields from the token
// (naming/labels only; the token passes relay validation before persisting).
func (s *Storage) PopulateIdentityFromAccessToken() {
	if s == nil {
		return
	}
	claims := jwtClaims(s.AccessToken)
	if strings.TrimSpace(s.AccountID) == "" {
		s.AccountID = firstClaimString(claims, "account_id", "accountId", "user_id", "userId", "sub")
	}
	if strings.TrimSpace(s.Email) == "" {
		s.Email = firstClaimString(claims, "email")
	}
	s.AccountID = strings.TrimSpace(s.AccountID)
	s.Email = strings.TrimSpace(s.Email)
}

// PopulatePlanFromAccessToken seeds plan metadata for a fresh or older auth
// record; a successful /auth/me profile check remains authoritative.
func (s *Storage) PopulatePlanFromAccessToken() {
	if s == nil || strings.TrimSpace(s.Plan) != "" {
		return
	}
	plan, expiresAt := AccessTokenPlan(s.AccessToken)
	if plan == "" {
		return
	}
	s.Plan = plan
	if expiresAt != nil {
		value := *expiresAt
		s.PlanExpiresAt = &value
	}
}

// AccessTokenPlan returns the Mirasim plan claims carried by a JWT. Opaque or
// malformed tokens simply have no locally observable plan.
func AccessTokenPlan(token string) (string, *int64) {
	claims := jwtClaims(token)
	plan, _ := claims["plan"].(string)
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return "", nil
	}
	var expiresAt int64
	switch value := claims["plan_exp"].(type) {
	case float64:
		if value > 0 && value == float64(int64(value)) {
			expiresAt = int64(value)
		}
	case json.Number:
		expiresAt, _ = value.Int64()
	}
	if expiresAt <= 0 {
		return plan, nil
	}
	return plan, &expiresAt
}

// AccessTokenAgentAccount returns the sub-account a relay request belongs to
// (x-mirasim-account), empty when the token names none.
func AccessTokenAgentAccount(token string) string {
	return firstClaimString(jwtClaims(token), "account_id", "accountId")
}

// RecordProfile persists the non-secret account state returned by /auth/me.
func (s *Storage) RecordProfile(email, plan string, planExpiresAt *int64, checkedAt time.Time) {
	if s == nil {
		return
	}
	if email = strings.TrimSpace(email); email != "" {
		s.Email = email
	}
	if plan = strings.TrimSpace(plan); plan != "" {
		s.Plan = plan
		if planExpiresAt != nil {
			value := *planExpiresAt
			s.PlanExpiresAt = &value
		}
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	s.ProfileCheckedAt = checkedAt.UTC().Format(time.RFC3339)
}

// ProfileCheckTime reports the last successful /auth/me observation.
func (s Storage) ProfileCheckTime() time.Time {
	parsed, _ := parseTimestamp(s.ProfileCheckedAt)
	return parsed
}

// DefaultAuthFileName mirrors the provider's account-specific OAuth files; a
// claim-less token falls back to the generated device identity.
func (s Storage) DefaultAuthFileName() string {
	identity := strings.TrimSpace(s.AccountID)
	if identity == "" {
		identity = strings.ToLower(strings.TrimSpace(s.Email))
	}
	component := safeFileComponent(identity)
	if component == "" {
		component = DeviceFingerprint(s.DevicePrivateKey)
	}
	return "mirasim-" + component + ".json"
}

// AuthLabel renders a compact human label for the credential.
func (s Storage) AuthLabel() string {
	if email := strings.TrimSpace(s.Email); email != "" {
		return "Mirasim (" + email + ")"
	}
	if accountID := strings.TrimSpace(s.AccountID); accountID != "" {
		return "Mirasim (" + abbreviatedIdentity(accountID) + ")"
	}
	return "Mirasim (device " + abbreviatedIdentity(DeviceFingerprint(s.DevicePrivateKey)) + ")"
}

func safeFileComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' || char == '@'
		if allowed {
			builder.WriteRune(char)
		} else if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
			builder.WriteByte('-')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	return strings.Trim(builder.String(), ".-_")
}

func abbreviatedIdentity(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil
	}
	return claims
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return time.Time{}
	}
	seconds, err := claims.ExpiresAt.Int64()
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func firstClaimString(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func setOrDelete(values map[string]any, key, value string) {
	if value == "" {
		delete(values, key)
		return
	}
	values[key] = value
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
