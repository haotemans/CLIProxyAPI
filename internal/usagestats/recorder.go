// Package usagestats provides native, persistent usage and cost statistics:
// completed-request usage records are tapped from the sdk usage manager into
// a SQLite store (pure-Go modernc driver, CGO-free builds keep working) and
// exposed through management query endpoints. It is fully additive and does
// not share code with plugins or quota subsystems.
package usagestats

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	_ "modernc.org/sqlite"
)

const (
	// channelSize bounds pending events awaiting the single-writer goroutine.
	// Overflow drops the event and counts it; the request path never blocks.
	channelSize = 4096

	// schemaVersion is bumped with each additive migration.
	schemaVersion = 1

	flushInterval = time.Second
	flushBatchCap = 256
)

// Event is the flattened unit persisted per completed request.
type Event struct {
	TimestampMS     int64
	Provider        string
	AuthFile        string
	APIKeyHash      string
	Endpoint        string
	Model           string
	UpstreamModel   string
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	TotalTokens     int64
	UpstreamCostUSD float64
	ComputedCostUSD float64
	Status          string
	LatencyMS       int64
	ErrorKind       string
}

// Config controls recorder creation.
type Config struct {
	// Path is the sqlite database file; required.
	Path string
	// RetentionDays bounds retention; events older than this get pruned by
	// the writer goroutine (hourly). Zero disables pruning.
	RetentionDays int
	// Pricer resolves computed cost per event. Nil disables computed cost.
	Pricer *Pricer
}

// Recorder persists usage events into sqlite via a single writer goroutine.
type Recorder struct {
	db      *sql.DB
	pricer  *Pricer
	config  Config
	ch      chan Event
	done    chan struct{}
	wg      sync.WaitGroup
	closed  int32
	dropped int64
}

// Open migrates the schema and starts the writer goroutine.
func Open(cfg Config) (*Recorder, error) {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return nil, errors.New("usagestats: database path is required")
	}
	cfg.Path = path
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("usagestats: create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("usagestats: open %s: %w", path, err)
	}
	if err = configureDatabase(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	recorder := &Recorder{
		db:     db,
		pricer: cfg.Pricer,
		config: cfg,
		ch:     make(chan Event, channelSize),
		done:   make(chan struct{}),
	}
	recorder.wg.Add(1)
	go recorder.run()
	return recorder, nil
}

func configureDatabase(db *sql.DB) error {
	settings := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = OFF",
		"PRAGMA wal_autocheckpoint = 1000",
	}
	for _, stmt := range settings {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("usagestats: %s: %w", stmt, err)
		}
	}
	db.SetMaxOpenConns(2)
	return nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS usage_stats_meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return fmt.Errorf("usagestats: meta table: %w", err)
	}
	current := 0
	row := db.QueryRow(`SELECT value FROM usage_stats_meta WHERE key = 'schema_version'`)
	var existing string
	switch err := row.Scan(&existing); err {
	case nil:
		_, _ = fmt.Sscanf(existing, "%d", &current)
	case sql.ErrNoRows:
	default:
		return fmt.Errorf("usagestats: read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("usagestats: database schema %d newer than supported %d", current, schemaVersion)
	}
	if current == 0 {
		ddl := `
CREATE TABLE IF NOT EXISTS usage_events (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	ts_ms           INTEGER NOT NULL,
	provider        TEXT NOT NULL,
	auth_file       TEXT NOT NULL,
	api_key         TEXT NOT NULL DEFAULT '',
	endpoint        TEXT NOT NULL DEFAULT '',
	model           TEXT NOT NULL DEFAULT '',
	upstream_model  TEXT NOT NULL DEFAULT '',
	input_tokens    INTEGER NOT NULL DEFAULT 0,
	output_tokens   INTEGER NOT NULL DEFAULT 0,
	cached_tokens   INTEGER NOT NULL DEFAULT 0,
	total_tokens    INTEGER NOT NULL DEFAULT 0,
	upstream_cost_usd  REAL NOT NULL DEFAULT 0,
	computed_cost_usd  REAL NOT NULL DEFAULT 0,
	status          TEXT NOT NULL DEFAULT 'ok',
	latency_ms      INTEGER NOT NULL DEFAULT 0,
	error_kind      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usage_events_ts ON usage_events(ts_ms);
CREATE INDEX IF NOT EXISTS idx_usage_events_provider ON usage_events(provider);
CREATE INDEX IF NOT EXISTS idx_usage_events_auth_file ON usage_events(auth_file);
CREATE INDEX IF NOT EXISTS idx_usage_events_model ON usage_events(model);
`
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("usagestats: create schema: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO usage_stats_meta (key, value) VALUES ('schema_version', ?)`, fmt.Sprint(schemaVersion)); err != nil {
			return fmt.Errorf("usagestats: set schema version: %w", err)
		}
	}
	return nil
}

// newForTest builds a recorder against an already-opened database without
// starting the writer goroutine, so tests can push events and flush them
// deterministically via flushOnce/insertBatch.
func newForTest(db *sql.DB, cfg Config) (*Recorder, error) {
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Recorder{
		db:     db,
		pricer: cfg.Pricer,
		config: cfg,
		ch:     make(chan Event, channelSize),
		done:   make(chan struct{}),
	}, nil
}

// Close stops the writer, drains pending events, and closes the database.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	if atomic.CompareAndSwapInt32(&r.closed, 0, 1) {
		close(r.done)
	}
	r.wg.Wait()
	return r.db.Close()
}

// Record queues an event for the writer. Overflowing the bounded channel
// drops the event (counted, never blocks the request path).
func (r *Recorder) Record(ev Event) {
	if r == nil || atomic.LoadInt32(&r.closed) == 1 {
		return
	}
	select {
	case r.ch <- ev:
	default:
		atomic.AddInt64(&r.dropped, 1)
	}
}

// Dropped returns the count of events discarded because the writer lagged.
func (r *Recorder) Dropped() int64 { return atomic.LoadInt64(&r.dropped) }

func (r *Recorder) run() {
	defer r.wg.Done()
	flushTick := time.NewTicker(flushInterval)
	pruneTick := time.NewTicker(time.Hour)
	defer flushTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-flushTick.C:
			r.flushOnce()
		case <-pruneTick.C:
			r.prune()
		case <-r.done:
			r.flushAll()
			return
		}
	}
}

// Flush synchronously writes all currently queued events. Primarily for
// tests; production uses the internal 1-second flush ticker. Concurrent
// writers are serialized by the single-writer channel contract.
func (r *Recorder) Flush() {
	if r == nil {
		return
	}
	for {
		size := len(r.ch)
		if size == 0 {
			return
		}
		r.flushOnce()
	}
}

// flushOnce drains at most flushBatchCap events into one insert transaction.
func (r *Recorder) flushOnce() {
	batch := make([]Event, 0, flushBatchCap)
	for len(batch) < flushBatchCap {
		select {
		case ev := <-r.ch:
			batch = append(batch, r.enrich(ev))
		default:
			if len(batch) > 0 {
				r.insertBatch(batch)
			}
			return
		}
	}
	r.insertBatch(batch)
}

// flushAll drains every pending event (shutdown path).
func (r *Recorder) flushAll() {
	for {
		select {
		case ev := <-r.ch:
			r.insertBatch([]Event{r.enrich(ev)})
		default:
			return
		}
	}
}

var insertColumns = `(ts_ms, provider, auth_file, api_key, endpoint, model, upstream_model, input_tokens, output_tokens, cached_tokens, total_tokens, upstream_cost_usd, computed_cost_usd, status, latency_ms, error_kind)`

func (r *Recorder) insertBatch(batch []Event) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO usage_events ` + insertColumns + ` VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer func() { _ = stmt.Close() }()
	for _, ev := range batch {
		if ev.TimestampMS == 0 {
			ev.TimestampMS = time.Now().UnixMilli()
		}
		if _, err = stmt.Exec(
			ev.TimestampMS, ev.Provider, ev.AuthFile, ev.APIKeyHash, ev.Endpoint, ev.Model, ev.UpstreamModel,
			ev.InputTokens, ev.OutputTokens, ev.CachedTokens, ev.TotalTokens,
			ev.UpstreamCostUSD, ev.ComputedCostUSD, ev.Status, ev.LatencyMS, ev.ErrorKind,
		); err != nil {
			_ = tx.Rollback()
			return
		}
	}
	_ = tx.Commit()
}

// enrich attaches computed cost via the price table (cheap, deterministic).
func (r *Recorder) enrich(ev Event) Event {
	if r.pricer != nil && ev.ComputedCostUSD == 0 {
		ev.ComputedCostUSD = r.pricer.Cost(ev.Model, ev.UpstreamModel, ev.InputTokens, ev.OutputTokens)
	}
	return ev
}

// prune deletes events older than the retention window.
func (r *Recorder) prune() {
	if r.config.RetentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(r.config.RetentionDays) * 24 * time.Hour).UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, _ = r.db.ExecContext(ctx, `DELETE FROM usage_events WHERE ts_ms < ?`, cutoff)
}

// HandleUsage implements the sdk usage.Plugin interface: adapt a completed
// record into an Event and enqueue it.
func (r *Recorder) HandleUsage(ctx context.Context, record coreusage.Record) {
	if r == nil {
		return
	}
	r.Record(EventFromRecord(ctx, record))
}

// MaskKeyLabel turns a downstream client key (Principal from the access
// layer) into a stable 8-hex sha256 prefix, so raw keys never reach the
// database. Empty input stays empty (requests through non-keyed access).
func MaskKeyLabel(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// endpointFromContext resolves the endpoint kind from the inbound gin route
// when available (e.g. "/v1/chat/completions"); empty otherwise.
func endpointFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if path := strings.TrimSpace(ginCtx.FullPath()); path != "" {
			return path
		}
	}
	return ""
}

// EventFromRecord adapts a coreusage.Record to the storage Event.
func EventFromRecord(ctx context.Context, record coreusage.Record) Event {
	provider := strings.TrimSpace(record.Provider)
	model := strings.TrimSpace(record.Model)
	upstreamModel := strings.TrimSpace(record.ResponseModel)
	if upstreamModel == "" {
		upstreamModel = model
	}
	endpoint := endpointFromContext(ctx)
	status := "ok"
	errorKind := ""
	if record.Failed {
		status = "error"
		if code := record.Fail.StatusCode; code > 0 {
			errorKind = fmt.Sprintf("http_%d", code)
		} else {
			errorKind = "upstream_error"
		}
	}
	ts := record.RequestedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	return Event{
		TimestampMS:     ts.UnixMilli(),
		Provider:        provider,
		AuthFile:        strings.TrimSpace(record.AuthID),
		APIKeyHash:      MaskKeyLabel(record.APIKey),
		Endpoint:        endpoint,
		Model:           model,
		UpstreamModel:   upstreamModel,
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		CachedTokens:    record.Detail.CacheReadTokens + record.Detail.CacheCreationTokens + record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
		UpstreamCostUSD: record.Detail.CostUSD,
		Status:          status,
		LatencyMS:       record.Latency.Milliseconds(),
		ErrorKind:       errorKind,
	}
}
