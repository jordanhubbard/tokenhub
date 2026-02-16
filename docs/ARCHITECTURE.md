# Tokenhub Architecture

## Topology

Tokenhub = control plane + data plane:

- **Data plane**: request handling, routing, provider calls
- **Control plane**: provider/model config, credentials, policies, metrics

## Package structure (recommended)

- `cmd/tokenhub/` main entrypoint
- `internal/app/` config + server wiring
- `internal/httpapi/` REST endpoints and request/response envelopes
- `internal/router/` routing engine + orchestration entrypoints
- `internal/providers/` adapters for each provider (OpenAI, Anthropic, vLLM)
- `internal/store/` persistence (SQLite/Postgres)
- `internal/vault/` encrypted key escrow
- `internal/policy/` routing policies, scoring, constraints, token estimation
- `internal/rl/` optional bandit/RL router (phase 2)
- `internal/metrics/` Prometheus registry and helpers

## Key interfaces

### Provider Adapter

Provider adapters translate the `router.Request` envelope into provider-specific requests.

Minimal interface:

- `Send(ctx, model, request) -> raw response`
- `Capabilities() -> max_context, streaming support, etc.`
- `ClassifyError(err/resp) -> {retryable, rate_limited, context_overflow, provider_down}`

### Store

The store persists:
- providers
- models
- encrypted credentials blob (for escrow)
- policy defaults
- optional request logs (redacted)

### Vault

Vault is responsible for:
- deriving master key from admin password (argon2id recommended)
- encrypting/decrypting credentials for storage
- locking/unlocking
- clearing key material on lock/shutdown

## Runtime state

Tokenhub should keep a small in-memory state:
- provider health and last error
- rolling latency stats per provider/model
- rate limit state (observed 429s, Retry-After)
- optional moving average “quality” score (phase 2)

This state can be ephemeral and rebuilt.

## Deployment

Stateless replicas behind load balancer:
- shared DB for config (or single-writer)
- shared secret manager for env keys
- optional Redis for orchestration session state (if you add long-running workflows)

## Observability

Expose:
- `/metrics` Prometheus
- structured JSON logs (with redaction)
- request IDs and decision metadata in responses

