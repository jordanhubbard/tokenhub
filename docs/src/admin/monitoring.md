# Monitoring & Observability

TokenHub provides multiple layers of observability: health tracking, Prometheus metrics, time-series data, request logs, audit logs, reward logs, and real-time SSE events.

## Health Endpoint

```bash
curl http://localhost:8080/healthz
```

| Status | Meaning |
|--------|---------|
| 200 | System is healthy, adapters and models are registered |
| 503 | No adapters or no models are registered |

Response:
```json
{"status": "ok", "adapters": 2, "models": 6}
```

## Provider Health

View per-provider health status:

```bash
curl http://localhost:8080/admin/v1/health
```

Response:
```json
{
  "providers": [
    {
      "provider_id": "openai",
      "state": "healthy",
      "total_requests": 1234,
      "total_errors": 5,
      "consec_errors": 0,
      "avg_latency_ms": 456.7,
      "last_error": "",
      "last_success_at": "2026-02-16T12:34:56Z",
      "cooldown_until": "0001-01-01T00:00:00Z"
    }
  ]
}
```

### Health States

| State | Consecutive Errors | Behavior |
|-------|-------------------|----------|
| **Healthy** | 0-1 | Normal routing |
| **Degraded** | 2-4 | Still routed but penalized in scoring |
| **Down** | 5+ | Excluded from routing; 30-second cooldown |

### Active Health Probing

TokenHub actively probes provider health endpoints in the background:

| Provider | Health Endpoint | Success Criteria |
|----------|----------------|-----------------|
| OpenAI | `GET /v1/models` | 2xx response |
| Anthropic | `GET /v1/messages` | 2xx or 405 response |
| vLLM | `GET /health` | 2xx response |

Probes run every 30 seconds with a 10-second timeout.

## Prometheus Metrics

Expose metrics at:

```bash
curl http://localhost:8080/metrics
```

### Available Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `tokenhub_requests_total` | counter | mode, model, provider, status | Total requests processed |
| `tokenhub_request_latency_ms` | histogram | mode, model, provider | Request latency distribution |
| `tokenhub_cost_usd_total` | counter | model, provider | Cumulative estimated cost |

### Prometheus Configuration

```yaml
# prometheus.yml
scrape_configs:
  - job_name: tokenhub
    scrape_interval: 15s
    static_configs:
      - targets: ['tokenhub:8080']
```

### Example Queries

```promql
# Request rate by model
rate(tokenhub_requests_total[5m])

# P95 latency
histogram_quantile(0.95, rate(tokenhub_request_latency_ms_bucket[5m]))

# Cost per hour by provider
rate(tokenhub_cost_usd_total[1h]) * 3600

# Error rate
sum(rate(tokenhub_requests_total{status="error"}[5m])) /
sum(rate(tokenhub_requests_total[5m]))
```

## Time-Series Database (TSDB)

TokenHub includes a lightweight SQLite-backed TSDB for historical metrics with querying and downsampling.

### Query Metrics

```bash
curl "http://localhost:8080/admin/v1/tsdb/query?metric=latency&model_id=gpt-4&start=2026-02-16T00:00:00Z&end=2026-02-16T23:59:59Z&step_ms=60000"
```

| Parameter | Required | Description |
|-----------|----------|-------------|
| `metric` | Yes | Metric name (`latency` or `cost`) |
| `model_id` | No | Filter by model |
| `provider_id` | No | Filter by provider |
| `start` | No | Start time (RFC3339) |
| `end` | No | End time (RFC3339) |
| `step_ms` | No | Downsample bucket in milliseconds |

### List Available Metrics

```bash
curl http://localhost:8080/admin/v1/tsdb/metrics
```

### Configure Retention

```bash
curl -X PUT http://localhost:8080/admin/v1/tsdb/retention \
  -H "Content-Type: application/json" \
  -d '{"retention_days": 14}'
```

Default retention is 7 days. Old data is automatically pruned hourly.

### Manual Prune

```bash
curl -X POST http://localhost:8080/admin/v1/tsdb/prune
```

## Request Logs

View paginated request history:

```bash
curl "http://localhost:8080/admin/v1/logs?limit=50&offset=0"
```

Each entry contains:
- Timestamp, request ID
- Model ID, provider ID, routing mode
- Estimated cost, latency
- HTTP status code, error class (if failed)

## Audit Logs

View admin action history:

```bash
curl "http://localhost:8080/admin/v1/audit?limit=50&offset=0"
```

Logged actions:
- `vault.lock`, `vault.unlock`, `vault.rotate`
- `provider.upsert`, `provider.delete`
- `model.upsert`, `model.patch`, `model.delete`
- `apikey.create`, `apikey.rotate`, `apikey.update`, `apikey.revoke`
- `routing-config.update`

## Reward Logs

View contextual bandit reward data for RL-based routing analysis:

```bash
curl "http://localhost:8080/admin/v1/rewards?limit=50&offset=0"
```

Each entry contains: request ID, mode, model, provider, token count, token bucket (small/medium/large), latency budget, actual latency, cost, success flag, error class, and computed reward.

## Aggregated Statistics

```bash
curl http://localhost:8080/admin/v1/stats
```

Returns global aggregates plus breakdowns by model and by provider.

## Server-Sent Events (SSE)

Subscribe to real-time events:

```bash
curl -N http://localhost:8080/admin/v1/events
```

Event types:

| Event | Fields | When |
|-------|--------|------|
| `route_success` | model_id, provider_id, latency_ms, cost_usd, reason | Request completed successfully |
| `route_error` | latency_ms, error_class, error_msg | Request failed |

Example:
```
data: {"type":"route_success","model_id":"gpt-4","provider_id":"openai","latency_ms":456.7,"cost_usd":0.023,"reason":"routed-weight-8"}
```

## Recommended Alerting Rules

| Alert | Condition | Severity |
|-------|-----------|----------|
| High error rate | Error rate > 5% over 5 minutes | Warning |
| Provider down | Provider in "down" state > 2 minutes | Critical |
| High latency | P95 latency > 10 seconds | Warning |
| Cost spike | Hourly cost > 2x 7-day average | Warning |
| Vault locked | Vault locked during business hours | Critical |
| No providers | Adapter count = 0 | Critical |
