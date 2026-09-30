// Package embed lets host processes run cpa-usage-keeper in-process: it maps
// structured options onto keeper's environment-based configuration, builds
// the app, and drives its lifecycle from a context.
package embed

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"cpa-usage-keeper/internal/app"
)

// Options carries every knob keeper resolves from its environment.
type Options struct {
	// EnvFile optionally points at a .env file to load first.
	EnvFile string
	// Host is the bind host for the embedded HTTP server (usually 127.0.0.1).
	Host string
	// Port is the bind port for the embedded HTTP server (default 18080).
	Port int
	// BasePath mounts every keeper route beneath this path (e.g. "/keeper").
	BasePath string
	// WorkDir is keeper's data directory: sqlite, logs and backups live below it.
	WorkDir string
	// CPABaseURL is the CLIProxyAPI base URL keeper collects usage from.
	CPABaseURL string
	// CPAManagementKey authenticates keeper against the CPA management API.
	CPAManagementKey string
	// CPAPublicURL is the browser-facing CPA URL used for frame-ancestors/links.
	CPAPublicURL string
	// LoginPassword protects keeper's own UI and admin API. Required unless
	// DisableAuth is set.
	LoginPassword string
	// AuthSessionTTL overrides keeper's default session lifetime when > 0.
	AuthSessionTTL time.Duration
	// DisableAuth turns keeper's login protection off explicitly.
	DisableAuth bool
}

// Run starts the keeper in this process and blocks until ctx is cancelled or
// the server fails. Environment variables required by internal/config are set
// only for the duration of the config load and restored afterwards so the host
// process keeps its own environment.
func Run(ctx context.Context, o Options) error {
	port := o.Port
	if port <= 0 {
		port = 18080
	}
	sets := map[string]string{
		"APP_BASE_PATH":       strings.TrimSpace(o.BasePath),
		"APP_PORT":            fmt.Sprintf("%d", port),
		"CPA_BASE_URL":        strings.TrimSpace(o.CPABaseURL),
		"CPA_MANAGEMENT_KEY":  strings.TrimSpace(o.CPAManagementKey),
		"CPA_PUBLIC_URL":      strings.TrimSpace(o.CPAPublicURL),
		"LOGIN_PASSWORD":      strings.TrimSpace(o.LoginPassword),
		"TRUSTED_PROXY_CIDRS": "127.0.0.1/32",
		"WORK_DIR":            strings.TrimSpace(o.WorkDir),
	}
	if o.DisableAuth {
		sets["AUTH_ENABLED"] = "false"
	}
	if o.AuthSessionTTL > 0 {
		sets["AUTH_SESSION_TTL"] = o.AuthSessionTTL.String()
	}

	restore, err := setTempEnv(sets)
	if err != nil {
		return err
	}
	defer restore()

	application, err := app.NewWithOptions(app.Options{EnvFile: o.EnvFile, AppHost: strings.TrimSpace(o.Host)})
	if err != nil {
		return fmt.Errorf("keeper init: %w", err)
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			// Close is idempotent; surface as an error only on the return path.
			_ = closeErr
		}
	}()

	if ctx == nil {
		ctx = context.Background()
	}
	return application.RunWithContext(ctx)
}

// setTempEnv overlays the given variables, retaining each old value so it can
// be restored. Leftover empty strings are treated as "leave untouched" unless
// the key is in the allowClear set, matching the sidecars embed convention:
// empty means do not inject, so host process values continue to apply.
func setTempEnv(sets map[string]string) (func(), error) {
	changed := make([]string, 0, len(sets))
	old := make(map[string]string, len(sets))
	for key, value := range sets {
		if value == "" {
			continue
		}
		old[key] = os.Getenv(key)
		if err := os.Setenv(key, value); err != nil {
			// Restore best-effort what was already changed.
			for _, k := range changed {
				if previous, ok := old[k]; ok {
					if previous == "" {
						_ = os.Unsetenv(k)
					} else {
						_ = os.Setenv(k, previous)
					}
				}
			}
			return nil, fmt.Errorf("set env %s: %w", key, err)
		}
		changed = append(changed, key)
	}
	return func() {
		for _, k := range changed {
			if previous, ok := old[k]; ok {
				if previous == "" {
					_ = os.Unsetenv(k)
				} else {
					_ = os.Setenv(k, previous)
				}
			}
		}
	}, nil
}
