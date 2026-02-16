package providers

import "fmt"

// StatusError captures an HTTP status code from a provider response.
// Used by adapters to return structured errors that ClassifyError can inspect.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Body)
}
