//go:build !cgo

package sidecars

import (
	"context"
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var errKeeperRequiresCGO = errors.New("keeper requires a CGO build (mattn/go-sqlite3); build cli-proxy-api with CGO_ENABLED=1 to embed it")

// runKeeper is unavailable in !cgo builds: keeper's sqlite driver needs CGO.
// The entry reports this startup error and the rest of CPA keeps serving.
func (m *Manager) runKeeper(ctx context.Context, cfg *config.Config, entry *Entry, baseURL string) error {
	return errKeeperRequiresCGO
}
