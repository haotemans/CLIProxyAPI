// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): device ticket lifecycle
// and signed relay control calls.
package mirasim

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionResource = "/v1/device/session"
	modelsResource  = "/v1/models"
	limitsResource  = "/v1/limits"

	// accessStaleLead is how close to expiry an access token may get before it
	// is refused for a fresh request (the conductor's scheduled refresh covers
	// the rest; a 30s floor prevents mid-flight expiry).
	accessStaleLead = 30 * time.Second
	// ticketRefreshLead is how far ahead of expiry a device ticket is re-minted.
	ticketRefreshLead = 2 * time.Minute
	ticketDefaultTTL  = 10 * time.Minute
	// ticketRetryFloor keeps successive mint attempts at least this far apart.
	ticketRetryFloor = 10 * time.Second
	// A relay answering 404 or 501 to the mint has no device-session route:
	// fall back to the access token itself (the official client behavior).
	ticketRouteAbsentQuiet   = time.Minute
	ticketUnimplementedQuiet = 15 * time.Minute
)

// RelayClient owns one credential's signed relay state: the device identity,
// the current access token, and the device ticket with its mint lifecycle.
// It is safe for concurrent use by one executor.
type RelayClient struct {
	mu         sync.Mutex
	storage    *Storage
	identity   *DeviceIdentity
	proxyURL   string
	httpClient *http.Client
	now        func() time.Time

	sessionID string

	ticket           string
	ticketExpiresAt  time.Time
	ticketNextMint   time.Time
	ticketLastErr    error
	ticketUnmintable time.Time
	accessTokenStale bool
}

// NewRelayClient builds a client over the storage snapshot held by the caller.
// The storage pointer is read atomically under the client lock; callers update
// tokens through the same pointer after a refresh.
func NewRelayClient(storage *Storage, proxyURL string) (*RelayClient, error) {
	if storage == nil {
		return nil, fmt.Errorf("Mirasim credential storage is missing")
	}
	identity, err := parseDeviceIdentity(storage.DevicePrivateKey)
	if err != nil {
		return nil, err
	}
	client := &RelayClient{
		storage:  storage,
		identity: identity,
		proxyURL: strings.TrimSpace(proxyURL),
		now:      time.Now,
	}
	return client, nil
}

// relayHTTPClient returns the proxy-aware client used for mint/control calls.
func (c *RelayClient) relayHTTPClient() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	client, _, err := newAuthHTTPClient(c.proxyURL, 0)
	if err != nil {
		return http.DefaultClient
	}
	return client
}

// AccessTokenStale reports whether the current access token must be refreshed
// before another request (conductor surfaces this as unauthorized).
func (c *RelayClient) AccessTokenStale() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accessStaleLocked(c.now())
}

func (c *RelayClient) accessStaleLocked(now time.Time) bool {
	if c.accessTokenStale {
		return true
	}
	token := strings.TrimSpace(c.storage.AccessToken)
	if token == "" {
		return true
	}
	expiresAt := c.storage.AccessTokenExpiry(now)
	return !now.Before(expiresAt.Add(-accessStaleLead))
}

// MarkAccessTokenStale latches a 401 so the next call signals refresh.
func (c *RelayClient) MarkAccessTokenStale() {
	c.mu.Lock()
	c.accessTokenStale = true
	c.mu.Unlock()
}

// Credential returns the Bearer credential for the next relay request: the
// device ticket when mintable, otherwise the access token itself (the relay
// accepts both; the official client falls back identically).
func (c *RelayClient) Credential(ctx context.Context) (string, error) {
	c.mu.Lock()
	now := c.now()
	if c.accessStaleLocked(now) {
		c.mu.Unlock()
		return "", NewStatusError(http.StatusUnauthorized, []byte(`{"error":"Mirasim access token requires refresh"}`), nil)
	}
	if c.ticket != "" && now.Before(c.ticketExpiresAt.Add(-ticketRefreshLead)) {
		ticket := c.ticket
		c.mu.Unlock()
		return ticket, nil
	}
	if now.Before(c.ticketUnmintable) {
		token := strings.TrimSpace(c.storage.AccessToken)
		c.mu.Unlock()
		return token, nil
	}
	if now.Before(c.ticketNextMint) {
		defer c.mu.Unlock()
		if c.ticket != "" && now.Before(c.ticketExpiresAt) {
			return c.ticket, c.ticketLastErr
		}
		return "", c.ticketLastErr
	}
	c.mu.Unlock()
	return c.mintTicket(ctx)
}

// mintTicket performs one POST /v1/device/session, signed with the access
// token as the credential inside the signature.
func (c *RelayClient) mintTicket(ctx context.Context) (string, error) {
	c.mu.Lock()
	accessToken := strings.TrimSpace(c.storage.AccessToken)
	body, err := json.Marshal(struct {
		PublicKey string `json:"publicKey"`
		DeviceID  string `json:"deviceId"`
	}{PublicKey: c.identity.PublicKeyBase64, DeviceID: c.identity.DeviceID})
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	headers, err := signatureHeaders(c.identity, c.storage.ClientVersion, http.MethodPost, sessionResource, accessToken, nil, body)
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("Content-Type", "application/json")
	endpoint, _, err := buildRelayEndpoint(c.storage.RelayURL, sessionResource, nil)
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	httpClient := c.relayHTTPClient()
	c.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header = headers
	if roundTrip, ok := ctx.Value(relayRoundTripperKey{}).(*http.Client); ok && roundTrip != nil {
		httpClient = roundTrip
	}
	resp, err := httpClient.Do(req)

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ticketNextMint = now.Add(ticketRetryFloor)
	if err != nil {
		err = fmt.Errorf("mint Mirasim device ticket: %w", err)
		c.ticketLastErr = err
		return c.staleTicketOrErrorLocked(now, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		err = fmt.Errorf("read Mirasim device ticket response: %w", readErr)
		c.ticketLastErr = err
		return c.staleTicketOrErrorLocked(now, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errStatus := NewStatusError(resp.StatusCode, raw, nil)
		if quiet := ticketUnmintableWindow(resp.StatusCode); quiet > 0 {
			c.ticketUnmintable = now.Add(quiet)
			c.ticketLastErr = nil
			return accessToken, nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			c.accessTokenStale = true
		}
		c.ticketLastErr = errStatus
		return c.staleTicketOrErrorLocked(now, errStatus)
	}
	var payload struct {
		Ticket    string   `json:"ticket"`
		ExpiresIn *float64 `json:"expiresIn"`
		ExpiresAt *float64 `json:"expiresAt"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		err = fmt.Errorf("decode Mirasim device ticket: %w", err)
		c.ticketLastErr = err
		return c.staleTicketOrErrorLocked(now, err)
	}
	payload.Ticket = strings.TrimSpace(payload.Ticket)
	if payload.Ticket == "" {
		err = fmt.Errorf("Mirasim device ticket response is missing ticket")
		c.ticketLastErr = err
		return c.staleTicketOrErrorLocked(now, err)
	}
	c.ticket = payload.Ticket
	c.ticketExpiresAt = resolveTicketExpiry(now, payload.ExpiresIn, payload.ExpiresAt)
	c.ticketLastErr = nil
	return c.ticket, nil
}

// staleTicketOrErrorLocked returns a still-valid cached ticket instead of the
// mint error when one exists, so a transient mint failure does not stall
// requests the relay would have served.
func (c *RelayClient) staleTicketOrErrorLocked(now time.Time, err error) (string, error) {
	if c.ticket != "" && now.Before(c.ticketExpiresAt) {
		return c.ticket, nil
	}
	return "", err
}

// SignHeaders renders the mrs-sig-v2 (+ sealed metadata) headers for one
// relay request. credential is the Bearer value the request will carry.
// metadata, when non-nil, is the request's x-mirasim-* context (session,
// agent, call, account); control-plane calls pass nil and skip sealing.
func (c *RelayClient) SignHeaders(method, requestPath, credential string, metadata map[string]string, body []byte) (http.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signHeadersLocked(method, requestPath, credential, metadata, body)
}

func (c *RelayClient) signHeadersLocked(method, requestPath, credential string, metadata map[string]string, body []byte) (http.Header, error) {
	headers, err := signatureHeaders(c.identity, c.storage.ClientVersion, method, requestPath, credential, metadata, body)
	if err != nil {
		return nil, err
	}
	headers.Set("Authorization", "Bearer "+credential)
	if metadata == nil {
		return headers, nil
	}
	if err := sealRelayHeaders(headers, method, requestPath); err != nil {
		return nil, err
	}
	return headers, nil
}

// SessionID returns (generating once) the long-lived x-mirasim-session value.
func (c *RelayClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == "" {
		id, err := randomRelayID(rand.Reader, "mirasim_")
		if err != nil {
			// crypto/rand failure is essentially unreachable; keep a stable but
			// time-derived session id rather than breaking the request.
			id = fmt.Sprintf("mirasim_fallback-%d", c.now().UnixNano())
		}
		c.sessionID = id
	}
	return c.sessionID
}

// AccountID returns the x-mirasim-account value from the current token.
func (c *RelayClient) AccountID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return AccessTokenAgentAccount(c.storage.AccessToken)
}

// FetchLimits performs the signed control-plane GET /v1/limits (quota lane).
func (c *RelayClient) FetchLimits(ctx context.Context) ([]byte, error) {
	return c.signedControlGet(ctx, limitsResource)
}

// FetchModels performs the signed control-plane GET /v1/models.
func (c *RelayClient) FetchModels(ctx context.Context) ([]byte, http.Header, error) {
	return c.signedControlGetWithHeaders(ctx, modelsResource)
}

func (c *RelayClient) signedControlGet(ctx context.Context, resource string) ([]byte, error) {
	raw, _, err := c.signedControlGetWithHeaders(ctx, resource)
	return raw, err
}

// signedControlGetWithHeaders issues one signed control-plane GET. Control
// calls carry empty metadata and are not sealed.
func (c *RelayClient) signedControlGetWithHeaders(ctx context.Context, resource string) ([]byte, http.Header, error) {
	credential, err := c.Credential(ctx)
	if err != nil {
		return nil, nil, err
	}
	endpoint, signaturePath, err := buildRelayEndpoint(c.storage.RelayURL, resource, nil)
	if err != nil {
		return nil, nil, err
	}
	headers, err := c.SignHeaders(http.MethodGet, signaturePath, credential, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	headers.Set("Accept", "application/json")

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header = headers
	httpClient := c.relayHTTPClient()
	if roundTrip, ok := ctx.Value(relayRoundTripperKey{}).(*http.Client); ok && roundTrip != nil {
		httpClient = roundTrip
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.MarkAccessTokenStale()
		return raw, resp.Header, NewStatusError(resp.StatusCode, raw, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, resp.Header, NewStatusError(resp.StatusCode, raw, nil)
	}
	return raw, resp.Header, nil
}

func ticketUnmintableWindow(statusCode int) time.Duration {
	switch statusCode {
	case http.StatusNotFound:
		return ticketRouteAbsentQuiet
	case http.StatusNotImplemented:
		return ticketUnimplementedQuiet
	default:
		return 0
	}
}

func resolveTicketExpiry(now time.Time, expiresIn, expiresAt *float64) time.Time {
	if expiresAt != nil && *expiresAt > 0 {
		when := time.UnixMilli(int64(*expiresAt))
		if when.After(now) {
			return when
		}
	}
	if expiresIn != nil && *expiresIn > 0 {
		return now.Add(time.Duration(*expiresIn) * time.Second)
	}
	return now.Add(ticketDefaultTTL)
}

// relayRoundTripperKey lets tests inject an http client through context.
type relayRoundTripperKey struct{}

// WithRelayHTTPClient injects the client used by control/mint calls (tests).
func WithRelayHTTPClient(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, relayRoundTripperKey{}, client)
}

// StatusError classifies an upstream HTTP failure for conductor status logic.
type StatusError struct {
	Code    int
	Headers http.Header
	Body    string
}

func (e *StatusError) Error() string {
	if e == nil {
		return "mirasim: request failed"
	}
	return fmt.Sprintf("status %d: %s", e.Code, e.Body)
}

// StatusCode returns the upstream status code.
func (e *StatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Code
}

// NewStatusError truncates the body into a bounded StatusError.
func NewStatusError(code int, body []byte, headers http.Header) *StatusError {
	const maxBody = 4 << 10
	trimmed := string(body)
	if len(trimmed) > maxBody {
		trimmed = trimmed[:maxBody]
	}
	return &StatusError{Code: code, Headers: headers, Body: trimmed}
}
