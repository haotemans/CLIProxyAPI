package usagestats

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GroupBy enumerates supported summary groupings.
type GroupBy string

// Supported groupings for /usage-meters/summary.
const (
	GroupProvider GroupBy = "provider"
	GroupModel    GroupBy = "model"
	GroupAuthFile GroupBy = "auth_file"
	GroupAPIKey   GroupBy = "api_key"
	GroupDay      GroupBy = "day"
)

// ParseGroupBy validates and normalizes a summary grouping.
func ParseGroupBy(value string) (GroupBy, bool) {
	switch GroupBy(strings.ToLower(strings.TrimSpace(value))) {
	case GroupProvider:
		return GroupProvider, true
	case GroupModel:
		return GroupModel, true
	case GroupAuthFile:
		return GroupAuthFile, true
	case GroupAPIKey:
		return GroupAPIKey, true
	case GroupDay:
		return GroupDay, true
	case "":
		return GroupProvider, true
	default:
		return "", false
	}
}

// seriesGroupings allowed on /usage-meters/series.
var seriesGroupings = map[GroupBy]bool{
	GroupProvider: true,
	GroupModel:    true,
}

// SummaryRow is one aggregated bucket from the summary endpoint.
type SummaryRow struct {
	Key             string  `json:"key"`
	Requests        int64   `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	UpstreamCostUSD float64 `json:"upstream_cost_usd"`
	ComputedCostUSD float64 `json:"computed_cost_usd"`
}

// SeriesRow is one aggregated bucket keyed by time and group.
type SeriesRow struct {
	Bucket          string  `json:"bucket"`
	Key             string  `json:"key"`
	Requests        int64   `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	UpstreamCostUSD float64 `json:"upstream_cost_usd"`
	ComputedCostUSD float64 `json:"computed_cost_usd"`
}

// Summary aggregates events between the optional millisecond time bounds.
func (r *Recorder) Summary(ctx context.Context, fromMS, toMS int64, groupBy GroupBy) ([]SummaryRow, error) {
	keyExpr, ok := sqlKeyExpression(groupBy)
	if !ok {
		return nil, fmt.Errorf("usagestats: unsupported group_by %q", groupBy)
	}
	where, args := timeBounds(fromMS, toMS)
	query := fmt.Sprintf(`SELECT %s AS key, COUNT(*) AS requests,
	COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cached_tokens),0), COALESCE(SUM(total_tokens),0),
	COALESCE(SUM(upstream_cost_usd),0), COALESCE(SUM(computed_cost_usd),0)
	FROM usage_events%s GROUP BY key ORDER BY COALESCE(SUM(computed_cost_usd),0)+COALESCE(SUM(upstream_cost_usd),0) DESC, requests DESC`, keyExpr, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usagestats: summary query: %w", err)
	}
	defer rows.Close()
	out := make([]SummaryRow, 0, 64)
	for rows.Next() {
		var row SummaryRow
		if err = rows.Scan(&row.Key, &row.Requests, &row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.TotalTokens, &row.UpstreamCostUSD, &row.ComputedCostUSD); err != nil {
			return nil, fmt.Errorf("usagestats: summary scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Series aggregates events into time buckets (hour or day) per key.
func (r *Recorder) Series(ctx context.Context, fromMS, toMS int64, interval string, groupBy GroupBy) ([]SeriesRow, error) {
	if !seriesGroupings[groupBy] {
		return nil, fmt.Errorf("usagestats: unsupported series group_by %q", groupBy)
	}
	pattern := ""
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "hour":
		pattern = "%Y-%m-%dT%H:00"
	case "day", "":
		pattern = "%Y-%m-%d"
	default:
		return nil, fmt.Errorf("usagestats: unsupported interval %q", interval)
	}
	where, args := timeBounds(fromMS, toMS)
	keyExpr, _ := sqlKeyExpression(groupBy)
	query := fmt.Sprintf(`SELECT strftime('%s', ts_ms / 1000, 'unixepoch') AS bucket, %s AS key, COUNT(*) AS requests,
	COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0),
	COALESCE(SUM(upstream_cost_usd),0), COALESCE(SUM(computed_cost_usd),0)
	FROM usage_events%s GROUP BY bucket, key ORDER BY bucket ASC, key ASC`, pattern, keyExpr, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usagestats: series query: %w", err)
	}
	defer rows.Close()
	out := make([]SeriesRow, 0, 256)
	for rows.Next() {
		var row SeriesRow
		if err = rows.Scan(&row.Bucket, &row.Key, &row.Requests, &row.InputTokens, &row.OutputTokens, &row.TotalTokens, &row.UpstreamCostUSD, &row.ComputedCostUSD); err != nil {
			return nil, fmt.Errorf("usagestats: series scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Totals roll up the entire window regardless of grouping (panel cards).
func (r *Recorder) Totals(ctx context.Context, fromMS, toMS int64) (SummaryRow, error) {
	where, args := timeBounds(fromMS, toMS)
	query := `SELECT COUNT(*) AS requests,
	COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cached_tokens),0), COALESCE(SUM(total_tokens),0),
	COALESCE(SUM(upstream_cost_usd),0), COALESCE(SUM(computed_cost_usd),0)
	FROM usage_events` + where
	row := SummaryRow{Key: "all"}
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&row.Requests, &row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.TotalTokens, &row.UpstreamCostUSD, &row.ComputedCostUSD)
	if err != nil {
		return row, fmt.Errorf("usagestats: totals query: %w", err)
	}
	return row, nil
}

func timeBounds(fromMS, toMS int64) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if fromMS > 0 {
		clauses = append(clauses, "ts_ms >= ?")
		args = append(args, fromMS)
	}
	if toMS > 0 {
		clauses = append(clauses, "ts_ms <= ?")
		args = append(args, toMS)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func sqlKeyExpression(groupBy GroupBy) (string, bool) {
	switch groupBy {
	case GroupProvider:
		return "provider", true
	case GroupModel:
		return "model", true
	case GroupAuthFile:
		return "auth_file", true
	case GroupAPIKey:
		return "api_key", true
	case GroupDay:
		return "strftime('%Y-%m-%d', ts_ms / 1000, 'unixepoch')", true
	default:
		return "", false
	}
}

// NowMS returns wall-clock milliseconds (test hook seam).
var NowMS = func() int64 { return time.Now().UnixMilli() }
