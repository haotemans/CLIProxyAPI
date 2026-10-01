package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

const (
	maxUsageRangeMS = 366 * 24 * time.Hour
)

// parseUsageWindow decodes the from/to window (unix milliseconds, seconds, or
// RFC3339). Defaults: last 24h when both empty; single-sided allowed.
func parseUsageWindow(c *gin.Context) (fromMS, toMS int64, ok bool) {
	from := parseUsageTime(strings.TrimSpace(c.Query("from")))
	to := parseUsageTime(strings.TrimSpace(c.Query("to")))
	if from == 0 && to == 0 {
		to = time.Now().UnixMilli()
		from = to - int64(24*time.Hour/time.Millisecond)
	}
	if from > 0 && to > 0 {
		if from > to {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must not be after to"})
			return 0, 0, false
		}
		if to-from > int64(maxUsageRangeMS/time.Millisecond) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "time window exceeds 366 days"})
			return 0, 0, false
		}
	}
	return from, to, true
}

func parseUsageTime(value string) int64 {
	if value == "" {
		return 0
	}
	if number, err := strconv.ParseInt(value, 10, 64); err == nil {
		switch {
		case number > 1_000_000_000_000:
			// microseconds → ms (defend against common caller slip)
			return number / 1000
		case number > 100_000_000_000:
			return number
		case number > 0:
			// seconds → ms
			return number * 1000
		}
		return 0
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UnixMilli()
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return parsed.UnixMilli()
	}
	return 0
}

// GetUsageMetersSummary handles GET /usage-meters/summary.
// Shape: {rows: SummaryRow[], totals: SummaryRow, dropped, group_by, from, to}.
func (h *Handler) GetUsageMetersSummary(c *gin.Context) {
	fromMS, toMS, ok := parseUsageWindow(c)
	if !ok {
		return
	}
	groupBy, okGroup := usagestats.ParseGroupBy(c.Query("group_by"))
	if !okGroup {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group_by (provider|model|auth_file|api_key|day)"})
		return
	}
	recorder := usagestats.Global()
	if recorder == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false, "rows": []any{}, "group_by": string(groupBy),
			"from": fromMS, "to": toMS,
		})
		return
	}
	rows, err := recorder.Summary(c.Request.Context(), fromMS, toMS, groupBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	totals, errTotals := recorder.Totals(c.Request.Context(), fromMS, toMS)
	if errTotals != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errTotals.Error()})
		return
	}
	if rows == nil {
		rows = []usagestats.SummaryRow{}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  true,
		"rows":     rows,
		"totals":   totals,
		"group_by": string(groupBy),
		"from":     fromMS,
		"to":       toMS,
		"dropped":  recorder.Dropped(),
	})
}

// GetUsageMetersSeries handles GET /usage-meters/series.
// Shape: {rows: SeriesRow[], interval, group_by, from, to, dropped}.
func (h *Handler) GetUsageMetersSeries(c *gin.Context) {
	fromMS, toMS, ok := parseUsageWindow(c)
	if !ok {
		return
	}
	groupBy, okGroup := usagestats.ParseGroupBy(c.Query("group_by"))
	if !okGroup {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group_by (provider|model|auth_file|api_key|day)"})
		return
	}
	interval := strings.ToLower(strings.TrimSpace(c.Query("interval")))
	if interval == "" {
		interval = "day"
	}
	if interval != "hour" && interval != "day" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid interval (hour|day)"})
		return
	}
	recorder := usagestats.Global()
	if recorder == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false, "rows": []any{}, "interval": interval,
			"group_by": string(groupBy), "from": fromMS, "to": toMS,
		})
		return
	}
	rows, err := recorder.Series(c.Request.Context(), fromMS, toMS, interval, groupBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []usagestats.SeriesRow{}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  true,
		"rows":     rows,
		"interval": interval,
		"group_by": string(groupBy),
		"from":     fromMS,
		"to":       toMS,
		"dropped":  recorder.Dropped(),
	})
}

// GetUsageMetersEvents handles GET /usage-meters/events: a paginated,
// filterable per-request archive (newest first). Query params: from, to,
// provider, model, auth_file, api_key, status, endpoint, page, page_size.
// Shape: {enabled, rows, total, page, page_size, from, to, dropped}.
func (h *Handler) GetUsageMetersEvents(c *gin.Context) {
	fromMS, toMS, ok := parseUsageWindow(c)
	if !ok {
		return
	}
	page, errPage := parsePositiveIntParam(c, "page", 1)
	if errPage != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page"})
		return
	}
	pageSize, errSize := parsePositiveIntParam(c, "page_size", usagestats.DefaultEventPageSize)
	if errSize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page_size"})
		return
	}
	filter := usagestats.EventFilter{
		FromMS:   fromMS,
		ToMS:     toMS,
		Provider: strings.TrimSpace(c.Query("provider")),
		Model:    strings.TrimSpace(c.Query("model")),
		AuthFile: strings.TrimSpace(c.Query("auth_file")),
		APIKey:   strings.TrimSpace(c.Query("api_key")),
		Status:   strings.TrimSpace(c.Query("status")),
		Endpoint: strings.TrimSpace(c.Query("endpoint")),
		Page:     page,
		PageSize: pageSize,
	}.Normalize()

	recorder := usagestats.Global()
	if recorder == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false, "rows": []any{}, "total": 0,
			"page": filter.Page, "page_size": filter.PageSize,
			"from": fromMS, "to": toMS,
		})
		return
	}
	rows, total, err := recorder.Events(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []usagestats.EventRow{}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   true,
		"rows":      rows,
		"total":     total,
		"page":      filter.Page,
		"page_size": filter.PageSize,
		"from":      fromMS,
		"to":        toMS,
		"dropped":   recorder.Dropped(),
	})
}

// parsePositiveIntParam reads an optional positive-int query parameter,
// falling back to the default when absent.
func parsePositiveIntParam(c *gin.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}
