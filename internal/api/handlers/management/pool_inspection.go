package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Pool inspection (read-only V1): grades every file-backed (OAuth) credential
// from signals CPA already tracks — disabled state, quota cooldowns, the
// usagestats error archive, and the model_probe section — and maps the
// dominant problem to an operator suggestion. No mutations.

// inspectionSuggestion codes; the panel localizes them.
const (
	suggestDelete       = "delete"        // disabled long ago → suggest removal
	suggestRelogin      = "relogin"       // auth_error dominant → suggest re-login
	suggestRotate       = "rotate"        // quota exhausted → suggest pause & rotation
	suggestWaitProvider = "wait-provider" // provider block phase → wait it out
)

const (
	// inspectionWindow is the usage-archive lookback for error signals.
	inspectionWindow = 24 * time.Hour
	// inspectionDisabledDeleteAfter is how long a credential must sit disabled
	// before removal is suggested.
	inspectionDisabledDeleteAfter = 7 * 24 * time.Hour
	// inspectionMinSignificantRequests bounds error-rate noise.
	inspectionMinSignificantRequests = 3
)

type inspectionSignals struct {
	Disabled        bool   `json:"disabled"`
	Status          string `json:"status"`
	StatusMessage   string `json:"status_message,omitempty"`
	LastActivityMS  int64  `json:"last_activity_ms,omitempty"`
	QuotaExceeded   bool   `json:"quota_exceeded"`
	QuotaReason     string `json:"quota_reason,omitempty"`
	QuotaRecoverMS  int64  `json:"quota_recover_ms,omitempty"`
	Unauthorized    bool   `json:"unauthorized"`
	Requests24H     int64  `json:"requests_24h"`
	Errors24H       int64  `json:"errors_24h"`
	DominantError   string `json:"dominant_error,omitempty"`
	ProbeCheckedAt  string `json:"probe_checked_at,omitempty"`
	ProbeAuthError  bool   `json:"probe_auth_error"`
	ProbeSkipReason string `json:"probe_skip_reason,omitempty"`
	ProbeBlocked    bool   `json:"probe_blocked,omitempty"`
}

type inspectionCredential struct {
	AuthFile    string            `json:"auth_file"`
	Provider    string            `json:"provider"`
	Health      string            `json:"health"` // good | warn | bad
	Signals     inspectionSignals `json:"signals"`
	Suggestions []string          `json:"suggestions"`
}

// isAuthErrorKind reports whether an error_kind marks a credential problem
// (401/403) rather than a rate/quota (429) or generic failure.
func isAuthErrorKind(kind string) bool {
	return kind == "http_401" || kind == "http_403"
}

// dominantErrorKind returns the most frequent error kind in the bucket.
func dominantErrorKind(bucket map[string]int64) (string, int64) {
	best, bestCount := "", int64(0)
	for kind, count := range bucket {
		if count > bestCount || (count == bestCount && kind < best) {
			best, bestCount = kind, count
		}
	}
	return best, bestCount
}

// probeAuthErrorFlagged reports whether the persisted model_probe section
// flags the credential itself (per-model auth_error or an auth_error skip).
// Provider-blocked sections are NOT auth errors: the credential is healthy,
// the provider just closed third-party access for a while.
func probeAuthErrorFlagged(section *modelprobe.Section) bool {
	if section == nil {
		return false
	}
	if section.Skipped && strings.Contains(section.SkipReason, string(modelprobe.StatusAuthError)) {
		return true
	}
	for _, outcome := range section.PerModel {
		if outcome != nil && outcome.Status == modelprobe.StatusAuthError {
			return true
		}
	}
	return false
}

// probeProviderBlocked reports whether the section marks a full block phase
// (all outcomes provider_blocked). The mark stays visible for operators but
// is not a credential problem, so it never suggests re-login.
func probeProviderBlocked(section *modelprobe.Section) bool {
	return section != nil && section.IsProviderBlocked()
}

// inspectCredential grades one credential. now, the 24h stats and the
// archive-wide last-activity map must be precomputed by the caller (one
// archive scan per request, not per credential).
func inspectCredential(auth *coreauth.Auth, now time.Time, stats usagestats.AuthFileUsageStat, lastActivityMS int64, errorKinds map[string]int64) inspectionCredential {
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = strings.TrimSpace(auth.ID)
	}
	signals := inspectionSignals{
		Disabled:       auth.Disabled || auth.Status == coreauth.StatusDisabled,
		Status:         string(auth.Status),
		StatusMessage:  strings.TrimSpace(auth.StatusMessage),
		Unauthorized:   coreauth.HasUnauthorizedAuthFailure(auth),
		Requests24H:    stats.Requests,
		Errors24H:      stats.Errors,
		LastActivityMS: lastActivityMS,
	}
	if auth.Quota.Exceeded {
		signals.QuotaExceeded = true
		signals.QuotaReason = strings.TrimSpace(auth.Quota.Reason)
		if !auth.Quota.NextRecoverAt.IsZero() {
			signals.QuotaRecoverMS = auth.Quota.NextRecoverAt.UnixMilli()
		}
	}
	dominant, dominantCount := dominantErrorKind(errorKinds)
	if dominantCount > 0 {
		signals.DominantError = dominant
	}
	section := modelprobe.ReadSection(auth.Metadata)
	if section != nil {
		signals.ProbeCheckedAt = section.CheckedAt
		signals.ProbeAuthError = probeAuthErrorFlagged(section)
		signals.ProbeBlocked = probeProviderBlocked(section)
		if section.Skipped {
			signals.ProbeSkipReason = section.SkipReason
		}
	}

	// Error-rate signals only become meaningful above the noise floor.
	significant := stats.Requests >= inspectionMinSignificantRequests && stats.Errors > 0
	authErrorsDominant := significant && stats.Errors*2 >= stats.Requests &&
		(dominant == "" || isAuthErrorKind(dominant))
	quotaDominant := significant && dominant == "http_429" && stats.Errors*2 >= stats.Requests

	suggestions := make([]string, 0, 3)
	addSuggestion := func(code string) {
		for _, existing := range suggestions {
			if existing == code {
				return
			}
		}
		suggestions = append(suggestions, code)
	}

	bad := false
	switch {
	case signals.Unauthorized, signals.ProbeAuthError, authErrorsDominant:
		addSuggestion(suggestRelogin)
		bad = true
	}
	// provider_blocked is a phase the credential rides out — warn and wait,
	// never a re-login suggestion for what is not a credential problem.
	if signals.ProbeBlocked {
		addSuggestion(suggestWaitProvider)
	}
	switch {
	case signals.QuotaExceeded:
		addSuggestion(suggestRotate)
	case quotaDominant:
		addSuggestion(suggestRotate)
	}
	// A disabled credential is dead weight when it has no recent traffic on
	// record (or never served at all). Auth timestamps mutate on refresh and
	// reset on restart, so the archive is the only trustworthy clock here.
	if signals.Disabled {
		inactiveFor := inspectionDisabledDeleteAfter + 1
		if lastActivityMS > 0 {
			inactiveFor = now.Sub(time.UnixMilli(lastActivityMS))
		}
		if lastActivityMS == 0 || inactiveFor >= inspectionDisabledDeleteAfter {
			addSuggestion(suggestDelete)
		}
	}

	health := "good"
	switch {
	case bad:
		health = "bad"
	case signals.Disabled, signals.QuotaExceeded, signals.Unauthorized,
		signals.ProbeAuthError, signals.ProbeBlocked, authErrorsDominant, quotaDominant,
		(significant && stats.Errors*5 >= stats.Requests):
		health = "warn"
	}

	return inspectionCredential{
		AuthFile:    name,
		Provider:    strings.TrimSpace(auth.Provider),
		Health:      health,
		Signals:     signals,
		Suggestions: suggestions,
	}
}

// GetPoolInspection handles GET /pool-inspection?provider=<id> (empty = all
// file-backed/OAuth credentials). Read-only: it never mutates auth state.
func (h *Handler) GetPoolInspection(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{
			"credentials": []any{},
			"summary":     gin.H{"good": 0, "warn": 0, "bad": 0},
		})
		return
	}
	providerFilter := strings.ToLower(strings.TrimSpace(c.Query("provider")))

	now := time.Now()
	fromMS := now.Add(-inspectionWindow).UnixMilli()
	var stats map[string]usagestats.AuthFileUsageStat
	var kinds map[string]map[string]int64
	var lastActivity map[string]int64
	if recorder := usagestats.Global(); recorder != nil {
		var err error
		stats, err = recorder.AuthFileStats(c.Request.Context(), fromMS, 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		kinds, err = recorder.AuthFileErrorKindCounts(c.Request.Context(), fromMS, 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		lastActivity, err = recorder.AuthFileLastActivity(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	credentials := make([]inspectionCredential, 0, 64)
	summary := map[string]int{"good": 0, "warn": 0, "bad": 0}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		// Pool inspection targets file-backed (OAuth) credentials; config
		// API-key credentials carry no "path" attribute and are skipped.
		if strings.TrimSpace(authAttribute(auth, "path")) == "" {
			continue
		}
		if providerFilter != "" && strings.ToLower(strings.TrimSpace(auth.Provider)) != providerFilter {
			continue
		}
		statKey := strings.TrimSpace(auth.FileName)
		if statKey == "" {
			statKey = strings.TrimSpace(auth.ID)
		}
		// usagestats records AuthID on every event; try both spellings.
		stat := stats[statKey]
		if stat.Requests == 0 {
			stat = stats[strings.TrimSpace(auth.ID)]
		}
		errorKinds := kinds[statKey]
		if len(errorKinds) == 0 {
			errorKinds = kinds[strings.TrimSpace(auth.ID)]
		}
		lastMS := lastActivity[statKey]
		if lastMS == 0 {
			lastMS = lastActivity[strings.TrimSpace(auth.ID)]
		}
		entry := inspectCredential(auth, now, stat, lastMS, errorKinds)
		summary[entry.Health]++
		credentials = append(credentials, entry)
	}
	sort.Slice(credentials, func(i, j int) bool {
		healthRank := map[string]int{"bad": 0, "warn": 1, "good": 2}
		if credentials[i].Health != credentials[j].Health {
			return healthRank[credentials[i].Health] < healthRank[credentials[j].Health]
		}
		return strings.ToLower(credentials[i].AuthFile) < strings.ToLower(credentials[j].AuthFile)
	})
	c.JSON(http.StatusOK, gin.H{
		"credentials": credentials,
		"summary":     summary,
		"window":      "24h",
	})
}
