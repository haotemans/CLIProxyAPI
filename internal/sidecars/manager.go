// Package sidecars embeds the vendored cpa-usage-keeper and cpa-manager-plus
// applications inside the CPA process behind loopback listeners, and
// reverse-proxies their traffic from the main HTTP server. The feature is
// feature-flagged by the config v8 `unified` section and degrades gracefully:
// a sidecar that fails to start is reported but keeps CPA serving.
package sidecars

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	// KeeperBasePath is the external mount prefix for the embedded keeper.
	KeeperBasePath = "/keeper"
	// ManagerBasePath is the external mount prefix for the embedded manager.
	ManagerBasePath = "/manager"
	// KeeperTarget is the keeper's loopback listener address.
	KeeperTarget = "127.0.0.1:18080"
	// ManagerTarget is the manager's loopback listener address.
	ManagerTarget = "127.0.0.1:18317"

	stopTimeout = 10 * time.Second
)

const (
	nameKeeper  = "keeper"
	nameManager = "manager"
)

// managerNote documents the CPAMP SPA limitation in unified mode.
const managerNote = "SPA unsupported under base path (absolute API routes); only the manager API is proxied. Run cpa-manager-plus standalone for the full panel UI."

// Entry reports the lifecycle state of one embedded sidecar.
type Entry struct {
	// Name is the sidecar identifier ("keeper" or "manager").
	Name string `json:"name"`
	// Enabled reports whether the sidecar is configured to start.
	Enabled bool `json:"enabled"`
	// Running reports whether the sidecar's runner is currently active.
	Running bool `json:"running"`
	// BasePath is the public mount prefix on the main HTTP server.
	BasePath string `json:"base_path"`
	// Target is the loopback address reverse-proxied traffic lands on.
	Target string `json:"target"`
	// LastError records the terminal error when the runner stopped early or
	// could not start (empty while healthy).
	LastError string `json:"last_error,omitempty"`
	// Note carries an informational caveat (e.g. SPA unsupported).
	Note string `json:"note,omitempty"`

	started bool
	running bool
	lastErr error
}

// Manager holds the configured sidecar entries and drives their lifecycle.
type Manager struct {
	unified config.UnifiedConfig
	entries []*Entry

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	start  atomic.Bool
	mu     sync.RWMutex
}

// NewManager captures the unified configuration. It may be nil-safe: a nil
// config produces a disabled manager.
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{}
	if cfg != nil {
		m.unified = cfg.Unified
	}
	return m
}

// Enabled reports whether the unified umbrella and at least one sidecar are on.
func (m *Manager) Enabled() bool {
	if m == nil || !m.unified.Enabled {
		return false
	}
	return m.unified.Keeper.Enabled || m.unified.Manager.Enabled
}

// CPAUpstreamBase resolves the loopback base URL sidecars use to reach CPA.
func (m *Manager) cpaBaseURL(cfg *config.Config) string {
	scheme := "http"
	port := 8317
	if cfg != nil {
		if cfg.TLS.Enable {
			scheme = "https"
		}
		if cfg.Port > 0 {
			port = cfg.Port
		}
	}
	return scheme + "://127.0.0.1:" + strconv.Itoa(port)
}

// Start launches every configured sidecar in its own goroutine. Failures are
// recorded on the entry and logged; they never abort CPA (graceful
// degradation). Start is a no-op when unified is disabled and idempotent.
func (m *Manager) Start(cfg *config.Config) {
	if m == nil || !m.Enabled() {
		return
	}
	if !m.start.CompareAndSwap(false, true) {
		return
	}
	m.mu.Lock()
	m.entries = m.buildEntries()
	m.runCtx, m.cancel = context.WithCancel(context.Background())
	ctx := m.runCtx
	m.mu.Unlock()

	baseURL := m.cpaBaseURL(cfg)
	for _, entry := range m.entries {
		if !entry.Enabled {
			continue
		}
		entry.started = true
		m.wg.Add(1)
		go m.runSidecar(ctx, entry, cfg, baseURL)
	}
	log.Infof("sidecars: unified mode enabled, started embedded sidecars on %s", baseURL)
}

// buildEntries derives the entry list from the unified configuration.
func (m *Manager) buildEntries() []*Entry {
	entries := make([]*Entry, 0, 2)
	keeper := &Entry{
		Name:     nameKeeper,
		Enabled:  m.unified.Keeper.Enabled,
		BasePath: KeeperBasePath,
		Target:   KeeperTarget,
	}
	if keeper.Enabled && strings.TrimSpace(m.unified.Keeper.LoginPassword) == "" {
		keeper.Enabled = false
		keeper.started = true
		keeper.lastErr = errKeeperLoginPasswordMissing
	}
	entries = append(entries, keeper)
	entries = append(entries, &Entry{
		Name:     nameManager,
		Enabled:  m.unified.Manager.Enabled,
		BasePath: ManagerBasePath,
		Target:   ManagerTarget,
		Note:     managerNote,
	})
	return entries
}

// Stop cancels the sidecar context and waits (bounded) for runners to exit.
func (m *Manager) Stop(ctx context.Context) {
	if m == nil || m.cancel == nil {
		return
	}
	m.cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	wait := stopTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			remain := time.Until(deadline)
			if remain > 0 && remain < wait {
				wait = remain
			}
		}
	}
	select {
	case <-done:
	case <-time.After(wait):
		log.Warn("sidecars: shutdown timed out waiting for embedded runners")
	}
}

// Status renders GET /v0/management/sidecars.
func (m *Manager) Status(c *gin.Context) {
	m.mu.RLock()
	entries := append([]*Entry(nil), m.entries...)
	m.mu.RUnlock()
	if entries == nil && !m.Enabled() {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "sidecars": []any{}})
		return
	}
	items := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		m.mu.RLock()
		item := Entry{
			Name:     entry.Name,
			Enabled:  entry.Enabled,
			Running:  entry.running,
			BasePath: entry.BasePath,
			Target:   entry.Target,
			Note:     entry.Note,
		}
		if entry.lastErr != nil {
			item.LastError = entry.lastErr.Error()
		}
		m.mu.RUnlock()
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"enabled": m.Enabled(), "sidecars": items})
}

// errKeeperLoginPasswordMissing guards the empty-credentials startup failure.
var errKeeperLoginPasswordMissing = errors.New("keeper requires unified.keeper.login-password; sidecar not started")

// runSidecar runs one sidecar until ctx cancellation or runner failure.
func (m *Manager) runSidecar(ctx context.Context, entry *Entry, cfg *config.Config, baseURL string) {
	defer m.wg.Done()
	m.mu.Lock()
	entry.running = true
	m.mu.Unlock()

	var err error
	switch entry.Name {
	case nameKeeper:
		err = m.runKeeper(ctx, cfg, entry, baseURL)
	case nameManager:
		err = m.runManager(ctx, cfg, entry, baseURL)
	default:
		err = fmt.Errorf("sidecars: unknown sidecar %q", entry.Name)
	}

	m.mu.Lock()
	entry.running = false
	if err != nil && ctx.Err() == nil {
		entry.lastErr = err
		log.Errorf("sidecars: %s stopped with error: %v", entry.Name, err)
	}
	m.mu.Unlock()
	log.Infof("sidecars: %s runner exited", entry.Name)
}

// proxyFor builds a reverse proxy handler for one entry.
func proxyFor(target, basePath string, stripPrefix bool) (http.Handler, error) {
	u, err := url.Parse("http://" + target)
	if err != nil {
		return nil, fmt.Errorf("sidecars: invalid proxy target %s: %w", target, err)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.SetXForwarded()
			if stripPrefix {
				trimmed := strings.TrimPrefix(r.Out.URL.Path, basePath)
				if trimmed == "" {
					trimmed = "/"
				}
				r.Out.URL.Path = trimmed
			}
			// Keeper keeps its prefix intact: keeper natively mounts itself
			// under APP_BASE_PATH, so the full external path is correct.
		},
		// SSE-friendly flushing for keeper's update streams.
		FlushInterval: 100 * time.Millisecond,
	}, nil
}

// RegisterRoutes mounts the reverse proxies for every configured sidecar on
// the gin engine. Keeper is mounted prefix-preserved (native APP_BASE_PATH
// support); Manager is mounted prefix-stripped (absolute routes only; the
// CPAMP SPA does not support base paths — see managerNote).
func (m *Manager) RegisterRoutes(engine *gin.Engine) {
	if m == nil || engine == nil || !m.Enabled() {
		return
	}
	if m.unified.Keeper.Enabled {
		if proxy, err := proxyFor(KeeperTarget, KeeperBasePath, false); err != nil {
			log.Errorf("sidecars: keeper proxy setup failed: %v", err)
		} else {
			engine.Any(KeeperBasePath, gin.WrapH(proxy))
			engine.Any(KeeperBasePath+"/*proxyPath", gin.WrapH(proxy))
		}
	}
	if m.unified.Manager.Enabled {
		if proxy, err := proxyFor(ManagerTarget, ManagerBasePath, true); err != nil {
			log.Errorf("sidecars: manager proxy setup failed: %v", err)
		} else {
			engine.Any(ManagerBasePath, gin.WrapH(proxy))
			engine.Any(ManagerBasePath+"/*proxyPath", gin.WrapH(proxy))
		}
	}
}
