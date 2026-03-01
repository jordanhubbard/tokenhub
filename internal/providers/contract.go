package providers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StatusError captures an HTTP status code from a provider response.
// Used by adapters to return structured errors that ClassifyError can inspect.
type StatusError struct {
	StatusCode     int
	Body           string
	RetryAfterSecs int // parsed from Retry-After header, 0 if not present
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Body)
}

// IsNetworkError reports whether err is a transient network-level error
// (timeout, connection refused, DNS failure) rather than an HTTP-level error.
// Network errors do not carry HTTP status codes and fail StatusError type
// assertions in adapter ClassifyError implementations. They are always
// transient — the provider endpoint may succeed on the next attempt.
func IsNetworkError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "dial tcp") ||
		strings.Contains(s, "EOF")
}

// ParseRetryAfter parses a Retry-After header value (seconds or HTTP-date).
func (e *StatusError) ParseRetryAfter(val string) {
	if val == "" {
		return
	}
	// Try as integer seconds first.
	if secs, err := strconv.Atoi(val); err == nil {
		e.RetryAfterSecs = secs
		return
	}
	// Try as HTTP-date.
	if t, err := time.Parse(time.RFC1123, val); err == nil {
		secs := int(time.Until(t).Seconds())
		if secs > 0 {
			e.RetryAfterSecs = secs
		}
	}
}
