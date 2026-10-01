// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): the browser OAuth
// coordinator (start/authorize/callback/email pages backed by in-memory
// login sessions).
package mirasim

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// browserLoginTTL matches the host OAuth session store ceiling: a
	// Management Center browser login stays valid for exactly as long as the
	// panel will keep polling it.
	browserLoginTTL = 30 * time.Minute
	// maxBrowserSessions bounds pending browser logins kept in memory.
	maxBrowserSessions = 8
	// maxEmailCodeSends bounds how many codes one pending login may ask
	// Mirasim to mail; the send route is unauthenticated and a state is all a
	// caller needs, so three requests keep the route useless as a mail relay.
	maxEmailCodeSends = 3
	// emailCodeSendInterval is the minimum wait between two code requests on
	// one login.
	emailCodeSendInterval = 60 * time.Second
	// maxEmailVerifyAttempts bounds wrong codes on one login before the login
	// is failed. Mirasim codes are short, so a small budget is a poor oracle
	// yet still leaves room for typos and a slow mail delivery.
	maxEmailVerifyAttempts = 5

	// BrowserStartPath is the gin route the panel's start handler returns.
	BrowserStartPath = "/mirasim/oauth/start"
	// BrowserAuthorizePath redirects the browser to Mirasim for one provider.
	BrowserAuthorizePath = "/mirasim/oauth/authorize"
	// BrowserCallbackPath receives the Mirasim browser callback.
	BrowserCallbackPath = "/mirasim/oauth/callback"
	// BrowserEmailSendPath asks Mirasim to mail a sign-in code.
	BrowserEmailSendPath = "/mirasim/oauth/email/send"
	// BrowserEmailVerifyPath exchanges the mailed code for credentials.
	BrowserEmailVerifyPath = "/mirasim/oauth/email/verify"
)

// errNoLoginProviders reports discovery that enabled no way to sign in.
var errNoLoginProviders = errors.New("Mirasim is not offering any sign-in provider right now")

// BrowserStartResult is what the auth-url handler returns to the panel: a
// (usually relative) start URL plus the session state.
type BrowserStartResult struct {
	StartURL        string
	State           string
	ExpiresAt       time.Time
	DefaultProvider string
}

// browserSession is one pending Management Center login.
type browserSession struct {
	state           string
	providers       []LoginProvider
	defaultProvider string
	provider        string
	proxyURL        string
	// email is the address the first code was successfully mailed to, and the
	// only address verify checks a code against. It stays empty until a send
	// succeeds, so a failed send leaves a typo fixable.
	email string
	// emailSentAt and emailSends rate limit the send route on this login.
	emailSentAt time.Time
	emailSends  int
	// emailMailed records whether any send reached Mirasim and
	// emailSendsInFlight counts the sends past their commit point, so a
	// failure can never undo a pin another send legitimately established.
	emailMailed        bool
	emailSendsInFlight int
	// emailAttempts counts wrong codes tried against this login.
	emailAttempts int
	callbackURL   string
	adminURL      string
	expiresAt     time.Time
	accessToken   string
	refreshToken  string
	done          bool
	errorMessage  string
}

func (s *browserSession) emailExhausted() bool {
	return s.emailAttempts >= maxEmailVerifyAttempts
}

// BrowserCoordinator keeps the pending Management Center logins. The zero
// value is usable, but NewBrowserCoordinator installs a testable clock.
type BrowserCoordinator struct {
	mu       sync.Mutex
	sessions map[string]*browserSession
	now      func() time.Time
}

// NewBrowserCoordinator returns a ready coordinator.
func NewBrowserCoordinator() *BrowserCoordinator {
	return &BrowserCoordinator{sessions: make(map[string]*browserSession), now: time.Now}
}

// StartBrowserLogin registers one pending login and returns the URL the panel
// opens. callbackOrigin is the loopback origin of THIS server
// ("http://127.0.0.1:<port>"): Mirasim only redirects to loopback, so the
// callback always returns through this process's own port.
func (c *BrowserCoordinator) StartBrowserLogin(ctx context.Context, settings Settings, callbackOrigin, requestedProvider string) (*BrowserStartResult, error) {
	settings = settings.Normalize()
	if strings.TrimSpace(callbackOrigin) == "" {
		return nil, fmt.Errorf("Mirasim OAuth callback origin is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(callbackOrigin))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("Mirasim OAuth callback origin is not usable")
	}
	if !IsLoopbackHost(parsed.Hostname()) {
		return nil, fmt.Errorf("Mirasim OAuth callback address is not loopback, which Mirasim refuses")
	}

	offered, err := DiscoverLoginProviders(ctx, settings.AdminURL, settings.ProxyURL)
	if err != nil {
		return nil, err
	}
	if len(offered) == 0 {
		return nil, errNoLoginProviders
	}
	defaultProvider, err := selectLoginDefault(offered, requestedProvider, settings.LoginProvider)
	if err != nil {
		return nil, err
	}
	state, err := randomOAuthValue(32)
	if err != nil {
		return nil, fmt.Errorf("generate Mirasim OAuth state: %w", err)
	}
	callbackURL := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: BrowserCallbackPath, RawQuery: url.Values{"state": {state}}.Encode()}
	startURL := startPageURLFor(state)

	now := c.clock()()
	expiresAt := now.Add(browserLoginTTL)
	c.mu.Lock()
	if c.sessions == nil {
		c.sessions = make(map[string]*browserSession)
	}
	c.purgeLocked(now)
	c.makeRoomLocked()
	c.sessions[state] = &browserSession{
		state:           state,
		providers:       offered,
		defaultProvider: defaultProvider,
		proxyURL:        settings.ProxyURL,
		callbackURL:     callbackURL.String(),
		adminURL:        settings.AdminURL,
		expiresAt:       expiresAt,
	}
	c.mu.Unlock()

	return &BrowserStartResult{
		StartURL:        startURL,
		State:           state,
		ExpiresAt:       expiresAt,
		DefaultProvider: defaultProvider,
	}, nil
}

func startPageURLFor(state string) string {
	return BrowserStartPath + "?" + url.Values{"state": {state}}.Encode()
}

func (c *BrowserCoordinator) clock() func() time.Time {
	if c.now != nil {
		return c.now
	}
	return time.Now
}

func (c *BrowserCoordinator) purgeLocked(now time.Time) {
	for state, session := range c.sessions {
		if session == nil || !now.Before(session.expiresAt) {
			delete(c.sessions, state)
		}
	}
}

func (c *BrowserCoordinator) makeRoomLocked() {
	for len(c.sessions) >= maxBrowserSessions {
		oldest := ""
		for state, session := range c.sessions {
			if oldest == "" || session.expiresAt.Before(c.sessions[oldest].expiresAt) {
				oldest = state
			}
		}
		delete(c.sessions, oldest)
	}
}

type sessionLookupResult int

const (
	sessionOK sessionLookupResult = iota
	sessionMissing
	sessionUsed
)

func (c *BrowserCoordinator) lookupLocked(state string) (*browserSession, sessionLookupResult) {
	c.purgeLocked(c.clock()())
	session := c.sessions[state]
	if session == nil || !constantTimeEqual(session.state, state) {
		return nil, sessionMissing
	}
	if session.done {
		return session, sessionUsed
	}
	return session, sessionOK
}

// SessionExpiredError is reported when PollTerminalResult names no session.
var SessionExpiredError = errors.New("unknown or expired Mirasim OAuth state")

// PollTerminalResult reports whether the named login reached a terminal
// state: done (credential latched or failure message) or missing/expired.
func (c *BrowserCoordinator) PollTerminalResult(state string) (done bool, errorMessage string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeLocked(c.clock()())
	session := c.sessions[state]
	if session == nil {
		return false, "", nil
	}
	if session.done {
		return true, session.errorMessage, nil
	}
	return false, "", nil
}

// DrainCredential hands the latched callback credential to exactly one
// caller (the management handler's save path), clearing it from memory.
func (c *BrowserCoordinator) DrainCredential(state string) (accessToken, refreshToken string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeLocked(c.clock()())
	session := c.sessions[state]
	if session == nil {
		return "", "", SessionExpiredError
	}
	if !session.done || session.errorMessage != "" {
		return "", "", SessionExpiredError
	}
	accessToken, refreshToken = session.accessToken, session.refreshToken
	session.accessToken = ""
	session.refreshToken = ""
	delete(c.sessions, state)
	return accessToken, refreshToken, nil
}

// handleStartPage renders the page the panel's "open link" button opens.
func (c *BrowserCoordinator) StartPage(state string) (int, []byte) {
	state = strings.TrimSpace(state)
	c.mu.Lock()
	session, lookup := c.lookupLocked(state)
	if lookup == sessionMissing {
		c.mu.Unlock()
		return 400, []byte(startExpiredPage)
	}
	if lookup == sessionUsed {
		exhausted := session.emailExhausted()
		c.mu.Unlock()
		if exhausted {
			return 400, []byte(emailAttemptsPage)
		}
		return 409, []byte(callbackUsedPage)
	}
	data := startPageData{State: session.state, CallbackURL: session.callbackURL, Field: pastedCallbackField, EmailField: emailAddressField, Minutes: int(browserLoginTTL.Minutes())}
	for _, provider := range session.providers {
		data.Providers = append(data.Providers, browserProviderButton{ID: provider.ID, Label: provider.Label, Default: provider.ID == session.defaultProvider})
	}
	c.mu.Unlock()
	body, err := renderStartPage(data)
	if err != nil {
		return 500, []byte(callbackNotFoundPage)
	}
	return 200, body
}

// HandleAuthorize sends the browser to Mirasim for the provider chosen on the
// start page. The pending session, not the query string, is the authority on
// which providers are offered.
func (c *BrowserCoordinator) HandleAuthorize(query url.Values) (status int, location string, page []byte) {
	state := strings.TrimSpace(query.Get("state"))
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	if state == "" || provider == "" {
		return 400, "", []byte(authorizeProviderPage)
	}
	c.mu.Lock()
	session, lookup := c.lookupLocked(state)
	if lookup == sessionMissing {
		c.mu.Unlock()
		return 400, "", []byte(startExpiredPage)
	}
	if lookup == sessionUsed {
		exhausted := session.emailExhausted()
		c.mu.Unlock()
		if exhausted {
			return 400, "", []byte(emailAttemptsPage)
		}
		return 409, "", []byte(callbackUsedPage)
	}
	if !ProviderOffered(session.providers, provider) {
		c.mu.Unlock()
		return 400, "", []byte(authorizeProviderPage)
	}
	session.provider = provider
	callbackURL := session.callbackURL
	adminURL := session.adminURL
	c.mu.Unlock()

	authURL, err := BuildLoginURL(adminURL, provider, callbackURL, state)
	if err != nil {
		return 500, "", []byte(authorizeUnavailablePage)
	}
	return 302, authURL, nil
}

// HandleCallback latches the single callback of the pending login its state
// names (direct Mirasim redirect or a URL pasted into the start page).
func (c *BrowserCoordinator) HandleCallback(query url.Values) (int, []byte) {
	if pasted, ok := query[pastedCallbackField]; ok {
		result, okResult := pastedCallbackResult(strings.Join(pasted, ""))
		if !okResult {
			// A wrong paste is the operator's slip, not Mirasim's answer, so it
			// must not use up the login.
			return 400, []byte(callbackPastePage)
		}
		return c.acceptCallback(result)
	}
	return c.acceptCallback(callbackResultFromValues(query))
}

func (c *BrowserCoordinator) acceptCallback(result browserCallbackResult) (int, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeLocked(c.clock()())
	session := c.sessions[result.state]
	if result.state == "" || session == nil || !constantTimeEqual(session.state, result.state) {
		return 400, []byte(callbackStatePage)
	}
	if session.done {
		return 409, []byte(callbackUsedPage)
	}
	session.done = true
	switch {
	case result.errorMessage != "":
		// callbackResultFromValues only ever produces a fixed message, never a
		// captured value, so this is safe to surface verbatim.
		session.errorMessage = "Mirasim OAuth login failed: " + result.errorMessage
	default:
		if rejection := rejectCallbackCredentials(result.accessToken, result.refreshToken); rejection != "" {
			session.errorMessage = "Mirasim OAuth login failed: " + rejection
			break
		}
		session.accessToken = result.accessToken
		session.refreshToken = result.refreshToken
		return 200, []byte(callbackCompletePage)
	}
	return 400, []byte(callbackFailedPage)
}

// browserCallbackResult is one latched Mirasim callback (or a valid paste).
type browserCallbackResult struct {
	state        string
	accessToken  string
	refreshToken string
	errorMessage string
}

func callbackResultFromValues(values url.Values) browserCallbackResult {
	accessToken := strings.TrimSpace(values.Get("access_token"))
	if accessToken == "" {
		accessToken = strings.TrimSpace(values.Get("token"))
	}
	errorMessage := ""
	if strings.TrimSpace(values.Get("error")) != "" {
		errorMessage = "Mirasim cancelled or rejected the login"
	}
	return browserCallbackResult{
		state:        strings.TrimSpace(values.Get("state")),
		accessToken:  accessToken,
		refreshToken: strings.TrimSpace(values.Get("refresh_token")),
		errorMessage: errorMessage,
	}
}

// rejectCallbackCredentials refuses callback credentials that are missing,
// oversized, or header-splitting (messages never quote the value rejected).
func rejectCallbackCredentials(accessToken, refreshToken string) string {
	const maxLen = 64 << 10
	if accessToken == "" || refreshToken == "" {
		return "callback did not include renewable credentials"
	}
	if len(accessToken) > maxLen || len(refreshToken) > maxLen {
		return "callback credentials exceeded the accepted size"
	}
	if strings.ContainsAny(accessToken, "\r\n\x00") || strings.ContainsAny(refreshToken, "\r\n\x00") {
		return "callback credentials contained invalid characters"
	}
	return ""
}

// HandleEmailSend mails a sign-in code for the pending login named by the
// request's state (plugin parity: pin, rate limits, code page rendering).
func (c *BrowserCoordinator) HandleEmailSend(ctx context.Context, query url.Values) (int, []byte) {
	state := strings.TrimSpace(query.Get("state"))

	c.mu.Lock()
	session, lookup := c.lookupLocked(state)
	if lookup == sessionMissing {
		c.mu.Unlock()
		return 400, []byte(startExpiredPage)
	}
	if lookup == sessionUsed {
		exhausted := session.emailExhausted()
		c.mu.Unlock()
		if exhausted {
			return 400, []byte(emailAttemptsPage)
		}
		return 409, []byte(callbackUsedPage)
	}
	// A send whose form carries no address is the code page's resend: the
	// login is already pinned, so the state is all it needs to carry.
	address := strings.TrimSpace(query.Get(emailAddressField))
	if address == "" && session.email != "" {
		address = session.email
	} else {
		normalized, err := normalizeLoginEmail(address)
		if err != nil {
			c.mu.Unlock()
			return 400, []byte(emailAddressPage)
		}
		address = normalized
	}
	// The login belongs to the first address a code was actually mailed to.
	if session.email != "" && address != session.email {
		c.mu.Unlock()
		return 409, []byte(emailAddressPinnedPage)
	}
	now := c.clock()()
	if session.emailSends >= maxEmailCodeSends || (!session.emailSentAt.IsZero() && now.Sub(session.emailSentAt) < emailCodeSendInterval) {
		remaining := maxEmailCodeSends - session.emailSends
		limited := remaining > 0 && session.emailMailed
		c.mu.Unlock()
		return emailCodePageResponse(state, 429, false, limited, remaining)
	}
	// Pin and spend under one lock hold, before the outbound call: the pin
	// has to be visible to a competing send for the whole time this one is in
	// flight, and spending here turns two concurrent submits into one send.
	if session.email == "" {
		session.email = address
	}
	session.emailSends++
	session.emailSendsInFlight++
	session.emailSentAt = now
	adminURL, proxyURL := session.adminURL, session.proxyURL
	c.mu.Unlock()

	errSend := RequestEmailCode(ctx, adminURL, proxyURL, address)

	c.mu.Lock()
	defer c.mu.Unlock()
	session = c.sessions[state]
	if session == nil {
		return 400, []byte(startExpiredPage)
	}
	session.emailSendsInFlight--
	if errSend != nil {
		// Reopen the address only when nothing was ever mailed and no other
		// send is still in flight, so a failure cannot wipe a pin a sibling
		// send established and a typo stays fixable.
		if !session.emailMailed && session.emailSendsInFlight == 0 {
			session.email = ""
		}
		return 502, []byte(emailSendFailedPage)
	}
	session.emailMailed = true
	c.purgeLocked(now)
	remaining := maxEmailCodeSends - session.emailSends
	return emailCodePageResponse(state, 200, false, false, remaining)
}

// HandleEmailVerify exchanges the code entered on the code page for
// credentials, latching them exactly where the OAuth callback latches its
// own so the handler's single finalize path installs them unchanged.
func (c *BrowserCoordinator) HandleEmailVerify(ctx context.Context, query url.Values) (int, []byte) {
	state := strings.TrimSpace(query.Get("state"))

	c.mu.Lock()
	session, lookup := c.lookupLocked(state)
	if lookup == sessionMissing {
		c.mu.Unlock()
		return 400, []byte(startExpiredPage)
	}
	if lookup == sessionUsed {
		exhausted := session.emailExhausted()
		c.mu.Unlock()
		if exhausted {
			return 400, []byte(emailAttemptsPage)
		}
		return 409, []byte(callbackUsedPage)
	}
	// Verification without a send on this login has nothing to check.
	if session.emailSends == 0 || session.email == "" {
		c.mu.Unlock()
		return 400, []byte(emailNoCodePage)
	}
	code, err := normalizeLoginCode(query.Get(emailCodeField))
	if err != nil {
		remaining := maxEmailCodeSends - session.emailSends
		c.mu.Unlock()
		return emailCodePageResponse(state, 400, true, false, remaining)
	}
	address, adminURL, proxyURL := session.email, session.adminURL, session.proxyURL
	c.mu.Unlock()

	accessToken, refreshToken, errVerify := VerifyEmailCode(ctx, adminURL, proxyURL, address, code)

	c.mu.Lock()
	defer c.mu.Unlock()
	session = c.sessions[state]
	if session == nil {
		return 400, []byte(startExpiredPage)
	}
	if session.done {
		if session.emailExhausted() {
			return 400, []byte(emailAttemptsPage)
		}
		return 409, []byte(callbackUsedPage)
	}
	if errVerify != nil {
		session.emailAttempts++
		if session.emailAttempts >= maxEmailVerifyAttempts {
			// The budget is spent, so this is a failed login rather than a
			// typo: latch the failure and let the poller report it.
			session.done = true
			session.errorMessage = "Mirasim email sign-in failed: too many code attempts"
			return 400, []byte(emailAttemptsPage)
		}
		return emailCodePageResponse(state, 400, true, false, maxEmailCodeSends-session.emailSends)
	}
	session.done = true
	session.accessToken = accessToken
	session.refreshToken = refreshToken
	return 200, []byte(callbackCompletePage)
}

// selectLoginDefault picks the provider the start page marks. An explicitly
// requested provider has to be offered; a configured one is only a hint, so a
// value Mirasim does not offer leaves the chooser without a marked default
// instead of failing the login. With neither naming one, github is the fallback.
func selectLoginDefault(offered []LoginProvider, requested, configured string) (string, error) {
	if value := strings.ToLower(strings.TrimSpace(requested)); value != "" {
		if !providerSlug.MatchString(value) {
			return "", fmt.Errorf("invalid Mirasim sign-in provider")
		}
		if !ProviderOffered(offered, value) {
			return "", UnsupportedLoginProviderError(value, offered)
		}
		return value, nil
	}
	if value := strings.ToLower(strings.TrimSpace(configured)); value != "" {
		if providerSlug.MatchString(value) && ProviderOffered(offered, value) {
			return value, nil
		}
		return "", nil
	}
	if ProviderOffered(offered, DefaultLoginProvider) {
		return DefaultLoginProvider, nil
	}
	return "", nil
}

// UnsupportedLoginProviderError names what Mirasim is actually offering, so
// an operator does not have to guess.
func UnsupportedLoginProviderError(requested string, offered []LoginProvider) error {
	ids := make([]string, 0, len(offered))
	for _, provider := range offered {
		ids = append(ids, provider.ID)
	}
	if len(ids) == 0 {
		return errNoLoginProviders
	}
	sort.Strings(ids)
	return fmt.Errorf("Mirasim sign-in provider %q is not offered; available: %s", requested, strings.Join(ids, ", "))
}

func randomOAuthValue(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func constantTimeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// IsLoopbackHost reports whether host is loopback (localhost or 127.x).
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if strings.HasPrefix(host, "127.") {
		return true
	}
	if host == "::1" || host == "[::1]" {
		return true
	}
	return false
}
