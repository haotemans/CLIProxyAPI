package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/distribution"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	"github.com/tidwall/gjson"
)

// distributionModelBodyCap bounds how much of a request body the middleware
// buffers to resolve the model for whitelist checks.
const distributionModelBodyCap = 16 << 20

// distributionKeysMiddleware enforces the issued-key lifecycle for requests
// that authenticated with a distribution key: enabled/expiry, per-key model
// whitelist, and the USD quota spent so far. Master keys (plain
// access.api-keys entries) and unauthenticated flows pass through untouched.
// nowFn pins the clock for expiry tests.
func distributionKeysMiddleware(getCfg func() *config.Config, nowFn func() time.Time) gin.HandlerFunc {
	if nowFn == nil {
		nowFn = time.Now
	}
	return func(c *gin.Context) {
		principal := distributionPrincipal(c)
		if principal == "" {
			c.Next()
			return
		}
		cfg := getCfg()
		if cfg == nil {
			c.Next()
			return
		}
		entry := findDistributionKey(cfg.DistributionKeys, principal)
		if entry == nil {
			// Master key (access.api-keys) or a principal from another access
			// provider — distribution rules do not apply.
			c.Next()
			return
		}
		if !distributionKeyEnabled(entry) {
			distributionReject(c, http.StatusForbidden, "distribution key is disabled", "authentication_error", nil, "key_disabled")
			return
		}
		if expires := strings.TrimSpace(entry.ExpiresAt); expires != "" {
			if at, errParse := time.Parse(time.RFC3339, expires); errParse == nil && !nowFn().Before(at) {
				distributionReject(c, http.StatusForbidden, "distribution key expired at "+expires, "authentication_error", nil, "key_expired")
				return
			}
		}
		keyHash := usagestats.MaskKeyLabel(principal)
		if entry.QuotaUSD > 0 {
			if store := distribution.Global(); store != nil {
				if spent := store.Spent(keyHash); spent >= entry.QuotaUSD {
					distributionReject(c, http.StatusTooManyRequests,
						fmt.Sprintf("quota exceeded for this key: $%.4f spent of a $%.2f cap", spent, entry.QuotaUSD),
						"insufficient_quota", nil, "quota_exceeded")
					return
				}
			}
		}
		if len(entry.AllowedModels) > 0 {
			if model := distributionRequestModel(c); model != "" && !distributionModelAllowed(entry.AllowedModels, model) {
				distributionReject(c, http.StatusForbidden,
					fmt.Sprintf("model %q is not allowed for this key", model),
					"invalid_request_error", "model", "model_not_allowed")
				return
			}
		}
		c.Set("distributionKeyHash", keyHash)
		c.Next()
	}
}

func distributionPrincipal(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get("userApiKey")
	if !exists {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func findDistributionKey(entries []config.DistributionKey, key string) *config.DistributionKey {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	for i := range entries {
		if entries[i].Key == key {
			return &entries[i]
		}
	}
	return nil
}

func distributionKeyEnabled(entry *config.DistributionKey) bool {
	return entry == nil || entry.Enabled == nil || *entry.Enabled
}

func distributionModelAllowed(allowed []string, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return true
	}
	for _, candidate := range allowed {
		if strings.ToLower(strings.TrimSpace(candidate)) == model {
			return true
		}
	}
	return false
}

// distributionRequestModel extracts the model field from a JSON request body
// (POST/PUT/PATCH), restoring the body for the downstream handler. GETs and
// non-JSON payloads return empty (nothing to whitelist-check).
func distributionRequestModel(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return ""
	}
	method := strings.ToUpper(c.Request.Method)
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return ""
	}
	contentType := c.GetHeader("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "json") {
		return ""
	}
	body, errRead := io.ReadAll(io.LimitReader(c.Request.Body, distributionModelBodyCap))
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
	if errRead != nil || len(body) == 0 {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

func distributionReject(c *gin.Context, status int, message, errType string, param any, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    errType,
		"param":   param,
		"code":    code,
	}})
}
