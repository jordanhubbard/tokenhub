# Provider Adapters

Tokenhub should implement provider adapters behind a common interface.

## OpenAI adapter
- Chat completions / responses mapping
- Support rate limit handling (429 + Retry-After)
- Detect context overflow errors and classify

## Anthropic adapter
- Messages API mapping
- Detect prompt too long / context errors
- Handle 429 + 529-ish transient errors

## vLLM adapter
- OpenAI-compatible endpoints (common) OR custom endpoints
- Support multiple endpoints per provider (load balancing)
- Health checks

## Error classification contract

Adapters should classify errors into:
- `context_overflow`
- `rate_limited` (with optional retry_after)
- `transient` (retryable)
- `fatal` (do not retry)

Tokenhub uses this to decide failover.

