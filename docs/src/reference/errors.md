# Error Classification

TokenHub classifies provider errors to enable intelligent failover. Each error from a provider is classified into one of four categories that determine the routing engine's next action.

## Error Classes

### context_overflow

The request exceeds the model's context window.

**Triggers**:
- HTTP 413 from provider
- Response body contains `context_length_exceeded`

**Router action**: Escalate to a model with a larger context window. If no larger model is available, try the next model in scored order.

---

### rate_limited

The provider is throttling requests.

**Triggers**:
- HTTP 429 from provider

**Router action**: Skip to a different provider. If the response includes a `Retry-After` header, the delay is recorded in the classified error for optional use by the caller.

---

### transient

A temporary server-side failure.

**Triggers**:
- HTTP 5xx from provider

**Router action**: Retry the same model with exponential backoff:
- Base delay: 100ms
- Maximum retries: 2
- Backoff multiplier: 2x (100ms, 200ms)

After retries are exhausted, try the next model.

---

### fatal

An unrecoverable client error.

**Triggers**:
- HTTP 4xx (except 429 and 413)
- Any other unclassified error

**Router action**: Skip to the next model in scored order. No retry.

## Error Flow

```
Provider returns error
  │
  ├── adapter.ClassifyError(err) → ClassifiedError{Class, RetryAfter}
  │
  └── Router handles based on class:
        ├── context_overflow → Find bigger model
        ├── rate_limited → Different provider (respect RetryAfter)
        ├── transient → Retry with backoff (up to 2x)
        └── fatal → Next model
```

## ClassifiedError Type

```go
type ClassifiedError struct {
    Err        error
    Class      ErrorClass  // "context_overflow", "rate_limited", "transient", "fatal"
    RetryAfter float64     // Seconds to wait (from Retry-After header, 429 only)
}
```

## HTTP Error Responses

### Consumer API Errors

| Status | Meaning | When |
|--------|---------|------|
| 400 | Bad Request | Invalid JSON, missing messages, validation failure |
| 401 | Unauthorized | Missing or invalid API key |
| 403 | Forbidden | Valid key but insufficient scopes |
| 502 | Bad Gateway | All models failed, no eligible models, or provider errors |

### Admin API Errors

| Status | Meaning | When |
|--------|---------|------|
| 400 | Bad Request | Invalid parameters or validation failure |
| 404 | Not Found | Resource not found (model, key, provider) |
| 500 | Internal Server Error | Database or vault errors |

## Provider-Specific Classification

### OpenAI

| HTTP Status | Body Pattern | Error Class |
|-------------|-------------|-------------|
| 429 | — | rate_limited |
| 500-599 | — | transient |
| 400 | `context_length_exceeded` | context_overflow |
| Other 4xx | — | fatal |

### Anthropic

| HTTP Status | Body Pattern | Error Class |
|-------------|-------------|-------------|
| 429 | — | rate_limited |
| 500-599 | — | transient |
| 400 | `context_length_exceeded` | context_overflow |
| Other 4xx | — | fatal |

### vLLM

| HTTP Status | Body Pattern | Error Class |
|-------------|-------------|-------------|
| 429 | — | rate_limited |
| 500-599 | — | transient |
| 400 | `context_length_exceeded` | context_overflow |
| Other 4xx | — | fatal |

## Reward Impact

Error classification affects the contextual bandit reward system:

- **Successful requests**: Reward computed from latency and cost
- **Failed requests**: Reward = 0.0 (regardless of error class)
- **Error class** is stored in reward entries for analysis

This ensures the Thompson Sampling policy learns to avoid unreliable models over time.
