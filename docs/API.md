# API (Draft)

## Consumer API

### POST /v1/chat

Request:
```json
{
  "capabilities": {"planning": true},
  "policy": {
    "mode": "normal",
    "max_budget_usd": 0.05,
    "max_latency_ms": 20000,
    "min_weight": 6
  },
  "request": {
    "id": "req-123",
    "messages": [{"role":"user","content":"Hello"}],
    "estimated_input_tokens": 123,
    "meta": {"client":"cli"}
  }
}
```

Response:
```json
{
  "negotiated_model": "claude-opus",
  "estimated_cost_usd": 0.023,
  "routing_reason": "context>64k;min_weight=6",
  "response": { "provider_specific": "..." }
}
```

### POST /v1/plan

Request:
```json
{
  "request": { "messages": [ ... ] },
  "orchestration": {
    "mode": "adversarial",
    "primary_min_weight": 6,
    "review_min_weight": 9,
    "iterations": 2
  }
}
```

Response: provider-specific raw response plus decision metadata.

## Admin API

### POST /admin/v1/vault/unlock
```json
{ "admin_password": "..." }
```

### POST /admin/v1/providers
Upsert provider configuration (persisted to SQLite).

### GET /admin/v1/providers
List all provider configurations.

### DELETE /admin/v1/providers/{id}
Delete a provider configuration.

### POST /admin/v1/models
Upsert model configuration (persisted to SQLite, registered in routing engine).

### GET /admin/v1/models
List all model configurations.

### PATCH /admin/v1/models/{id}
Partially update a model (e.g. toggle enabled, change weight).

### DELETE /admin/v1/models/{id}
Delete a model configuration.

### GET /admin/v1/routing-config
Get current routing policy defaults.

### PUT /admin/v1/routing-config
Update routing policy defaults (mode, budget, latency).

### GET /admin/v1/health
Provider health statistics.

### GET /admin/v1/stats
Request statistics and counters.

### GET /admin/v1/logs
Paginated request logs (query params: limit, offset).

### GET /admin/v1/audit
Paginated audit logs (query params: limit, offset).

### GET /admin/v1/rewards
Paginated contextual bandit reward logs (query params: limit, offset).

### GET /admin/v1/engine/models
Live engine model registry snapshot.

### GET /admin/v1/events
Server-Sent Events stream for real-time updates.

### TSDB endpoints
- GET /admin/v1/tsdb/query — Query time-series data
- GET /admin/v1/tsdb/metrics — List available metrics
- POST /admin/v1/tsdb/prune — Manually prune old data
- PUT /admin/v1/tsdb/retention — Set retention policy

## Observability

### GET /metrics
Prometheus

### GET /healthz
OK

