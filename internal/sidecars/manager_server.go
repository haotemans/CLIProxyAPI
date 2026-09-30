package sidecars

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	managerembed "github.com/seakee/cpa-manager-plus/apps/manager-server/embed"
)

// runManager starts the embedded cpa-manager-plus backend. The manager itself
// is pure Go (modernc sqlite), so it embeds in every build variant.
func (m *Manager) runManager(ctx context.Context, cfg *config.Config, entry *Entry, baseURL string) error {
	mc := m.unified.Manager

	dataDir := strings.TrimSpace(mc.DataDir)
	if dataDir == "" {
		dataDir = "cpa-manager-plus/data"
	}
	managementKey := m.managementKey(cfg)
	if managementKey == "" {
		return fmt.Errorf("manager requires unified.management-key (or a plaintext remote-management.secret-key)")
	}

	return managerembed.Run(ctx, managerembed.Options{
		HTTPAddr:       ManagerTarget,
		DataDir:        dataDir,
		DBPath:         "",
		DataKeyPath:    "",
		AdminKey:       strings.TrimSpace(mc.AdminKey),
		ManagementKey:  managementKey,
		CPAUpstreamURL: baseURL,
		CollectorMode:  strings.TrimSpace(mc.CollectorMode),
	})
}

// managementKey resolves the plaintext management key sidecars authenticate
// with: unified.management-key first, then a plaintext
// remote-management.secret-key (bcrypt-hashed keys cannot be shared).
func (m *Manager) managementKey(cfg *config.Config) string {
	if key := strings.TrimSpace(m.unified.ManagementKey); key != "" {
		return key
	}
	if cfg == nil {
		return ""
	}
	key := strings.TrimSpace(cfg.RemoteManagement.SecretKey)
	if key == "" || strings.HasPrefix(key, "$2") {
		return ""
	}
	return key
}
