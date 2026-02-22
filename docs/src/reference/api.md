# API Reference

Complete reference for all TokenHub HTTP endpoints.

## Consumer Endpoints

### POST /v1/chat

Send a chat completion request with automatic model routing.

**Authentication**: Required (Bearer token)

**Request Body**:
```json
{
  "request": {
    "messages": [{"role": "string", "content": "string"}],
    "model_hint": "string",
    "estimated_input_tokens": 0,
    "parameters": {},
    "stream": false,
    "meta": {},
    "output_schema": {}
  },
  "capabilities": {"planning": false},
  "policy": {
    "mode": "normal",
    "max_budget_usd": 0.05,
    "max_latency_ms": 20000,
    "min_weight": 0
  },
  "output_format": {
    "type": "json",
    "schema": "string",
    "max_tokens": 0,
    "strip_think": false
  }
}
```

**Response**: `200 OK`
```json
{
  "negotiated_model": "string",
  "estimated_cost_usd": 0.0,
  "routing_reason": "string",
  "response": {}
}
```

**Errors**: 400, 401, 403, 502

---

### POST /v1/plan

Send an orchestrated multi-model request.

**Authentication**: Required (Bearer token)

**Request Body**:
```json
{
  "request": {
    "messages": [{"role": "string", "content": "string"}]
  },
  "orchestration": {
    "mode": "adversarial",
    "iterations": 2,
    "primary_model_id": "string",
    "review_model_id": "string",
    "primary_min_weight": 0,
    "review_min_weight": 0,
    "return_plan_only": false,
    "output_schema": "string"
  }
}
```

**Response**: `200 OK`
```json
{
  "negotiated_model": "string",
  "estimated_cost_usd": 0.0,
  "routing_reason": "string",
  "response": {}
}
```

**Errors**: 400, 401, 403, 502

---

## Health

### GET /healthz

System health check.

**Response**: `200 OK` or `503 Service Unavailable`
```json
{
  "status": "ok",
  "adapters": 2,
  "models": 6
}
```

---

### GET /metrics

Prometheus metrics endpoint.

**Response**: `200 OK` (text/plain, Prometheus exposition format)

---

## Admin - Vault

### POST /admin/v1/vault/unlock

**Body**: `{"admin_password": "string"}`

**Response**: `200 OK` → `{"ok": true}`

---

### POST /admin/v1/vault/lock

**Response**: `200 OK` → `{"ok": true, "already_locked": false}`

---

### POST /admin/v1/vault/rotate

**Body**: `{"old_password": "string", "new_password": "string"}`

**Response**: `200 OK` → `{"ok": true}`

---

## Admin - Providers

### POST /admin/v1/providers

Create or update a provider.

**Body**: `{"id": "string", "type": "openai|anthropic|vllm", "enabled": true, "base_url": "string", "cred_store": "vault|none", "api_key": "string"}`

**Response**: `200 OK` → `{"ok": true, "cred_store": "vault"}`

---

### GET /admin/v1/providers

List all providers (from the persistent store).

**Query**: `?limit=N&offset=N`

**Response**: `200 OK` → `{"items": [{provider objects}], "total": N, "limit": N, "offset": N}`

---

### PATCH /admin/v1/providers/{id}

Partial update of a provider. Runtime-only providers (not in the store) are automatically created in the store when first patched.

**Body**: `{"type": "string", "base_url": "string", "enabled": true, "api_key": "string", "cred_store": "string"}`

**Response**: `200 OK` → `{"ok": true, "provider": {updated provider}}`

---

### DELETE /admin/v1/providers/{id}

Delete a provider.

**Response**: `200 OK` → `{"ok": true}`

---

### GET /admin/v1/providers/{id}/discover

Discover models available from a provider by querying its `/v1/models` endpoint.

**Response**: `200 OK` → `{"models": [{"id": "string", "registered": false}]}`

---

## Admin - Models

### POST /admin/v1/models

Create or update a model. Registers the model in both the runtime engine and persistent store.

**Body**: `{"id": "string", "provider_id": "string", "weight": 5, "max_context_tokens": 128000, "input_per_1k": 0.01, "output_per_1k": 0.03, "enabled": true}`

**Response**: `200 OK` → `{"ok": true}`

---

### GET /admin/v1/models

List all models (from the persistent store).

**Query**: `?limit=N&offset=N`

**Response**: `200 OK` → `{"items": [{model objects}], "total": N, "limit": N, "offset": N}`

---

### PATCH /admin/v1/models/{id}

Partial model update. Model IDs can contain slashes (e.g., `Qwen/Qwen2.5-Coder-32B-Instruct`). Runtime-only models are automatically seeded into the store from engine data on first patch.

**Body**: `{"weight": 7, "enabled": true, "input_per_1k": 0.015, "output_per_1k": 0.035, "max_context_tokens": 128000}`

**Response**: `200 OK` → `{"ok": true, "model": {updated model}}`

---

### DELETE /admin/v1/models/{id}

Delete a model. Model IDs with slashes are supported.

**Response**: `200 OK` → `{"ok": true}`

---

## Admin - Routing

### GET /admin/v1/routing-config

Get current routing defaults.

**Response**: `200 OK` → `{"default_mode": "string", "default_max_budget_usd": 0.05, "default_max_latency_ms": 20000}`

---

### PUT /admin/v1/routing-config

Set routing defaults.

**Body**: `{"default_mode": "string", "default_max_budget_usd": 0.1, "default_max_latency_ms": 30000}`

**Response**: `200 OK` → `{"ok": true}`

---

### POST /admin/v1/routing/simulate

Run a what-if routing simulation without sending a real request.

**Body**: `{"mode": "string", "token_count": 500, "max_budget_usd": 0.05, "min_weight": 0, "model_hint": "string"}`

**Response**: `200 OK` → `{"decision": {decision object}, "eligible": [{model objects}]}`

---

## Admin - API Keys

### POST /admin/v1/apikeys

Create a new API key.

**Body**: `{"name": "string", "scopes": "[\"chat\",\"plan\"]", "rotation_days": 0, "expires_in": "720h", "monthly_budget_usd": 50.0}`

**Response**: `200 OK` → `{"ok": true, "key": "tokenhub_...", "id": "string", "prefix": "string", "warning": "string"}`

---

### GET /admin/v1/apikeys

List all API keys (no plaintext).

**Response**: `200 OK` → `[{key objects without plaintext}]`

---

### POST /admin/v1/apikeys/{id}/rotate

Rotate an API key.

**Response**: `200 OK` → `{"ok": true, "key": "tokenhub_...", "warning": "string"}`

---

### PATCH /admin/v1/apikeys/{id}

Update API key metadata.

**Body**: `{"name": "string", "scopes": "string", "rotation_days": 0, "enabled": true}`

**Response**: `200 OK` → `{"ok": true}`

---

### DELETE /admin/v1/apikeys/{id}

Revoke (delete) an API key.

**Response**: `200 OK` → `{"ok": true}`

---

## Admin - Observability

### GET /admin/v1/health

Provider health status.

**Response**: `200 OK` → `{"providers": [{health state objects}]}`

---

### GET /admin/v1/stats

Aggregated request statistics.

**Response**: `200 OK` → `{"global": {}, "by_model": {}, "by_provider": {}}`

---

### GET /admin/v1/logs?limit=100&offset=0

Paginated request logs.

---

### GET /admin/v1/audit?limit=100&offset=0

Paginated audit logs.

---

### GET /admin/v1/rewards?limit=100&offset=0

Paginated reward entries.

---

### GET /admin/v1/engine/models

Runtime model registry, adapter list, and adapter metadata.

**Response**: `200 OK`
```json
{
  "models": [{model objects}],
  "total": 7,
  "adapters": ["openai", "anthropic", "vllm"],
  "adapter_info": [
    {"id": "openai", "health_endpoint": "https://api.openai.com/v1/models"},
    {"id": "vllm", "health_endpoint": "http://vllm-1:8000/health"}
  ]
}
```

---

## Admin - TSDB

### GET /admin/v1/tsdb/query?metric=latency&model_id=gpt-4&start=...&end=...&step_ms=60000

Query time-series data.

---

### GET /admin/v1/tsdb/metrics

List available TSDB metrics.

---

### POST /admin/v1/tsdb/prune

Manually prune old TSDB data.

---

### PUT /admin/v1/tsdb/retention

Set TSDB retention period.

**Body**: `{"retention_days": 7}`

---

## Admin - Workflows (Temporal)

### GET /admin/v1/workflows?limit=50&status=RUNNING

List Temporal workflow executions.

---

### GET /admin/v1/workflows/{id}

Describe a workflow execution.

---

### GET /admin/v1/workflows/{id}/history

Get workflow event history.

---

## Admin - Events

### GET /admin/v1/events

Server-Sent Events stream.

**Content-Type**: `text/event-stream`

**Events**: `route_success`, `route_error`

---

## Admin UI

### GET /admin

Serves the embedded admin SPA. The root URL (`/`) redirects here.

### GET /admin/v1/info

Admin status information. Requires admin token authentication (Bearer header or `?token=` query parameter).

**Response**: `200 OK`
```json
{
  "tokenhub": "admin",
  "vault_locked": true,
  "vault_initialized": false
}
```

The `vault_initialized` field indicates whether the vault has ever been set up (salt exists). The UI uses this to distinguish first-time setup from a normal unlock prompt.
