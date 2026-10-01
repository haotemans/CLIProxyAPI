// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): relay signed-request
// protocol (mrs-sig-v2 signature, mrs-seal-v1 metadata sealing).
package mirasim

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

const (
	// SignatureVersion is the mrs-sig-v2 canonical-payload marker.
	SignatureVersion = "mrs-sig-v2"
	// SealVersion is the mrs-seal-v1 envelope marker.
	SealVersion = "mrs-seal-v1"

	defaultSealPublicKeyBase64 = "HlyNMMeGXryasYLJuYQ/9ksCD4AYVVy1zXKAtJdpJn4="

	headerMirasimDevice            = "x-mirasim-device"
	headerMirasimTimestamp         = "x-mirasim-ts"
	headerMirasimNonce             = "x-mirasim-nonce"
	headerMirasimSignature         = "x-mirasim-sig"
	headerMirasimClient            = "x-mirasim-client"
	headerMirasimEncryptedMetadata = "x-mirasim-enc"
	headerMirasimSession           = "x-mirasim-session"
	headerMirasimAgent             = "x-mirasim-agent"
	headerMirasimCall              = "x-mirasim-call"
	headerMirasimAccount           = "x-mirasim-account"
)

// signingInput is the mrs-sig-v2 canonical signing material.
type signingInput struct {
	Method        string
	Path          string
	Timestamp     string
	Nonce         string
	DeviceID      string
	ClientVersion string
	Credential    string
	Metadata      map[string]string
	Body          []byte
}

// canonicalSignaturePayload mirrors the 0.0.260 crypto core. An empty metadata
// set contributes an empty line rather than SHA-256("").
func canonicalSignaturePayload(input signingInput) ([]byte, error) {
	fields := []string{
		strings.ToUpper(strings.TrimSpace(input.Method)),
		input.Path,
		input.Timestamp,
		input.Nonce,
		input.DeviceID,
		input.ClientVersion,
		input.Credential,
	}
	for _, field := range fields {
		if strings.IndexByte(field, 0) >= 0 {
			return nil, fmt.Errorf("Mirasim signature field contains NUL")
		}
	}
	metadataCanonical, err := canonicalMetadata(input.Metadata)
	if err != nil {
		return nil, err
	}
	metadataDigest := ""
	if metadataCanonical != "" {
		metadataDigest = sha256Hex([]byte(metadataCanonical))
	}
	payload := strings.Join([]string{
		SignatureVersion,
		fields[0],
		fields[1],
		fields[2],
		fields[3],
		fields[4],
		fields[5],
		sha256Hex([]byte(fields[6])),
		metadataDigest,
		sha256Hex(input.Body),
	}, "\n")
	return []byte(payload), nil
}

func canonicalMetadata(metadata map[string]string) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	normalized := make(map[string]string, len(metadata))
	for key, value := range metadata {
		key = strings.ToLower(key)
		if value == "" {
			continue
		}
		if strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
			return "", fmt.Errorf("Mirasim metadata contains NUL")
		}
		normalized[key] = value
	}
	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+":"+normalized[key])
	}
	return strings.Join(pairs, "\n"), nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// DeviceIdentity is the parsed Ed25519 device key plus its public identifiers.
type DeviceIdentity struct {
	PrivateKey      ed25519.PrivateKey
	PublicKeyBase64 string
	DeviceID        string
}

// parseDeviceIdentity decodes the persisted device key PEM.
func parseDeviceIdentity(keyPEM string) (*DeviceIdentity, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(keyPEM)))
	if block == nil {
		return nil, fmt.Errorf("decode Mirasim device private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Mirasim device private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("Mirasim device private key is not Ed25519")
	}
	publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return nil, fmt.Errorf("marshal Mirasim device public key: %w", err)
	}
	publicBase64 := base64.StdEncoding.EncodeToString(publicDER)
	digest := sha256.Sum256([]byte(publicBase64))
	return &DeviceIdentity{
		PrivateKey:      append(ed25519.PrivateKey(nil), privateKey...),
		PublicKeyBase64: publicBase64,
		DeviceID:        base64.RawURLEncoding.EncodeToString(digest[:])[:22],
	}, nil
}

// signatureHeaders renders the mrs-sig-v2 headers for one relay request.
func signatureHeaders(identity *DeviceIdentity, clientVersion, method, requestPath, credential string, metadata map[string]string, body []byte) (http.Header, error) {
	nonceBytes := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return nil, fmt.Errorf("generate Mirasim signature nonce: %w", err)
	}
	input := signingInput{
		Method:        method,
		Path:          requestPath,
		Timestamp:     strconv.FormatInt(time.Now().UnixMilli(), 10),
		Nonce:         base64.RawURLEncoding.EncodeToString(nonceBytes),
		DeviceID:      identity.DeviceID,
		ClientVersion: clientVersion,
		Credential:    credential,
		Metadata:      metadata,
		Body:          body,
	}
	payload, err := canonicalSignaturePayload(input)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(identity.PrivateKey, payload)
	headers := make(http.Header, len(metadata)+5)
	for key, value := range metadata {
		if value != "" {
			headers.Set(key, value)
		}
	}
	headers.Set(headerMirasimDevice, input.DeviceID)
	headers.Set(headerMirasimTimestamp, input.Timestamp)
	headers.Set(headerMirasimNonce, input.Nonce)
	headers.Set(headerMirasimSignature, base64.RawURLEncoding.EncodeToString(signature))
	if input.ClientVersion != "" {
		headers.Set(headerMirasimClient, input.ClientVersion)
	}
	return headers, nil
}

// sealRelayHeaders encrypts every x-mirasim-* header except client/version and
// the envelope itself into x-mirasim-enc (mrs-seal-v1: X25519 ephemeral +
// HKDF-SHA256 + ChaCha20-Poly1305, AAD "mrs-seal-v1\nMETHOD\npath").
func sealRelayHeaders(headers http.Header, method, requestPath string) error {
	recipientPublic, err := relaySealPublicKey()
	if err != nil {
		return err
	}
	metadata := make(map[string]string)
	sealedNames := make([]string, 0)
	for name := range headers {
		lowerName := strings.ToLower(name)
		if !isSealedRelayHeader(lowerName) {
			continue
		}
		value := headers.Get(name)
		if value == "" {
			continue
		}
		metadata[lowerName] = value
		sealedNames = append(sealedNames, name)
	}
	if len(metadata) == 0 {
		return nil
	}
	plaintext, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode Mirasim relay metadata: %w", err)
	}
	ephemeralSecret := make([]byte, curve25519.ScalarSize)
	if _, err := io.ReadFull(rand.Reader, ephemeralSecret); err != nil {
		return fmt.Errorf("generate Mirasim seal key: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate Mirasim seal nonce: %w", err)
	}
	aad := []byte(strings.Join([]string{SealVersion, strings.ToUpper(strings.TrimSpace(method)), requestPath}, "\n"))
	sealed, err := sealPayload(recipientPublic, ephemeralSecret, nonce, plaintext, aad)
	if err != nil {
		return err
	}
	for _, name := range sealedNames {
		headers.Del(name)
	}
	headers.Set(headerMirasimEncryptedMetadata, base64.RawURLEncoding.EncodeToString(sealed))
	return nil
}

func isSealedRelayHeader(lowerName string) bool {
	return strings.HasPrefix(lowerName, "x-mirasim-") &&
		lowerName != headerMirasimClient &&
		lowerName != headerMirasimEncryptedMetadata
}

func relaySealPublicKey() ([]byte, error) {
	encoded := strings.TrimSpace(os.Getenv("MIRASIM_SEAL_PUBKEY"))
	if encoded == "" {
		encoded = defaultSealPublicKeyBase64
	}
	var publicKey []byte
	var errDecode error
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		publicKey, errDecode = encoding.DecodeString(encoded)
		if errDecode == nil {
			break
		}
	}
	if errDecode != nil {
		return nil, fmt.Errorf("MIRASIM_SEAL_PUBKEY is not valid base64")
	}
	if len(publicKey) != curve25519.PointSize {
		return nil, fmt.Errorf("MIRASIM_SEAL_PUBKEY must decode to 32 bytes, got %d", len(publicKey))
	}
	return publicKey, nil
}

func sealPayload(recipientPublic, ephemeralSecret, nonce, plaintext, aad []byte) ([]byte, error) {
	if len(recipientPublic) != curve25519.PointSize {
		return nil, fmt.Errorf("Mirasim relay seal public key must be 32 bytes")
	}
	if len(ephemeralSecret) != curve25519.ScalarSize {
		return nil, fmt.Errorf("Mirasim ephemeral seal key must be 32 bytes")
	}
	if len(nonce) != chacha20poly1305.NonceSize {
		return nil, fmt.Errorf("Mirasim seal nonce must be 12 bytes")
	}
	ephemeralPublic, err := curve25519.X25519(ephemeralSecret, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive Mirasim ephemeral public key: %w", err)
	}
	sharedSecret, err := curve25519.X25519(ephemeralSecret, recipientPublic)
	if err != nil {
		return nil, fmt.Errorf("derive Mirasim relay shared key: %w", err)
	}
	key, err := hkdf.Key(sha256.New, sharedSecret, ephemeralPublic, SealVersion, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("derive Mirasim relay seal key: %w", err)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("initialize Mirasim relay seal: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	packed := make([]byte, 0, len(ephemeralPublic)+len(nonce)+len(ciphertext))
	packed = append(packed, ephemeralPublic...)
	packed = append(packed, nonce...)
	packed = append(packed, ciphertext...)
	return packed, nil
}

// AgentForRequest mirrors the official client's agent marker per request
// path, with /v1/messages model-family overrides read from the request body.
func AgentForRequest(requestPath string, body []byte) string {
	if strings.HasPrefix(requestPath, "/v1/messages") {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &payload); err == nil {
			model := strings.ToLower(strings.TrimSpace(payload.Model))
			switch {
			case strings.HasPrefix(model, "deepseek-"):
				return "dsh"
			case strings.HasPrefix(model, "glm-"):
				return "zcode"
			case strings.HasPrefix(model, "kimi-"):
				return "kimi"
			}
		}
	}
	return relayAgent(requestPath)
}

func relayAgent(requestPath string) string {
	if strings.HasPrefix(requestPath, "/v1/responses") || strings.HasPrefix(requestPath, "/v1/alpha/search") || strings.HasPrefix(requestPath, "/v1/images/") {
		return "codex"
	}
	return "claude"
}

// NewCallID returns the per-attempt x-mirasim-call identifier.
func NewCallID() string {
	id, err := randomRelayID(rand.Reader, "")
	if err != nil {
		return fmt.Sprintf("mirasim-call-%d", time.Now().UnixNano())
	}
	return id
}

func randomRelayID(source io.Reader, prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	uuid := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	return prefix + uuid, nil
}

// buildRelayEndpoint joins the relay base URL with a request path and query,
// returning the full URL and the signature path (path only).
func buildRelayEndpoint(relayURL, requestPath string, query url.Values) (string, string, error) {
	base, err := url.Parse(strings.TrimSpace(relayURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", "", fmt.Errorf("invalid Mirasim relay URL %q", relayURL)
	}
	requestPath = "/" + strings.TrimLeft(strings.TrimSpace(requestPath), "/")
	base.Path = strings.TrimRight(base.Path, "/") + requestPath
	base.RawPath = ""
	base.RawQuery = query.Encode()
	return base.String(), base.Path, nil
}
