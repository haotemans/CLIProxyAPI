package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	mirasimauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/mirasim"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// mirasimOAuthStart is the GET /mirasim-auth-url handler: it registers one
// pending Mirasim browser OAuth login and returns the start page URL. The
// response shape matches the plugin era: {status, url, state, flow}.
//
// The browser flow returns through this server's own port (Mirasim only
// redirects to loopback); where that port is not reachable from the
// operator's browser, the start page's paste box takes the refused callback
// URL and completes the login instead.
func (h *Handler) RequestMirasimToken(c *gin.Context) {
	fmt.Println("Initializing Mirasim authentication...")

	requestedProvider := strings.TrimSpace(c.Query("provider"))
	isWebUI := isWebUIRequest(c)

	origin, errOrigin := h.mirasimCallbackOrigin()
	if errOrigin != nil {
		log.WithError(errOrigin).Error("failed to compute mirasim callback origin")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}

	settings := mirasimauth.SettingsFromConfig("")
	if h.cfg != nil {
		settings.ProxyURL = strings.TrimSpace(h.cfg.ProxyURL)
	}
	settings = mirasimOAuthEffectiveSettings(settings)
	start, errStart := h.mirasimOauthCoordinator().StartBrowserLogin(c.Request.Context(), settings, origin, requestedProvider)
	if errStart != nil {
		log.WithError(errStart).Error("failed to start mirasim oauth login")
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to start Mirasim login: %v", errStart)})
		return
	}
	state := start.State
	RegisterOAuthSession(state, "mirasim")
	go h.finalizeMirasimOAuthSession(state, settings)

	// The panel resolves the (relative) start URL against its own reachable
	// address; the TUI asks for an absolute loopback URL it can open directly.
	startURL := start.StartURL
	if isWebUI {
		startURL = origin + start.StartURL
	}
	response := gin.H{
		"status": "ok",
		"url":    startURL,
		"state":  state,
		"flow":   "browser_oauth",
	}
	if start.DefaultProvider != "" {
		response["login_provider"] = start.DefaultProvider
	}
	c.JSON(http.StatusOK, response)
}

// mirasimCallbackOrigin returns this server's loopback origin used both as
// the Mirasim redirect_uri (it refuses non-loopback) and, for the TUI, as the
// absolute start-page origin.
func (h *Handler) mirasimCallbackOrigin() (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", scheme, h.cfg.Port), nil
}

// mirasimOauthCoordinator returns the handler-wide browser login coordinator.
func (h *Handler) mirasimOauthCoordinator() *mirasimauth.BrowserCoordinator {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mirasimOAuth == nil {
		h.mirasimOAuth = mirasimauth.NewBrowserCoordinator()
	}
	return h.mirasimOAuth
}

// mirasimOAuthEndpointOverride swaps the public mirasim endpoints in tests
// (no config knob in production; the deployment defaults stay constant).
var mirasimOAuthEndpointOverride struct {
	mu       sync.Mutex
	adminURL string
	relayURL string
}

// SetMirasimOAuthEndpointsForTest points the OAuth flow at fake servers and
// returns a restore function.
func SetMirasimOAuthEndpointsForTest(adminURL, relayURL string) func() {
	mirasimOAuthEndpointOverride.mu.Lock()
	mirasimOAuthEndpointOverride.adminURL = strings.TrimSpace(adminURL)
	mirasimOAuthEndpointOverride.relayURL = strings.TrimSpace(relayURL)
	mirasimOAuthEndpointOverride.mu.Unlock()
	return func() {
		mirasimOAuthEndpointOverride.mu.Lock()
		mirasimOAuthEndpointOverride.adminURL = ""
		mirasimOAuthEndpointOverride.relayURL = ""
		mirasimOAuthEndpointOverride.mu.Unlock()
	}
}

// mirasimOAuthEffectiveSettings applies the test endpoint override, when set.
func mirasimOAuthEffectiveSettings(settings mirasimauth.Settings) mirasimauth.Settings {
	mirasimOAuthEndpointOverride.mu.Lock()
	adminURL := mirasimOAuthEndpointOverride.adminURL
	relayURL := mirasimOAuthEndpointOverride.relayURL
	mirasimOAuthEndpointOverride.mu.Unlock()
	if adminURL != "" {
		settings.AdminURL = adminURL
	}
	if relayURL != "" {
		settings.RelayURL = relayURL
	}
	return settings
}

// finalizeMirasimOAuthSession waits for one pending login to reach a terminal
// state (callback or email verify), then installs the credential (device
// identity + remote validation) and persists it like the other OAuth flows.
func (h *Handler) finalizeMirasimOAuthSession(state string, settings mirasimauth.Settings) {
	ctx := context.Background()
	coordinator := h.mirasimOauthCoordinator()
	for {
		if !IsOAuthSessionPending(state, "mirasim") {
			return
		}
		done, message, _ := coordinator.PollTerminalResult(state)
		if done {
			if message != "" {
				SetOAuthSessionError(state, message)
				return
			}
			accessToken, refreshToken, errDrain := coordinator.DrainCredential(state)
			if errDrain != nil {
				SetOAuthSessionError(state, "Mirasim OAuth session expired before credentials were installed")
				return
			}
			storage, errFinalize := mirasimauth.FinalizeOAuthStorage(ctx, settings, accessToken, refreshToken)
			if errFinalize != nil {
				log.WithError(errFinalize).Error("mirasim oauth finalize failed")
				SetOAuthSessionError(state, "Mirasim did not return a usable sign-in")
				return
			}
			fileName := storage.DefaultAuthFileName()
			record := &coreauth.Auth{
				ID:         fileName,
				Provider:   mirasimauth.Provider,
				FileName:   fileName,
				Label:      storage.AuthLabel(),
				Metadata:   storage.Metadata(),
				Attributes: map[string]string{"auth_kind": "oauth"},
			}
			if errGuard := guardOAuthSessionPendingForSave(state, "mirasim"); errGuard != nil {
				return
			}
			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				log.WithError(errSave).Error("failed to save mirasim oauth credential")
				SetOAuthSessionError(state, "Failed to save authentication tokens")
				return
			}
			fmt.Printf("Mirasim authentication successful! Token saved to %s\n", savedPath)
			CompleteOAuthSession(state)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// GetMirasimOAuthStart serves GET /mirasim/oauth/start (provider chooser page).
func (h *Handler) GetMirasimOAuthStart(c *gin.Context) {
	status, body := h.mirasimOauthCoordinator().StartPage(c.Query("state"))
	serveMirasimPage(c, status, body, false)
}

// GetMirasimOAuthAuthorize serves GET /mirasim/oauth/authorize (302 to Mirasim).
func (h *Handler) GetMirasimOAuthAuthorize(c *gin.Context) {
	status, location, page := h.mirasimOauthCoordinator().HandleAuthorize(c.Request.URL.Query())
	if location != "" {
		for key, values := range mirasimauth.BrowserHeaders() {
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		c.Redirect(status, location)
		return
	}
	serveMirasimPage(c, status, page, false)
}

// GetMirasimOAuthCallback serves GET /mirasim/oauth/callback (direct redirect
// or pasted callback URL form submit).
func (h *Handler) GetMirasimOAuthCallback(c *gin.Context) {
	status, body := h.mirasimOauthCoordinator().HandleCallback(c.Request.URL.Query())
	serveMirasimPage(c, status, body, false)
}

// GetMirasimOAuthEmailSend serves GET /mirasim/oauth/email/send.
func (h *Handler) GetMirasimOAuthEmailSend(c *gin.Context) {
	status, body := h.mirasimOauthCoordinator().HandleEmailSend(c.Request.Context(), c.Request.URL.Query())
	serveMirasimPage(c, status, body, true)
}

// GetMirasimOAuthEmailVerify serves GET /mirasim/oauth/email/verify.
func (h *Handler) GetMirasimOAuthEmailVerify(c *gin.Context) {
	status, body := h.mirasimOauthCoordinator().HandleEmailVerify(c.Request.Context(), c.Request.URL.Query())
	serveMirasimPage(c, status, body, true)
}

// serveMirasimPage writes one of the fixed browser pages with its security
// headers (form pages get the form-action-allowed variant).
func serveMirasimPage(c *gin.Context, status int, body []byte, formPage bool) {
	headers := mirasimauth.BrowserHeaders()
	if formPage {
		headers = mirasimauth.FormPageHeaders()
	}
	for key, values := range headers {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Data(status, "text/html; charset=utf-8", body)
}
