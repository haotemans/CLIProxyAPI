// Package distribution tracks cumulative spend per issued client key and
// persists it in a small JSON state file next to the auth dir, so quotas
// survive restarts without a database.
package distribution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// UsageFileName is the state file name inside the auth dir.
const UsageFileName = "distribution-usage.json"

// UsageState is the on-disk shape of UsageFileName.
type UsageState struct {
	Version   int                  `json:"version"`
	UpdatedAt string               `json:"updated_at"`
	Keys      map[string]*KeyUsage `json:"keys"`
}

// KeyUsage is the cumulative spend for one key hash (usagestats.MaskKeyLabel
// of the key value — raw keys never reach the state file).
type KeyUsage struct {
	SpentUSD      float64 `json:"spent_usd"`
	Requests      int64   `json:"requests"`
	InputTokens   int64   `json:"input_tokens,omitempty"`
	OutputTokens  int64   `json:"output_tokens,omitempty"`
	LastRequestAt string  `json:"last_request_at,omitempty"`
}

// UsageStore accumulates per-key spend and persists it atomically (tmp file +
// rename) on every recorded event. nowFunc is injectable for tests.
type UsageStore struct {
	path    string
	nowFunc func() time.Time
	// mu serializes mutation and persistence; state lives fully in memory
	// after the initial load.
	mu    chan struct{}
	state *UsageState
}

// NewUsageStore loads (or initializes) the store at path. nowFunc may be nil.
func NewUsageStore(path string, nowFunc func() time.Time) (*UsageStore, error) {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	store := &UsageStore{path: path, nowFunc: nowFunc, mu: make(chan struct{}, 1)}
	store.mu <- struct{}{}
	state := &UsageState{Version: 1, Keys: map[string]*KeyUsage{}}
	raw, errRead := os.ReadFile(path)
	if errRead == nil && len(raw) > 0 {
		var loaded UsageState
		if errJSON := json.Unmarshal(raw, &loaded); errJSON != nil {
			return nil, fmt.Errorf("distribution usage: parse %s: %w", path, errJSON)
		}
		if loaded.Keys != nil {
			state.Keys = loaded.Keys
		}
	} else if errRead != nil && !os.IsNotExist(errRead) {
		return nil, fmt.Errorf("distribution usage: read %s: %w", path, errRead)
	}
	store.state = state
	return store, nil
}

// Add accumulates one completed request's computed cost and counters under
// the key hash, then persists the state file. costUSD is the usage-meters
// pricing for the request (enriched by the recorder pipeline).
func (s *UsageStore) Add(keyHash string, costUSD float64, inputTokens, outputTokens int64) {
	if s == nil || strings.TrimSpace(keyHash) == "" {
		return
	}
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	entry := s.state.Keys[keyHash]
	if entry == nil {
		entry = &KeyUsage{}
		s.state.Keys[keyHash] = entry
	}
	entry.SpentUSD += costUSD
	entry.Requests++
	entry.InputTokens += inputTokens
	entry.OutputTokens += outputTokens
	entry.LastRequestAt = s.nowFunc().UTC().Format(time.RFC3339)
	s.state.UpdatedAt = s.nowFunc().UTC().Format(time.RFC3339)
	if errPersist := s.persistLocked(); errPersist != nil {
		log.Warnf("distribution usage: persist %s failed: %v", s.path, errPersist)
	}
}

// Spent returns the cumulative spend for a key hash (0 for unknown).
func (s *UsageStore) Spent(keyHash string) float64 {
	if s == nil {
		return 0
	}
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	if entry := s.state.Keys[keyHash]; entry != nil {
		return entry.SpentUSD
	}
	return 0
}

// Reset zeroes the cumulative spend/counters for a key hash. Unknown hashes
// are accepted (idempotent for panel retries).
func (s *UsageStore) Reset(keyHash string) {
	if s == nil || strings.TrimSpace(keyHash) == "" {
		return
	}
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	if entry := s.state.Keys[keyHash]; entry != nil {
		entry.SpentUSD = 0
		entry.Requests = 0
		entry.InputTokens = 0
		entry.OutputTokens = 0
		s.state.UpdatedAt = s.nowFunc().UTC().Format(time.RFC3339)
		if errPersist := s.persistLocked(); errPersist != nil {
			log.Warnf("distribution usage: persist %s failed: %v", s.path, errPersist)
		}
	}
}

// Snapshot returns a deep copy of the per-key usage rows for reporting.
func (s *UsageStore) Snapshot() map[string]KeyUsage {
	out := map[string]KeyUsage{}
	if s == nil {
		return out
	}
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	for hash, entry := range s.state.Keys {
		if entry != nil {
			out[hash] = *entry
		}
	}
	return out
}

func (s *UsageStore) persistLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("distribution usage: empty state path")
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("distribution usage: create dir: %w", err)
		}
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("distribution usage: marshal state: %w", err)
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return fmt.Errorf("distribution usage: write temp: %w", errWrite)
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		return fmt.Errorf("distribution usage: rename: %w", errRename)
	}
	return nil
}

// HandleUsage implements the sdk usage.Plugin interface: every completed
// request with a downstream client key accumulates Under its masked hash.
func (s *UsageStore) HandleUsage(_ context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	keyValue := strings.TrimSpace(record.APIKey)
	if keyValue == "" {
		return
	}
	cost := record.Detail.CostUSD
	if cost <= 0 {
		// The native pricer enriches ComputedCostUSD inside the recorder's own
		// event; resolve the same value here so quota math matches the meters.
		if price, ok := usagestats.GlobalPricer().PriceFor(firstNonEmpty(record.ResponseModel, record.Model)); ok {
			in := record.Detail.InputTokens
			out := record.Detail.OutputTokens
			cost = float64(in)*price.Input/1_000_000 + float64(out)*price.Output/1_000_000
		}
	}
	if cost < 0 {
		cost = 0
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		cost = 0
	}
	s.Add(usagestats.MaskKeyLabel(keyValue), cost, record.Detail.InputTokens, record.Detail.OutputTokens)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var globalStore atomic.Pointer[UsageStore]

// Start boots the per-key usage store for the config's auth dir and registers
// it on the usage event stream (mirrors usagestats.Start). Safe to call once
// per process; later calls with the same path return the same store.
func Start(cfg *config.Config) (*UsageStore, error) {
	if cfg == nil || strings.TrimSpace(cfg.AuthDir) == "" {
		return nil, fmt.Errorf("distribution usage: auth-dir is required")
	}
	path := filepath.Join(cfg.AuthDir, UsageFileName)
	if existing := globalStore.Load(); existing != nil && existing.path == path {
		return existing, nil
	}
	store, err := NewUsageStore(path, nil)
	if err != nil {
		return nil, err
	}
	globalStore.Store(store)
	coreusage.RegisterNamedPlugin("distribution-usage", store)
	log.Infof("distribution usage: per-key quota accounting enabled (state=%s)", path)
	return store, nil
}

// Global returns the active store, or nil when Start hasn't run (tests wire
// their own store via SetGlobal).
func Global() *UsageStore {
	return globalStore.Load()
}

// SetGlobal installs a store (tests) or clears it (nil).
func SetGlobal(store *UsageStore) {
	globalStore.Store(store)
}
