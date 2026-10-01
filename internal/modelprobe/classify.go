package modelprobe

import "strings"

// notAvailableMarkers hits provider-specific text that unambiguously means
// "this account cannot use this model" — tier gates, unavailable flags and
// unknown model errors across OpenAI/Anthropic/Connect/Codewhisperer styles.
var notAvailableMarkers = []string{
	"model is not enabled",
	"model not enabled",
	"not enabled for",
	"model not available",
	"not available in your plan",
	"not supported for your account",
	"unsupported model",
	"unknown model",
	"does not exist",
	"model_not_found",
	"model is blocked",
	"not part of your subscription",
	"upgrade required",
	"pro plan",
	"on-demand",
	"abuse scope does not cover",
	"not included in your subscription",
	"resource_not_found",
	"permission denied for model",
	"not entitled",
	"model access denied",
	"insufficient credit for model",
	"not listed for this key",
	"key not authorized for model",
}

// authMarkers substrings that mean the credential itself was rejected.
var authMarkers = []string{
	"invalid token",
	"invalid api key",
	"invalid access token",
	"account suspended",
	"forbidden for this account",
	"account not found",
	"sign in again",
}

// providerBlockedMarkers are provider-specific texts unambiguously naming the
// "third-party access temporarily closed" brick wall, NOT a credential
// failure and NOT "model unavailable". Cline's periodic third-party
// clampdown answers every chat completion with 401 and this body text.
var providerBlockedMarkers = []string{
	"latest version of cline",
}

// isProviderBlockedMessage reports whether the error carries the provider's
// third-party block-phase marker.
func isProviderBlockedMessage(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range providerBlockedMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// classifyProbeError maps an executor error onto a probe status. nil means the
// request may still be classified usable (caller decides); error-free probes
// never reach here.
func classifyProbeError(err error) Status {
	if err == nil {
		return StatusUsable
	}
	// The provider's third-party block phase wins over everything: a 401 that
	// carries the marker text is neither a dead credential (auth_error) nor a
	// prunable "model unavailable".
	if isProviderBlockedMessage(err) {
		return StatusProviderBlocked
	}
	status := 0
	if se, ok := err.(interface{ StatusCode() int }); ok {
		status = se.StatusCode()
	}
	switch status {
	case 401:
		return StatusAuthError
	case 403:
		// permission_denied may be the tier gate phrased as model-deny; only
		// account-level rejection counts as an auth error.
		if isNotAvailableMessage(err) {
			return StatusNotAvailable
		}
		return StatusAuthError
	case 429:
		return StatusLimited
	case 408, 425, 502, 503, 504:
		return StatusUnreachable
	case 400, 404, 422:
		if isAuthMessage(err) {
			return StatusAuthError
		}
		return StatusNotAvailable
	}
	if isAuthMessage(err) {
		return StatusAuthError
	}
	if isNotAvailableMessage(err) {
		return StatusNotAvailable
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") || strings.Contains(msg, "too many") {
		return StatusLimited
	}
	return StatusUnreachable
}

// ClassifyError exposes the probe status classification for one-off live
// checks (e.g. the management test-model endpoint) that report outcomes in
// the same vocabulary as probe cycles.
func ClassifyError(err error) Status {
	return classifyProbeError(err)
}

func isNotAvailableMessage(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range notAvailableMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func isAuthMessage(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range authMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
