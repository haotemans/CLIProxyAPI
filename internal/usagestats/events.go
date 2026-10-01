package usagestats

import (
	"context"
	"fmt"
	"strings"
)

// EventFilter narrows the paginated per-request event listing. Empty fields
// match everything; Page is 1-based; PageSize defaults to 50 and caps at 200.
type EventFilter struct {
	FromMS   int64
	ToMS     int64
	Provider string
	Model    string
	AuthFile string
	APIKey   string
	Status   string
	Endpoint string
	Page     int
	PageSize int
}

// EventRow is one usage_events row in API form (ts in unix milliseconds).
type EventRow struct {
	ID              int64   `json:"id"`
	TsMS            int64   `json:"ts"`
	Provider        string  `json:"provider"`
	AuthFile        string  `json:"auth_file"`
	APIKey          string  `json:"api_key"`
	Endpoint        string  `json:"endpoint"`
	Model           string  `json:"model"`
	UpstreamModel   string  `json:"upstream_model"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	UpstreamCostUSD float64 `json:"upstream_cost_usd"`
	ComputedCostUSD float64 `json:"computed_cost_usd"`
	Status          string  `json:"status"`
	LatencyMS       int64   `json:"latency_ms"`
	ErrorKind       string  `json:"error"`
}

// DefaultEventPageSize is the listing default; MaxEventPageSize bounds it.
const (
	DefaultEventPageSize = 50
	MaxEventPageSize     = 200
)

// Normalize clamps pagination to the supported bounds (page >= 1).
func (f EventFilter) Normalize() EventFilter {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = DefaultEventPageSize
	}
	if f.PageSize > MaxEventPageSize {
		f.PageSize = MaxEventPageSize
	}
	return f
}

// eventColumns is the select list shared by the row query; keep in sync with
// the scan order in Events.
const eventColumns = `id, ts_ms, provider, auth_file, api_key, endpoint, model, upstream_model,
	input_tokens, output_tokens, cached_tokens, total_tokens, upstream_cost_usd, computed_cost_usd, status, latency_ms, error_kind`

// Events returns one page of per-request rows (newest first) plus the total
// number of rows matching the filter.
func (r *Recorder) Events(ctx context.Context, filter EventFilter) ([]EventRow, int64, error) {
	filter = filter.Normalize()
	where, args := eventFilterWhere(filter)

	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("usagestats: events count: %w", err)
	}

	query := `SELECT ` + eventColumns + ` FROM usage_events` + where +
		` ORDER BY ts_ms DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("usagestats: events query: %w", err)
	}
	defer rows.Close()
	out := make([]EventRow, 0, filter.PageSize)
	for rows.Next() {
		var row EventRow
		if err = rows.Scan(
			&row.ID, &row.TsMS, &row.Provider, &row.AuthFile, &row.APIKey, &row.Endpoint,
			&row.Model, &row.UpstreamModel, &row.InputTokens, &row.OutputTokens, &row.CachedTokens,
			&row.TotalTokens, &row.UpstreamCostUSD, &row.ComputedCostUSD, &row.Status,
			&row.LatencyMS, &row.ErrorKind,
		); err != nil {
			return nil, 0, fmt.Errorf("usagestats: events scan: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// eventFilterWhere builds the WHERE clause from the equality filters and the
// optional time bounds. All values are parameterized.
func eventFilterWhere(filter EventFilter) (string, []any) {
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if filter.FromMS > 0 {
		clauses = append(clauses, "ts_ms >= ?")
		args = append(args, filter.FromMS)
	}
	if filter.ToMS > 0 {
		clauses = append(clauses, "ts_ms <= ?")
		args = append(args, filter.ToMS)
	}
	for _, eq := range []struct {
		column string
		value  string
	}{
		{"provider", filter.Provider},
		{"model", filter.Model},
		{"auth_file", filter.AuthFile},
		{"api_key", filter.APIKey},
		{"status", filter.Status},
		{"endpoint", filter.Endpoint},
	} {
		value := strings.TrimSpace(eq.value)
		if value == "" {
			continue
		}
		clauses = append(clauses, eq.column+" = ?")
		args = append(args, value)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// AuthFileUsageStat aggregates success/error counts per auth_file over a
// window; used by the pool-inspection endpoint for error-rate signals.
// LastTsMS is the newest event timestamp seen inside the window (0 when the
// credential has no archived events at all).
type AuthFileUsageStat struct {
	Requests int64 `json:"requests"`
	Errors   int64 `json:"errors"`
	LastTsMS int64 `json:"last_ts_ms"`
}

// AuthFileStats aggregates per-auth-file request/error counts between the
// optional millisecond bounds.
func (r *Recorder) AuthFileStats(ctx context.Context, fromMS, toMS int64) (map[string]AuthFileUsageStat, error) {
	where, args := timeBounds(fromMS, toMS)
	rows, err := r.db.QueryContext(ctx, `SELECT auth_file, COUNT(*), COALESCE(SUM(status <> 'ok'),0), COALESCE(MAX(ts_ms),0)
		FROM usage_events`+where+` GROUP BY auth_file`, args...)
	if err != nil {
		return nil, fmt.Errorf("usagestats: auth-file stats query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]AuthFileUsageStat, 64)
	for rows.Next() {
		var key string
		var stat AuthFileUsageStat
		if err = rows.Scan(&key, &stat.Requests, &stat.Errors, &stat.LastTsMS); err != nil {
			return nil, fmt.Errorf("usagestats: auth-file stats scan: %w", err)
		}
		out[key] = stat
	}
	return out, rows.Err()
}

// AuthFileLastActivity returns the newest archived event timestamp per
// auth_file across the whole archive (no window); pool-inspection uses it to
// age disabled credentials without trusting mutable auth timestamps.
func (r *Recorder) AuthFileLastActivity(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT auth_file, MAX(ts_ms) FROM usage_events GROUP BY auth_file`)
	if err != nil {
		return nil, fmt.Errorf("usagestats: auth-file last-activity query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int64, 64)
	for rows.Next() {
		var key string
		var last int64
		if err = rows.Scan(&key, &last); err != nil {
			return nil, fmt.Errorf("usagestats: auth-file last-activity scan: %w", err)
		}
		out[key] = last
	}
	return out, rows.Err()
}

// AuthFileErrorKindCounts aggregates per-auth-file error counts grouped by
// error_kind over a window (e.g. http_401 vs http_429 dominance).
func (r *Recorder) AuthFileErrorKindCounts(ctx context.Context, fromMS, toMS int64) (map[string]map[string]int64, error) {
	where, args := timeBounds(fromMS, toMS)
	if where == "" {
		where = " WHERE status <> 'ok'"
	} else {
		where += " AND status <> 'ok'"
	}
	rows, err := r.db.QueryContext(ctx, `SELECT auth_file, error_kind, COUNT(*)
		FROM usage_events`+where+` GROUP BY auth_file, error_kind`, args...)
	if err != nil {
		return nil, fmt.Errorf("usagestats: auth-file error kinds query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]map[string]int64, 64)
	for rows.Next() {
		var key, kind string
		var count int64
		if err = rows.Scan(&key, &kind, &count); err != nil {
			return nil, fmt.Errorf("usagestats: auth-file error kinds scan: %w", err)
		}
		bucket := out[key]
		if bucket == nil {
			bucket = make(map[string]int64, 4)
			out[key] = bucket
		}
		bucket[kind] += count
	}
	return out, rows.Err()
}
