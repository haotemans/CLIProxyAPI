package usagestats

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultRetentionDays = 90
	defaultDBFileName    = "usage-stats.db"
)

var (
	globalRecorder atomic.Pointer[Recorder]
	globalPricer   atomic.Pointer[Pricer]
	globalMu       sync.Mutex
)

// EffectiveEnabled reports whether the recorder should run for the config.
// Omitted `usage-stats.enabled` means enabled.
func EffectiveEnabled(cfg *config.Config) bool {
	if cfg == nil || cfg.UsageStats.Enabled == nil {
		return true
	}
	return *cfg.UsageStats.Enabled
}

// EffectivePath resolves the sqlite database path from the config.
func EffectivePath(cfg *config.Config) string {
	if cfg == nil {
		return defaultDBFileName
	}
	if path := strings.TrimSpace(cfg.UsageStats.Path); path != "" {
		return path
	}
	authDir := strings.TrimSpace(cfg.AuthDir)
	if authDir == "" {
		authDir = "."
	}
	if expanded, err := util.ResolveAuthDir(cfg.AuthDir); err == nil {
		authDir = expanded
	}
	return filepath.Join(authDir, defaultDBFileName)
}

// EffectiveRetentionDays resolves the retention window (default 90 days).
func EffectiveRetentionDays(cfg *config.Config) int {
	if cfg == nil || cfg.UsageStats.RetentionDays <= 0 {
		return defaultRetentionDays
	}
	return cfg.UsageStats.RetentionDays
}

// pricingOverrides maps the raw config pricing map into Price entries.
func pricingOverrides(cfg *config.Config) map[string]Price {
	if cfg == nil || len(cfg.UsageStats.Pricing) == 0 {
		return nil
	}
	out := make(map[string]Price, len(cfg.UsageStats.Pricing))
	for pattern, price := range cfg.UsageStats.Pricing {
		out[pattern] = Price{Input: price.Input, Output: price.Output}
	}
	return out
}

// Global returns the active recorder (nil when disabled or not yet started).
func Global() *Recorder { return globalRecorder.Load() }

// GlobalPricer returns the active pricer (always non-nil after Start starts,
// so management cost previews still resolve defaults during tests).
func GlobalPricer() *Pricer {
	if p := globalPricer.Load(); p != nil {
		return p
	}
	globalMu.Lock()
	defer globalMu.Unlock()
	if p := globalPricer.Load(); p != nil {
		return p
	}
	p := NewPricer(nil)
	globalPricer.Store(p)
	return p
}

// Start boots the recorder per config and returns a stop function.
// When disabled it is a cheap no-op that still returns a usable stopper.
// Calling Start twice closes any previous instance first.
func Start(cfg *config.Config) (stop func(), err error) {
	overrides := pricingOverrides(cfg)
	if p := globalPricer.Load(); p != nil {
		p.UpdateOverrides(overrides)
	} else {
		globalMu.Lock()
		if globalPricer.Load() == nil {
			globalPricer.Store(NewPricer(overrides))
		} else {
			globalPricer.Load().UpdateOverrides(overrides)
		}
		globalMu.Unlock()
	}

	if !EffectiveEnabled(cfg) {
		globalRecorder.Store(nil)
		return func() {}, nil
	}

	globalMu.Lock()
	defer globalMu.Unlock()
	if prev := globalRecorder.Load(); prev != nil {
		_ = prev.Close()
	}
	recorder, err := Open(Config{
		Path:          EffectivePath(cfg),
		RetentionDays: EffectiveRetentionDays(cfg),
		Pricer:        GlobalPricer(),
	})
	if err != nil {
		return nil, err
	}
	globalRecorder.Store(recorder)
	coreusage.RegisterNamedPlugin("usagestats", recorder)
	log.Infof("usagestats: native usage accounting enabled (db=%s, retention=%dd)", EffectivePath(cfg), EffectiveRetentionDays(cfg))
	return func() {
		if globalRecorder.CompareAndSwap(recorder, nil) {
			if errClose := recorder.Close(); errClose != nil {
				log.Errorf("usagestats: close error: %v", errClose)
			}
		}
	}, nil
}

// ApplyReload hook used by config hot-reload: replaces pricing overrides
// only; path/enabled/retention changes require a restart (documented).
func ApplyReload(cfg *config.Config) {
	if p := globalPricer.Load(); p != nil {
		p.UpdateOverrides(pricingOverrides(cfg))
	}
}

// SetGlobalForTest swaps in a recorder for handler-level tests.
func SetGlobalForTest(r *Recorder) func() {
	prev := globalRecorder.Load()
	globalRecorder.Store(r)
	return func() { globalRecorder.Store(prev) }
}
