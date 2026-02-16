package providers

import (
	"fmt"
	"strconv"
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
