package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// Adapter implements router.Sender for Anthropic.
type Adapter struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
}

// New creates a new Anthropic adapter. A zero timeout defaults to 30s.
func New(id, apiKey, baseURL string, opts ...Option) *Adapter {
	a := &Adapter{
		id:      id,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Option configures an Adapter.
type Option func(*Adapter)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(a *Adapter) {
		a.client.Timeout = d
	}
}

func (a *Adapter) ID() string { return a.id }

// HealthEndpoint returns a URL for health probing. A GET to the messages
// endpoint returns 405 (Method Not Allowed) which proves reachability.
func (a *Adapter) HealthEndpoint() string {
	return a.baseURL + "/v1/messages"
}

func (a *Adapter) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) {
	messages := make([]map[string]string, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
	}
	// Merge client parameters
	for k, v := range req.Parameters {
		if k != "model" && k != "messages" {
			payload[k] = v
		}
	}
	// Anthropic requires max_tokens — default if not provided.
	if _, ok := payload["max_tokens"]; !ok {
		payload["max_tokens"] = 4096
	}

	return a.makeRequest(ctx, "/v1/messages", payload)
}

func (a *Adapter) ClassifyError(err error) *router.ClassifiedError {
	var se *providers.StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == 429 || se.StatusCode == 529:
			ce := &router.ClassifiedError{Err: err, Class: router.ErrRateLimited}
			if se.RetryAfterSecs > 0 {
				ce.RetryAfter = se.RetryAfterSecs
			}
			return ce
		case se.StatusCode >= 500:
			return &router.ClassifiedError{Err: err, Class: router.ErrTransient}
		case strings.Contains(se.Body, "prompt is too long") || strings.Contains(se.Body, "prompt_too_long"):
			return &router.ClassifiedError{Err: err, Class: router.ErrContextOverflow}
		}
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

// SendStream sends a streaming request and returns the raw SSE response body.
func (a *Adapter) SendStream(ctx context.Context, model string, req router.Request) (io.ReadCloser, error) {
	messages := make([]map[string]string, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	for k, v := range req.Parameters {
		if k != "model" && k != "messages" && k != "stream" {
			payload[k] = v
		}
	}
	if _, ok := payload["max_tokens"]; !ok {
		payload["max_tokens"] = 4096
	}

	return a.makeStreamRequest(ctx, "/v1/messages", payload)
}

func (a *Adapter) authHeaders() map[string]string {
	return map[string]string{
		"x-api-key":         a.apiKey,
		"anthropic-version": "2023-06-01",
	}
}

func (a *Adapter) makeStreamRequest(ctx context.Context, endpoint string, payload any) (io.ReadCloser, error) {
	return providers.DoStreamRequest(ctx, a.client, a.baseURL+endpoint, payload, a.authHeaders())
}

func (a *Adapter) makeRequest(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	return providers.DoRequest(ctx, a.client, a.baseURL+endpoint, payload, a.authHeaders())
}
