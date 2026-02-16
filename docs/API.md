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
Upsert providers (TODO)

### POST /admin/v1/models
Upsert/enable models (partially stubbed; registers in-memory for now)

## Observability

### GET /metrics
Prometheus

### GET /healthz
OK

