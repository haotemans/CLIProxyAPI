package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Kiro retry configuration and backoff helpers.
// (split from kiro_executor.go; no behavior change)

const (
	// Kiro API common constants
	kiroContentType  = "application/json"
	kiroAcceptStream = "*/*"

	// Event Stream frame size constants for boundary protection
	// AWS Event Stream binary format: prelude (12 bytes) + headers + payload + message_crc (4 bytes)
	// Prelude consists of: total_length (4) + headers_length (4) + prelude_crc (4)
	minEventStreamFrameSize = 16       // Minimum: 4(total_len) + 4(headers_len) + 4(prelude_crc) + 4(message_crc)
	maxEventStreamMsgSize   = 10 << 20 // Maximum message length: 10MB

	// Event Stream error type constants
	ErrStreamFatal     = "fatal"     // Connection/authentication errors, not recoverable
	ErrStreamMalformed = "malformed" // Format errors, data cannot be parsed

	// kiroIDEAgentMode is the agent mode header value for Kiro IDE requests
	kiroIDEAgentMode = "vibe"

	// Socket retry configuration constants
	// Maximum number of retry attempts for socket/network errors
	kiroSocketMaxRetries = 3
	// Base delay between retry attempts (uses exponential backoff: delay * 2^attempt)
	kiroSocketBaseRetryDelay = 1 * time.Second
	// Maximum delay between retry attempts (cap for exponential backoff)
	kiroSocketMaxRetryDelay = 30 * time.Second
	// First token timeout for streaming responses (how long to wait for first response)
	kiroFirstTokenTimeout = 15 * time.Second
	// Streaming read timeout (how long to wait between chunks)
	kiroStreamingReadTimeout = 300 * time.Second
)

// retryableHTTPStatusCodes defines HTTP status codes that are considered retryable.
// Based on kiro2Api reference: 502 (Bad Gateway), 503 (Service Unavailable), 504 (Gateway Timeout)
var retryableHTTPStatusCodes = map[int]bool{
	502: true, // Bad Gateway - upstream server error
	503: true, // Service Unavailable - server temporarily overloaded
	504: true, // Gateway Timeout - upstream server timeout
}

// Real-time usage estimation configuration
// These control how often usage updates are sent during streaming
var (
	usageUpdateCharThreshold = 5000             // Send usage update every 5000 characters
	usageUpdateTimeInterval  = 15 * time.Second // Or every 15 seconds, whichever comes first
)

// endpointAliases maps user preference values to canonical endpoint names.
var endpointAliases = map[string]string{
	"codewhisperer": "codewhisperer",
	"ide":           "codewhisperer",
	"amazonq":       "amazonq",
	"q":             "amazonq",
	"cli":           "amazonq",
}

func enqueueTranslatedSSE(out chan<- cliproxyexecutor.StreamChunk, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
}

// retryConfig holds configuration for socket retry logic.
// Based on kiro2Api Python implementation patterns.
type retryConfig struct {
	MaxRetries      int           // Maximum number of retry attempts
	BaseDelay       time.Duration // Base delay between retries (exponential backoff)
	MaxDelay        time.Duration // Maximum delay cap
	RetryableErrors []string      // List of retryable error patterns
	RetryableStatus map[int]bool  // HTTP status codes to retry
	FirstTokenTmout time.Duration // Timeout for first token in streaming
	StreamReadTmout time.Duration // Timeout between stream chunks
}

// defaultRetryConfig returns the default retry configuration for Kiro socket operations.
func defaultRetryConfig() retryConfig {
	return retryConfig{
		MaxRetries:      kiroSocketMaxRetries,
		BaseDelay:       kiroSocketBaseRetryDelay,
		MaxDelay:        kiroSocketMaxRetryDelay,
		RetryableStatus: retryableHTTPStatusCodes,
		RetryableErrors: []string{
			"connection reset",
			"connection refused",
			"broken pipe",
			"EOF",
			"timeout",
			"temporary failure",
			"no such host",
			"network is unreachable",
			"i/o timeout",
		},
		FirstTokenTmout: kiroFirstTokenTimeout,
		StreamReadTmout: kiroStreamingReadTimeout,
	}
}

// isRetryableError checks if an error is retryable based on error type and message.
// Returns true for network timeouts, connection resets, and temporary failures.
// Based on kiro2Api's retry logic patterns.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context cancellation - not retryable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Check for net.Error (timeout, temporary)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			log.Debugf("kiro: isRetryableError: network timeout detected")
			return true
		}
		// Note: Temporary() is deprecated but still useful for some error types
	}

	// Check for specific syscall errors (connection reset, broken pipe, etc.)
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		switch syscallErr {
		case syscall.ECONNRESET: // Connection reset by peer
			log.Debugf("kiro: isRetryableError: ECONNRESET detected")
			return true
		case syscall.ECONNREFUSED: // Connection refused
			log.Debugf("kiro: isRetryableError: ECONNREFUSED detected")
			return true
		case syscall.EPIPE: // Broken pipe
			log.Debugf("kiro: isRetryableError: EPIPE (broken pipe) detected")
			return true
		case syscall.ETIMEDOUT: // Connection timed out
			log.Debugf("kiro: isRetryableError: ETIMEDOUT detected")
			return true
		case syscall.ENETUNREACH: // Network is unreachable
			log.Debugf("kiro: isRetryableError: ENETUNREACH detected")
			return true
		case syscall.EHOSTUNREACH: // No route to host
			log.Debugf("kiro: isRetryableError: EHOSTUNREACH detected")
			return true
		}
	}

	// Check for net.OpError wrapping other errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		log.Debugf("kiro: isRetryableError: net.OpError detected, op=%s", opErr.Op)
		// Recursively check the wrapped error
		if opErr.Err != nil {
			return isRetryableError(opErr.Err)
		}
		return true
	}

	// Check error message for retryable patterns
	errMsg := strings.ToLower(err.Error())
	cfg := defaultRetryConfig()
	for _, pattern := range cfg.RetryableErrors {
		if strings.Contains(errMsg, pattern) {
			log.Debugf("kiro: isRetryableError: pattern '%s' matched in error: %s", pattern, errMsg)
			return true
		}
	}

	// Check for EOF which may indicate connection was closed
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		log.Debugf("kiro: isRetryableError: EOF/UnexpectedEOF detected")
		return true
	}

	return false
}

// isRetryableHTTPStatus checks if an HTTP status code is retryable.
// Based on kiro2Api: 502, 503, 504 are retryable server errors.
func isRetryableHTTPStatus(statusCode int) bool {
	return retryableHTTPStatusCodes[statusCode]
}

// calculateRetryDelay calculates the delay for the next retry attempt using exponential backoff.
// delay = min(baseDelay * 2^attempt, maxDelay)
// Adds ±30% jitter to prevent thundering herd.
func calculateRetryDelay(attempt int, cfg retryConfig) time.Duration {
	return kiroauth.ExponentialBackoffWithJitter(attempt, cfg.BaseDelay, cfg.MaxDelay)
}

// logRetryAttempt logs a retry attempt with relevant context.
func logRetryAttempt(attempt, maxRetries int, reason string, delay time.Duration, endpoint string) {
	log.Warnf("kiro: retry attempt %d/%d for %s, waiting %v before next attempt (endpoint: %s)",
		attempt+1, maxRetries, reason, delay, endpoint)
}

// kiroHTTPClientPool provides a shared HTTP client with connection pooling for Kiro API.
// This reduces connection overhead and improves performance for concurrent requests.
// Based on kiro2Api's connection pooling pattern.
