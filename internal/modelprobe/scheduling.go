package modelprobe

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultJitter is the fraction of interval randomization applied to every
// cycle's next start when the config doesn't say otherwise.
const DefaultJitter = 0.5

// DefaultProbeSpacing is the default min-max sleep before each probe.
const DefaultProbeSpacing = "3s-12s"

// SpacingRange parses a "min-max" duration range like "2s-8s" or "500ms-2s".
// Min <= max enforced by swapping; single bare durations become min==max.
func SpacingRange(value string) (minDuration, maxDuration time.Duration, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultProbeSpacing
	}
	parts := strings.SplitN(value, "-", 2)
	minPart := strings.TrimSpace(parts[0])
	maxPart := minPart
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		maxPart = strings.TrimSpace(parts[1])
	}
	if minDuration, err = time.ParseDuration(minPart); err != nil {
		return 0, 0, fmt.Errorf("modelprobe: invalid spacing min %q: %w", minPart, err)
	}
	if maxDuration, err = time.ParseDuration(maxPart); err != nil {
		return 0, 0, fmt.Errorf("modelprobe: invalid spacing max %q: %w", maxPart, err)
	}
	if maxDuration < minDuration {
		minDuration, maxDuration = maxDuration, minDuration
	}
	return minDuration, maxDuration, nil
}

// EffectiveJitter clamps a configured jitter fraction into [0,1]; negative
// clamps to 0, >1 clamps to 1. Callers pass 0 explicitly only when the user
// opted into an exact fixed cadence (documented as detectable).
func EffectiveJitter(raw float64) float64 {
	switch {
	case raw < 0:
		return 0
	case raw > 1:
		return 1
	default:
		return raw
	}
}

// jitteredInterval applies interval*(1+jitter*(2*u-1)) for u in [0,1). The
// randomness is injectable so tests are deterministic.
func jitteredInterval(interval time.Duration, jitter float64, u float64) time.Duration {
	if u < 0 {
		u = 0
	}
	if u > 1 {
		u = 1
	}
	span := 1 + jitter*(2*u-1)
	next := time.Duration(float64(interval) * span)
	if next <= 0 {
		return interval
	}
	return next
}

// parseProbeSpacingSeconds accepts "12", "3s", "750ms" or "2s-8s" as legacy
// convenience; canonical form is duration syntax.
func parseProbeSpacingSeconds(value string) (time.Duration, time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return SpacingRange("")
	}
	if !strings.Contains(value, "s") && !strings.Contains(value, "ms") {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil {
			return SpacingRange(fmt.Sprintf("%gs", seconds))
		}
	}
	return SpacingRange(value)
}
