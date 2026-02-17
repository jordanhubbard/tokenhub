package vllm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// Adapter implements router.Sender for vLLM instances.
// Supports round-robin across multiple endpoints.
type Adapter struct {
	id        string
	endpoints []string
	counter   atomic.Uint64
	client    *http.Client
}

// New creates a new vLLM adapter with one or more endpoints.
// A zero timeout defaults to 30s.
func New(id string, endpoint string, opts ...Option) *Adapter {
	a := &Adapter{
		id:        id,
		endpoints: []string{endpoint},
		client:    &http.Client{Timeout: 30 * time.Second},
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

// WithEndpoints adds additional endpoints for round-robin balancing.
func WithEndpoints(endpoints ...string) Option {
	return func(a *Adapter) {
		a.endpoints = append(a.endpoints, endpoints...)
	}
}

func (a *Adapter) ID() string { return a.id }

// HealthEndpoint returns the /health URL of the first endpoint for probing.
func (a *Adapter) HealthEndpoint() string {
	return a.endpoints[0] + "/health"
}

// nextEndpoint returns the next endpoint in round-robin order.
func (a *Adapter) nextEndpoint() string {
	idx := a.counter.Add(1) - 1
	return a.endpoints[idx%uint64(len(a.endpoints))]
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
	// Merge client parameters (temperature, max_tokens, top_p, etc.)
	for k, v := range req.Parameters {
		if k != "model" && k != "messages" { // never override model/messages
			payload[k] = v
		}
	}

	baseURL := a.nextEndpoint()
	return a.makeRequest(ctx, baseURL, "/v1/chat/completions", payload)
}

func (a *Adapter) ClassifyError(err error) *router.ClassifiedError {
	var se *providers.StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == 429:
			ce := &router.ClassifiedError{Err: err, Class: router.ErrRateLimited}
			if se.RetryAfterSecs > 0 {
				ce.RetryAfter = se.RetryAfterSecs
			}
			return ce
		case se.StatusCode >= 500:
			return &router.ClassifiedError{Err: err, Class: router.ErrTransient}
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

	baseURL := a.nextEndpoint()
	return a.makeStreamRequest(ctx, baseURL, "/v1/chat/completions", payload)
}

func (a *Adapter) makeStreamRequest(ctx context.Context, baseURL, endpoint string, payload any) (io.ReadCloser, error) {
	return providers.DoStreamRequest(ctx, a.client, baseURL+endpoint, payload, nil)
}

func (a *Adapter) makeRequest(ctx context.Context, baseURL, endpoint string, payload any) ([]byte, error) {
	return providers.DoRequest(ctx, a.client, baseURL+endpoint, payload, nil)
}
