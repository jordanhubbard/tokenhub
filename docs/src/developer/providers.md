# Provider Adapters

Provider adapters translate TokenHub's generic request format into provider-specific API calls. Each adapter implements the `router.Sender` interface.

## Interface

```go
// Sender is the core provider adapter interface.
type Sender interface {
    ID() string
    Send(ctx context.Context, model string, req Request) (ProviderResponse, error)
    ClassifyError(err error) *ClassifiedError
}

// StreamSender extends Sender with streaming support.
type StreamSender interface {
    Sender
    SendStream(ctx context.Context, model string, req Request) (io.ReadCloser, error)
}

// Probeable enables active health probing.
type Probeable interface {
    ID() string
    HealthEndpoint() string
}
```

`ProviderResponse` is `[]byte` (raw JSON from the provider).

## Existing Adapters

### OpenAI (`internal/providers/openai/`)

- Endpoint: `POST {baseURL}/v1/chat/completions`
- Health: `GET {baseURL}/v1/models`
- Auth: `Authorization: Bearer {apiKey}`
- Request translation: Maps `req.Messages` to OpenAI chat format, merges `req.Parameters`
- Error classification:
  - 429 → `ErrRateLimited` (with `Retry-After` header parsing)
  - 5xx → `ErrTransient`
  - Body contains `context_length_exceeded` → `ErrContextOverflow`
  - Other → `ErrFatal`

### Anthropic (`internal/providers/anthropic/`)

- Endpoint: `POST {baseURL}/v1/messages`
- Health: `GET {baseURL}/v1/messages` (405 = healthy)
- Auth: `x-api-key: {apiKey}`, `anthropic-version: 2023-06-01`
- Request translation: Splits system message from user messages (Anthropic API requires separate `system` field), defaults `max_tokens` to 4096 if not in `req.Parameters`
- Error classification: Same pattern as OpenAI

### vLLM (`internal/providers/vllm/`)

- Endpoint: `POST {endpoint}/v1/chat/completions` (OpenAI-compatible)
- Health: `GET {endpoint}/health`
- Auth: None (local deployment)
- Features: Multiple endpoints with round-robin load balancing
- Request translation: Same as OpenAI (vLLM implements OpenAI-compatible API)

## Common Patterns

### Parameter Forwarding

All adapters merge `req.Parameters` into the provider payload:

```go
for k, v := range req.Parameters {
    if k != "model" && k != "messages" {
        payload[k] = v
    }
}
```

Reserved keys (`model`, `messages`, `stream`) are never overridden by parameters.

### Request ID Propagation

All adapters forward the request ID for distributed tracing:

```go
if reqID := providers.GetRequestID(ctx); reqID != "" {
    req.Header.Set("X-Request-ID", reqID)
}
```

The request ID is injected into the context by the HTTP handler using `providers.WithRequestID()`.

### Error Wrapping

Adapters wrap HTTP errors in `providers.StatusError`:

```go
type StatusError struct {
    StatusCode    int
    Body          string
    RetryAfterSecs float64
}
```

The `ClassifyError()` method on each adapter converts these to `router.ClassifiedError` for the routing engine's failover logic.

## Creating a New Adapter

To add support for a new provider:

1. Create `internal/providers/{name}/adapter.go`
2. Implement `router.Sender` (and optionally `router.StreamSender` and `health.Probeable`)
3. Add an `Option` pattern for configuration (timeout, endpoints, etc.)
4. Add a case for the new type in `registerProviderAdapter()` in `internal/httpapi/handlers_admin.go`
5. Register providers and models at runtime via the admin API or `tokenhubctl`

Example skeleton:

```go
package newprovider

import (
    "context"
    "github.com/jordanhubbard/tokenhub/internal/router"
)

type Adapter struct {
    id     string
    apiKey string
    // ...
}

func New(id, apiKey string) *Adapter {
    return &Adapter{id: id, apiKey: apiKey}
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) {
    // Translate req to provider format, make HTTP call, return raw JSON
}

func (a *Adapter) ClassifyError(err error) *router.ClassifiedError {
    // Classify the error for failover logic
}

func (a *Adapter) HealthEndpoint() string {
    return "https://api.newprovider.com/health"
}
```
