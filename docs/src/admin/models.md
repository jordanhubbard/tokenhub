# Model Management

Models are the LLM model definitions that TokenHub uses for routing decisions. Each model is associated with a provider and has properties that affect routing: capability weight, context window size, and pricing.

## Default Models

TokenHub registers these default models at startup:

| Model ID | Provider | Weight | Context | Input $/1K | Output $/1K |
|----------|----------|--------|---------|------------|-------------|
| `gpt-4` | openai | 8 | 128,000 | $0.010 | $0.030 |
| `gpt-3.5-turbo` | openai | 3 | 16,385 | $0.0005 | $0.0015 |
| `claude-opus` | anthropic | 10 | 200,000 | $0.015 | $0.075 |
| `claude-sonnet` | anthropic | 7 | 200,000 | $0.003 | $0.015 |

Defaults are overridden if persisted models exist in the database.

## API Operations

### Create or Update a Model

```bash
curl -X POST http://localhost:8080/admin/v1/models \
  -H "Content-Type: application/json" \
  -d '{
    "id": "gpt-4-turbo",
    "provider_id": "openai",
    "weight": 7,
    "max_context_tokens": 128000,
    "input_per_1k": 0.01,
    "output_per_1k": 0.03,
    "enabled": true
  }'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Model identifier (must match provider's model name) |
| `provider_id` | string | Yes | ID of the registered provider |
| `weight` | int | Yes | Capability weight (0-10); higher = more capable |
| `max_context_tokens` | int | Yes | Maximum context window in tokens |
| `input_per_1k` | float | Yes | Cost per 1,000 input tokens in USD |
| `output_per_1k` | float | Yes | Cost per 1,000 output tokens in USD |
| `enabled` | bool | Yes | Whether the model is available for routing |

### List Models

```bash
curl http://localhost:8080/admin/v1/models
```

### Patch a Model

Update individual fields without resending the full configuration:

```bash
curl -X PATCH http://localhost:8080/admin/v1/models/gpt-4 \
  -H "Content-Type: application/json" \
  -d '{
    "weight": 9,
    "enabled": true,
    "input_per_1k": 0.012
  }'
```

Patchable fields: `weight`, `enabled`, `input_per_1k`, `output_per_1k`.

### Delete a Model

```bash
curl -X DELETE http://localhost:8080/admin/v1/models/gpt-4-legacy
```

## Weight Guidelines

The model weight is the primary indicator of model capability used in routing decisions:

| Weight | Intended For |
|--------|-------------|
| 1-3 | Simple tasks, low cost (e.g., GPT-3.5 Turbo) |
| 4-6 | General purpose (e.g., GPT-4 Turbo, Claude Sonnet) |
| 7-8 | Complex reasoning (e.g., GPT-4, Claude Opus) |
| 9-10 | Highest capability (e.g., next-gen frontier models) |

Different routing modes weight the capability score differently:
- **`cheap`** mode barely considers weight (0.1 factor)
- **`high_confidence`** and **`planning`** modes heavily favor higher weights (0.6-0.7 factor)
- **`normal`** mode balances weight equally with cost, latency, and reliability (0.25 each)

## Context Window

The `max_context_tokens` field tells the router whether a model can handle a given request size. The router applies a 15% headroom buffer — a model with 128,000 tokens can handle requests estimated up to ~108,000 tokens.

Token estimation uses `estimated_input_tokens` from the request if provided, otherwise falls back to a `characters / 4` heuristic.

## Pricing

Model pricing is used for:
1. **Cost estimation**: Returned in the response as `estimated_cost_usd`
2. **Budget filtering**: Models exceeding the request's `max_budget_usd` are excluded
3. **Cost scoring**: In routing modes that consider cost (especially `cheap` mode)

Keep pricing up to date as providers change their rates.

## Audit Trail

Model mutations are logged:
- `model.upsert` — Model created or updated
- `model.patch` — Model partially updated
- `model.delete` — Model removed
