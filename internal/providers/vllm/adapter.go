package vllm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (a *Adapter) makeRequest(ctx context.Context, baseURL, endpoint string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		se := &providers.StatusError{StatusCode: resp.StatusCode, Body: string(body)}
		se.ParseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, se
	}

	return body, nil
}
