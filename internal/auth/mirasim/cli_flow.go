// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): the interactive
// --mirasim-login flow: loopback callback listener, browser open, manual
// paste fallback, and email code login.
package mirasim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// cliLoginTTL bounds the blocking --mirasim-login wait.
	cliLoginTTL = 3 * time.Minute
	// cliManualPromptDelay is when the manual paste prompt appears.
	cliManualPromptDelay = 15 * time.Second

	manualPastePrompt = "Paste the Mirasim callback URL (or press Enter to keep waiting): "

	loopbackShutdownGrace = time.Second
)

// These are fixed errors on purpose: the pasted value is operator-supplied
// and may carry a credential, so nothing derived from it may reach an error
// string, a log line or stderr — url.Parse quotes the offending input.
var (
	errInvalidCallbackURL   = errors.New("invalid Mirasim callback URL")
	errForeignCallbackURL   = errors.New("pasted URL is not this login's Mirasim callback address")
	errMissingCallbackToken = errors.New("Mirasim callback URL is missing access_token")
)

// CLILoginResult bundles the successful login outcome.
type CLILoginResult struct {
	Storage Storage
	Stdout  string
}

// RunCLILogin performs the interactive browser login: discovery, loopback
// callback listener, browser open (unless NoBrowser), manual paste fallback,
// and credential finalize. prompt, when non-nil, reads one operator line for
// the paste fallback; when nil the flow relies on the listener alone.
func RunCLILogin(ctx context.Context, settings Settings, noBrowser bool, prompt func(string) (string, error)) (*CLILoginResult, error) {
	loginProvider, err := ResolveLoginProvider(settings.LoginProvider)
	if err != nil {
		return nil, err
	}
	settings = settings.Normalize()
	offered, err := DiscoverLoginProviders(ctx, settings.AdminURL, settings.ProxyURL)
	if err != nil {
		return nil, err
	}
	if !ProviderOffered(offered, loginProvider) {
		return nil, UnsupportedLoginProviderError(loginProvider, offered)
	}
	state, err := randomOAuthValue(32)
	if err != nil {
		return nil, err
	}
	capture, err := startLoopbackCapture(settings.CallbackPort, state)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	authURL, err := BuildLoginURL(settings.AdminURL, loginProvider, capture.callbackURL(), state)
	if err != nil {
		return nil, err
	}

	var stdout strings.Builder
	notice := "Open this URL to authenticate Mirasim:\n\n" + authURL + "\n\n"
	_, _ = os.Stdout.Write([]byte(notice))
	stdout.WriteString(notice)
	if !noBrowser {
		_ = openBrowser(authURL)
	}

	timer := time.NewTimer(cliLoginTTL)
	defer timer.Stop()
	manualTimer := time.NewTimer(cliManualPromptDelay)
	defer manualTimer.Stop()
	var pasted <-chan string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("Mirasim OAuth login timed out")
		case result := <-capture.results():
			storage, errFinish := finishCLILogin(ctx, settings, state, result)
			if errFinish != nil {
				return nil, errFinish
			}
			return &CLILoginResult{Storage: storage, Stdout: stdout.String() + "Mirasim authentication successful.\n"}, nil
		case <-manualTimer.C:
			if prompt != nil {
				pasted = promptLines(prompt, manualPastePrompt)
			}
			manualTimer.Stop()
		case line := <-pasted:
			pasted = nil
			if strings.TrimSpace(line) == "" {
				manualTimer.Reset(60 * time.Second)
				continue
			}
			result, okResult, errParse := parseManualOAuthResult(line, capture.callbackURL())
			if errParse != nil {
				return nil, errParse
			}
			if okResult {
				// Mirasim 0.0.272 omits state from its token callback. The
				// paste has already been checked against this login's own
				// callback address, so bind a state-less result to this
				// invocation before the constant-time check.
				if strings.TrimSpace(result.state) == "" {
					result.state = state
				}
				storage, errFinish := finishCLILogin(ctx, settings, state, result)
				if errFinish != nil {
					return nil, errFinish
				}
				return &CLILoginResult{Storage: storage, Stdout: stdout.String() + "Mirasim authentication successful.\n"}, nil
			}
		}
	}
}

// promptLines adapts the LoginOptions prompt to a one-line channel.
func promptLines(prompt func(string) (string, error), message string) <-chan string {
	out := make(chan string, 1)
	go func() {
		line, err := prompt(message)
		if err != nil {
			out <- ""
			return
		}
		out <- line
	}()
	return out
}

func finishCLILogin(ctx context.Context, settings Settings, state string, result browserCallbackResult) (Storage, error) {
	if result.errorMessage != "" {
		return Storage{}, fmt.Errorf("Mirasim OAuth login failed: %s", result.errorMessage)
	}
	if !constantTimeEqual(state, strings.TrimSpace(result.state)) {
		return Storage{}, fmt.Errorf("Mirasim OAuth state mismatch")
	}
	return FinalizeOAuthStorage(ctx, settings, result.accessToken, result.refreshToken)
}

// RunEmailLogin signs in with a mailed code. Mirasim issues these for
// accounts with no OAuth provider bound to them, which the browser and
// provider flows cannot reach at all.
func RunEmailLogin(ctx context.Context, settings Settings, email, code string, prompt func(string) (string, error)) (*CLILoginResult, error) {
	address, err := normalizeLoginEmail(email)
	if err != nil {
		return nil, err
	}
	settings = settings.Normalize()
	var stdout strings.Builder
	if strings.TrimSpace(code) == "" {
		if err := RequestEmailCode(ctx, settings.AdminURL, settings.ProxyURL, address); err != nil {
			return nil, err
		}
		notice := "Mirasim sent a sign-in code to " + address + ".\n"
		_, _ = os.Stdout.Write([]byte(notice))
		stdout.WriteString(notice)
		if prompt == nil {
			return nil, fmt.Errorf("Mirasim email sign-in requires an interactive prompt for the code")
		}
		entered, err := prompt("Enter the Mirasim sign-in code: ")
		if err != nil {
			return nil, err
		}
		code = entered
	}
	code, err = normalizeLoginCode(code)
	if err != nil {
		return nil, err
	}
	accessToken, refreshToken, err := VerifyEmailCode(ctx, settings.AdminURL, settings.ProxyURL, address, code)
	if err != nil {
		return nil, err
	}
	storage, err := FinalizeOAuthStorage(ctx, settings, accessToken, refreshToken)
	if err != nil {
		return nil, err
	}
	return &CLILoginResult{Storage: storage, Stdout: stdout.String() + "Mirasim authentication successful.\n"}, nil
}

// parseManualOAuthResult reads a callback URL the operator pasted at the
// prompt, and accepts it only if it names this login's own callback address.
// That check is the paste's channel binding (see the plugin's rationale: a
// state-less paste carries nothing else tying it to the login in progress).
func parseManualOAuthResult(input, callbackURL string) (browserCallbackResult, bool, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return browserCallbackResult{}, false, nil
	}
	if !strings.Contains(value, "://") {
		// An address bar that hides the scheme still yields host, path and query.
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return browserCallbackResult{}, false, errInvalidCallbackURL
	}
	expected, err := url.Parse(callbackURL)
	if err != nil {
		return browserCallbackResult{}, false, errInvalidCallbackURL
	}
	// Userinfo is not part of the authority, so a URL carrying it is refused
	// outright rather than compared.
	if parsed.User != nil || !strings.EqualFold(parsed.Host, expected.Host) || parsed.Path != expected.Path {
		return browserCallbackResult{}, false, errForeignCallbackURL
	}
	values := parsed.Query()
	if parsed.Fragment != "" {
		if fragment, errFragment := url.ParseQuery(parsed.Fragment); errFragment == nil {
			for key, entries := range fragment {
				if values.Get(key) == "" && len(entries) > 0 {
					values.Set(key, entries[0])
				}
			}
		}
	}
	result := callbackResultFromValues(values)
	if result.accessToken == "" && result.errorMessage == "" {
		return browserCallbackResult{}, false, errMissingCallbackToken
	}
	return result, true, nil
}

// loopbackCapture owns a single-use HTTP listener bound to 127.0.0.1 that
// receives exactly one Mirasim OAuth callback for --mirasim-login.
type loopbackCapture struct {
	callbackAddr string
	resultCh     chan browserCallbackResult
	server       *http.Server
	done         chan struct{}
	closeOnce    sync.Once
}

// startLoopbackCapture binds the listener before any authorize URL is handed
// out. A port of 0 takes an ephemeral port.
func startLoopbackCapture(port int, expectedState string) (*loopbackCapture, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid Mirasim OAuth callback port")
	}
	pathToken, err := randomOAuthValue(18)
	if err != nil {
		return nil, fmt.Errorf("generate Mirasim OAuth callback path: %w", err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("start Mirasim OAuth callback listener: %w", err)
	}
	callbackPath := "/callback/" + pathToken
	capture := &loopbackCapture{
		callbackAddr: "http://" + listener.Addr().String() + callbackPath,
		resultCh:     make(chan browserCallbackResult, 1),
		done:         make(chan struct{}),
	}
	capture.server = &http.Server{
		Handler:           localOAuthHandler(callbackPath, expectedState, capture.resultCh),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if errServe := capture.server.Serve(listener); errServe != nil && errServe != http.ErrServerClosed {
			select {
			case capture.resultCh <- browserCallbackResult{errorMessage: "Mirasim OAuth callback listener stopped"}:
			default:
			}
		}
	}()
	return capture, nil
}

func (c *loopbackCapture) callbackURL() string { return c.callbackAddr }

func (c *loopbackCapture) results() <-chan browserCallbackResult { return c.resultCh }

func (c *loopbackCapture) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		ctx, cancel := context.WithTimeout(context.Background(), loopbackShutdownGrace)
		defer cancel()
		if err := c.server.Shutdown(ctx); err != nil {
			_ = c.server.Close()
		}
	})
}

// localOAuthHandler serves exactly one callback on callbackPath. That path
// holds 144 bits of randomness on a loopback-only listener, which is what
// makes it a usable channel binding when Mirasim omits the state parameter.
func localOAuthHandler(callbackPath, expectedState string, results chan<- browserCallbackResult) http.Handler {
	var used atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		for key, values := range BrowserHeaders() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if !strings.EqualFold(r.Method, http.MethodGet) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		result := callbackResultFromValues(r.URL.Query())
		if strings.TrimSpace(result.state) == "" {
			result.state = strings.TrimSpace(expectedState)
		}
		if !constantTimeEqual(expectedState, result.state) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(callbackStatePage))
			return
		}
		if result.errorMessage == "" {
			if rejection := rejectCallbackCredentials(result.accessToken, result.refreshToken); rejection != "" {
				result.errorMessage = rejection
			}
		}
		if result.errorMessage != "" {
			result.accessToken, result.refreshToken = "", ""
		}
		if !used.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(callbackUsedPage))
			return
		}
		select {
		case results <- result:
		default:
		}
		if result.errorMessage != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(callbackFailedPage))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(callbackCompletePage))
	})
	return mux
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}
