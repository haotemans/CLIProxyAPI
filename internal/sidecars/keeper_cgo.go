//go:build cgo

package sidecars

import (
	"context"
	"fmt"
	"strings"

	keeperembed "cpa-usage-keeper/embed"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// runKeeper starts the embedded cpa-usage-keeper app. Keeper persists through
// mattn/go-sqlite3, so it only embeds into CGO builds; the !cgo variant
// reports a clear startup error instead (see keeper_nocgo.go).
func (m *Manager) runKeeper(ctx context.Context, cfg *config.Config, entry *Entry, baseURL string) error {
	kc := m.unified.Keeper

	dataDir := strings.TrimSpace(kc.DataDir)
	if dataDir == "" {
		dataDir = "cpa-usage-keeper/data"
	}
	managementKey := m.managementKey(cfg)
	if managementKey == "" {
		return fmt.Errorf("keeper requires unified.management-key (or a plaintext remote-management.secret-key)")
	}

	return keeperembed.Run(ctx, keeperembed.Options{
		Host:             "127.0.0.1",
		Port:             18080,
		BasePath:         KeeperBasePath,
		WorkDir:          dataDir,
		CPABaseURL:       baseURL,
		CPAManagementKey: managementKey,
		LoginPassword:    strings.TrimSpace(kc.LoginPassword),
	})
}
