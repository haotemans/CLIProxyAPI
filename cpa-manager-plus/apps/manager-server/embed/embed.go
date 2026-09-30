// Package embed lets host processes run cpa-manager-plus in-process: it maps
// structured options onto the manager's environment-based configuration and
// drives its lifecycle from a context.
package embed

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/appserver"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
)

// DefaultHTTPAddr is the loopback address used when Options.HTTPAddr is empty.
const DefaultHTTPAddr = "127.0.0.1:18317"

// Options carries the manager knobs normally resolved from the environment.
type Options struct {
	// HTTPAddr is the manager's bind address (loopback only; default 127.0.0.1:18317).
	HTTPAddr string
	// DataDir is the manager's data directory (sqlite db, archives, data key).
	DataDir string
	// DBPath overrides the sqlite database path (default <DataDir>/usage.sqlite).
	DBPath string
	// DataKeyPath overrides the data encryption key path (default <DataDir>/data.key).
	DataKeyPath string
	// AdminKey is CPA_MANAGER_ADMIN_KEY: the admin credential for the manager API.
	// When empty the manager bootstraps and generates one on first run.
	AdminKey string
	// ManagementKey authenticates the manager against the CPA management API
	// (CPA_MANAGEMENT_KEY).
	ManagementKey string
	// CPAUpstreamURL points the manager at the CPA upstream it proxies and
	// collects from (defaults to http://127.0.0.1:8317 style loopback supplied
	// by the host).
	CPAUpstreamURL string
	// CollectorMode selects the usage collector transport ("auto", "http",
	// "resp" or "subscribe"). Empty keeps the manager default ("auto").
	CollectorMode string
}

// Run starts the manager server in this process and blocks until ctx is
// cancelled or the server fails. Environment variables required by
// internal/config are set only for the duration of the config load and then
// restored, so the host process keeps its own environment.
func Run(ctx context.Context, o Options) error {
	addr := strings.TrimSpace(o.HTTPAddr)
	if addr == "" {
		addr = DefaultHTTPAddr
	}

	sets := map[string]string{
		"CPA_MANAGER_ADMIN_KEY":     strings.TrimSpace(o.AdminKey),
		"CPA_MANAGER_DATA_KEY_PATH": strings.TrimSpace(o.DataKeyPath),
		"CPA_MANAGEMENT_KEY":        strings.TrimSpace(o.ManagementKey),
		"CPA_UPSTREAM_URL":          strings.TrimSpace(o.CPAUpstreamURL),
		"USAGE_COLLECTOR_MODE":      strings.TrimSpace(o.CollectorMode),
		"USAGE_DATA_DIR":            strings.TrimSpace(o.DataDir),
		"USAGE_DB_PATH":             strings.TrimSpace(o.DBPath),
	}
	restore, err := setTempEnv(sets)
	if err != nil {
		return err
	}
	defer restore()

	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return fmt.Errorf("manager config: %w", err)
	}
	// The embedded manager always binds the loopback target chosen by the host.
	cfg.HTTPAddr = addr

	if ctx == nil {
		ctx = context.Background()
	}
	return appserver.RunServer(ctx, cfg)
}

// setTempEnv overlays the given variables, retaining each old value so it can
// be restored. Empty values are not injected, so host process values continue
// to apply.
func setTempEnv(sets map[string]string) (func(), error) {
	changed := make([]string, 0, len(sets))
	old := make(map[string]string, len(sets))
	for key, value := range sets {
		if value == "" {
			continue
		}
		old[key] = os.Getenv(key)
		if err := os.Setenv(key, value); err != nil {
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
