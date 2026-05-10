package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// KeyFunc returns the current API key. It is called on every request so that
// a vault or secret-manager can rotate keys without restarting the process.
// Returning an empty string signals that the key is unavailable (e.g. vault locked).
type KeyFunc func() string

// Adapter implements router.Sender for OpenAI.
type Adapter struct {
	id      string
	keyFunc KeyFunc
	baseURL string
	client  *http.Client
}

// New creates a new OpenAI adapter. A zero timeout defaults to 30s.
// The static apiKey is wrapped in a closure; use WithKeyFunc for dynamic keys.
func New(id, apiKey, baseURL string, opts ...Option) *Adapter {
	a := &Adapter{
		id:      id,
		keyFunc: func() string { return apiKey },
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

// WithKeyFunc sets a dynamic key resolver, overriding any static key
// passed to New. This allows the vault to provide a live key-fetching
// function that returns an empty string when locked.
func WithKeyFunc(fn KeyFunc) Option {
	return func(a *Adapter) {
		a.keyFunc = fn
	}
}

func (a *Adapter) ID() string { return a.id }

// HealthEndpoint returns a lightweight URL for health probing.
func (a *Adapter) HealthEndpoint() string {
	return a.baseURL + "/v1/models"
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

	return a.makeRequest(ctx, "/v1/chat/completions", payload)
}

// buildOpenAIMessages converts router.Message values into the wire shape
// expected by OpenAI-compatible providers. It preserves tool_calls,
// tool_call_id, and name so assistant↔tool linkage survives the hop —
// upstream litellm/Azure adapters fail with a "tool_call_id" KeyError
// when those fields are stripped.
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
		case strings.Contains(se.Body, "context_length_exceeded") ||
			strings.Contains(se.Body, "context length") ||
			se.StatusCode == 400 && strings.Contains(se.Body, "max_tokens") && strings.Contains(se.Body, "too large"):
			return &router.ClassifiedError{Err: err, Class: router.ErrContextOverflow}
		case se.StatusCode == 400 && strings.Contains(se.Body, "budget_exceeded"):
			return &router.ClassifiedError{Err: err, Class: router.ErrBudgetExceeded}
		}
		return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
	}
	// Network-level errors (timeout, connection refused, DNS failure) are transient.
	if providers.IsNetworkError(err) {
		return &router.ClassifiedError{Err: err, Class: router.ErrTransient}
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

// SendStream sends a streaming request and returns the raw SSE response body.
func (a *Adapter) SendStream(ctx context.Context, model string, req router.Request) (io.ReadCloser, error) {
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

	return a.makeStreamRequest(ctx, "/v1/chat/completions", payload)
}

func (a *Adapter) authHeaders() map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + a.keyFunc(),
	}
}

func (a *Adapter) makeStreamRequest(ctx context.Context, endpoint string, payload any) (io.ReadCloser, error) {
	streamClient := *a.client
	streamClient.Timeout = 0
	return providers.DoStreamRequest(ctx, &streamClient, a.baseURL+endpoint, payload, a.authHeaders())
}

func (a *Adapter) makeRequest(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	return providers.DoRequest(ctx, a.client, a.baseURL+endpoint, payload, a.authHeaders())
}

// ForwardRawStream implements router.AnthropicRawSender streaming for backends
// (e.g. NVIDIA NIM) that accept the Anthropic /v1/messages wire format.
func (a *Adapter) ForwardRawStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	url := a.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.authHeaders() {
		req.Header.Set(k, v)
	}
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, data)
	}
	return resp.Body, nil
}

// ForwardRaw implements router.AnthropicRawSender for backends (e.g. NVIDIA NIM)
// that accept the Anthropic /v1/messages wire format alongside OpenAI.
func (a *Adapter) ForwardRaw(ctx context.Context, body []byte) ([]byte, int, error) {
	url := a.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
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

// SendEmbeddings proxies a raw OpenAI-compatible embeddings request using the
// adapter's registered base URL and credential resolver.
func (a *Adapter) SendEmbeddings(ctx context.Context, body []byte) ([]byte, int, error) {
	url := a.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
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
