package vllm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// KeyFunc returns the current API key. It is called on every request so that
// a vault or secret-manager can rotate keys without restarting the process.
// Returning an empty string signals that no key is available.
type KeyFunc func() string

// Adapter implements router.Sender for vLLM instances.
// Supports round-robin across multiple endpoints.
type Adapter struct {
	id             string
	keyFunc        KeyFunc
	endpoints      []string
	counter        atomic.Uint64
	client         *http.Client
	reasoningModel bool // true when the model emits reasoning_content (e.g. Nemotron)
}

// New creates a new vLLM adapter with one or more endpoints.
// A zero timeout defaults to 30s. By default no API key is sent;
// use WithAPIKey or WithKeyFunc to enable authentication.
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

// WithAPIKey sets a static API key for authenticated vLLM deployments.
// The key is wrapped in a closure; use WithKeyFunc for dynamic keys.
func WithAPIKey(apiKey string) Option {
	return func(a *Adapter) {
		a.keyFunc = func() string { return apiKey }
	}
}

// WithKeyFunc sets a dynamic key resolver. This allows the vault to provide
// a live key-fetching function that returns an empty string when locked.
func WithKeyFunc(fn KeyFunc) Option {
	return func(a *Adapter) {
		a.keyFunc = fn
	}
}

// WithReasoningModel marks this adapter as serving a reasoning model (e.g.
// Nemotron-3-Super) that emits both reasoning_content and content fields in
// its responses. When set, the adapter logs a notice when reasoning tokens
// are present in streaming responses (where field-level extraction is not
// possible). Non-streaming responses are handled transparently by
// router.ExtractContentParts.
func WithReasoningModel() Option {
	return func(a *Adapter) {
		a.reasoningModel = true
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
	payload := map[string]any{
		"model":    model,
		"messages": buildOpenAIMessages(req.Messages),
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

// buildOpenAIMessages converts router.Message values into the wire shape vLLM
// (and other OpenAI-compatible servers) expect, preserving tool_calls,
// tool_call_id, and name so assistant↔tool linkage survives.
func buildOpenAIMessages(in []router.Message) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, msg := range in {
		m := map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			m["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		if msg.Name != "" {
			m["name"] = msg.Name
		}
		out[i] = m
	}
	return out
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
		case isContextOverflow(se.Body):
			return &router.ClassifiedError{Err: err, Class: router.ErrContextOverflow}
		}
		return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
	}
	// Network-level errors (timeout, connection refused, DNS failure) are transient.
	if providers.IsNetworkError(err) {
		return &router.ClassifiedError{Err: err, Class: router.ErrTransient}
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

// isContextOverflow returns true when the provider error body signals that the
// request exceeded the model's context window. vLLM and other OpenAI-compatible
// servers return HTTP 400 with these patterns for context overflow.
func isContextOverflow(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "context length") ||
		strings.Contains(lower, "context_length") ||
		strings.Contains(lower, "max_tokens") && strings.Contains(lower, "too large") ||
		strings.Contains(lower, "prompt is too long") ||
		strings.Contains(lower, "input is too long") ||
		strings.Contains(lower, "maximum context") ||
		strings.Contains(lower, "token limit") ||
		strings.Contains(lower, "too many tokens")
}

// SendStream sends a streaming request and returns the raw SSE response body.
// For reasoning models, reasoning_content tokens appear inline in the SSE
// stream as vLLM emits them; they cannot be separated at this layer. Clients
// that need to parse reasoning tokens should handle the SSE delta fields
// directly. A log notice is emitted so this is observable.
func (a *Adapter) SendStream(ctx context.Context, model string, req router.Request) (io.ReadCloser, error) {
	if a.reasoningModel {
		slog.Debug("vllm: streaming from reasoning model; reasoning_content tokens are inline in SSE deltas",
			slog.String("adapter", a.id),
			slog.String("model", model),
		)
	}
	payload := map[string]any{
		"model":    model,
		"messages": buildOpenAIMessages(req.Messages),
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

func (a *Adapter) authHeaders() map[string]string {
	if a.keyFunc == nil {
		return nil
	}
	key := a.keyFunc()
	if key == "" {
		return nil
	}
	return map[string]string{
		"Authorization": "Bearer " + key,
	}
}

func (a *Adapter) makeStreamRequest(ctx context.Context, baseURL, endpoint string, payload any) (io.ReadCloser, error) {
	streamClient := *a.client
	streamClient.Timeout = 0
	return providers.DoStreamRequest(ctx, &streamClient, baseURL+endpoint, payload, a.authHeaders())
}

func (a *Adapter) makeRequest(ctx context.Context, baseURL, endpoint string, payload any) ([]byte, error) {
	return providers.DoRequest(ctx, a.client, baseURL+endpoint, payload, a.authHeaders())
}

// SendEmbeddings proxies a raw OpenAI-compatible embeddings request using the
// adapter's registered endpoint rotation and credential resolver.
func (a *Adapter) SendEmbeddings(ctx context.Context, body []byte) ([]byte, int, error) {
	baseURL := a.nextEndpoint()
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.authHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("read response: %w", err)
	}
	return data, resp.StatusCode, nil
}
