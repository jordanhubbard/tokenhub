# Routing Configuration

TokenHub's routing engine uses a multi-objective scoring function to select the best model for each request. Administrators can configure the default routing behavior that applies when clients don't specify a policy.

## Default Routing Settings

### View Current Defaults

```bash
curl http://localhost:8080/admin/v1/routing-config
```

Response:
```json
{
  "default_mode": "normal",
  "default_max_budget_usd": 0.05,
  "default_max_latency_ms": 20000
}
```

### Update Defaults

```bash
curl -X PUT http://localhost:8080/admin/v1/routing-config \
  -H "Content-Type: application/json" \
  -d '{
    "default_mode": "normal",
    "default_max_budget_usd": 0.10,
    "default_max_latency_ms": 30000
  }'
```

| Field | Type | Range | Description |
|-------|------|-------|-------------|
| `default_mode` | string | See below | Default routing mode |
| `default_max_budget_usd` | float | 0-100 | Default cost ceiling per request |
| `default_max_latency_ms` | int | 0-300000 | Default latency ceiling |

Changes take effect immediately for new requests and are persisted to the database.

## Routing Modes

Each mode applies different weights to the four scoring objectives:

| Mode | Cost | Latency | Failure Rate | Capability | Use Case |
|------|------|---------|-------------|------------|----------|
| `cheap` | 0.7 | 0.1 | 0.1 | 0.1 | Minimize costs for simple tasks |
| `normal` | 0.25 | 0.25 | 0.25 | 0.25 | Balanced operation |
| `high_confidence` | 0.05 | 0.1 | 0.15 | 0.7 | Complex tasks needing strong models |
| `planning` | 0.1 | 0.1 | 0.2 | 0.6 | Multi-step reasoning tasks |
| `adversarial` | 0.1 | 0.1 | 0.2 | 0.6 | Adversarial orchestration |
| `thompson` | — | — | — | — | Adaptive RL-based selection |

### How Scoring Works

For modes other than `thompson`, the scoring formula is:

```
score = (cost_norm × w_cost) + (latency_norm × w_latency) + (failure_norm × w_failure) - (weight × w_capability)
```

Where:
- `cost_norm`: Estimated cost normalized to 0-1 range
- `latency_norm`: Average latency normalized to 0-1 range
- `failure_norm`: Error rate from health tracker
- `weight`: Model capability weight (0-10)
- `w_*`: Mode-specific weights from the table above

**Lower score = better model**. Models are sorted by score and tried in order.

### Thompson Sampling

The `thompson` mode uses a contextual bandit approach:

1. Each (model, token_bucket) pair maintains Beta distribution parameters (alpha, beta)
2. For each request, a reward value is sampled from each model's Beta distribution
3. Models are sorted by sampled reward (highest first)
4. Parameters are updated periodically from historical reward data

This approach automatically adapts to changing model performance over time.

## Model Eligibility Filtering

Before scoring, the router filters models:

1. **Enabled**: Model must be enabled
2. **Minimum weight**: Must meet the request's `min_weight` threshold
3. **Context capacity**: Must have enough context window (with 15% headroom)
4. **Provider health**: Provider must not be in the "down" state
5. **Budget**: Estimated cost must be within `max_budget_usd`

If no models pass filtering, the request fails with a 502 error.

## Escalation and Failover

When a provider call fails, the router uses the error classification to decide what to do:

| Error Class | Action |
|-------------|--------|
| `context_overflow` | Find a model with a larger context window |
| `rate_limited` | Skip to the next provider; honor `Retry-After` header |
| `transient` (5xx) | Retry with exponential backoff (100ms base, 2 retries) |
| `fatal` (4xx) | Try the next model in scored order |

The router tries up to 5 models before giving up.

## Runtime Model Registry

View the current in-memory model registry and registered adapters:

```bash
curl http://localhost:8080/admin/v1/engine/models
```

Response:
```json
{
  "models": [
    {
      "id": "gpt-4",
      "provider_id": "openai",
      "weight": 8,
      "max_context_tokens": 128000,
      "input_per_1k": 0.01,
      "output_per_1k": 0.03,
      "enabled": true
    }
  ],
  "adapters": ["openai", "anthropic", "vllm"]
}
```

## Audit Trail

Routing configuration changes are logged as `routing-config.update` in the audit trail.
