# Plan API

The plan endpoint provides multi-model orchestrated reasoning. It coordinates multiple LLM calls using different strategies to produce higher-quality outputs than a single model call.

**Endpoint**: `POST /v1/plan`

## Request Format

```json
{
  "request": {
    "messages": [
      {"role": "user", "content": "Design a REST API for a task management app"}
    ]
  },
  "orchestration": {
    "mode": "adversarial",
    "iterations": 2,
    "primary_model_id": "claude-opus",
    "review_model_id": "gpt-4",
    "primary_min_weight": 5,
    "review_min_weight": 8,
    "return_plan_only": false,
    "output_schema": "{\"type\":\"object\"}"
  }
}
```

## Orchestration Modes

### Adversarial

A three-phase plan-critique-refine loop:

1. **Plan**: Primary model generates an initial plan
2. **Critique**: Review model analyzes the plan and provides feedback
3. **Refine**: Primary model improves the plan based on the critique

The critique-refine cycle repeats for the configured number of iterations.

```json
{
  "orchestration": {
    "mode": "adversarial",
    "iterations": 2
  }
}
```

**Response**:

```json
{
  "negotiated_model": "claude-opus",
  "estimated_cost_usd": 0.15,
  "routing_reason": "adversarial-orchestration",
  "response": {
    "initial_plan": "Here is the initial API design...",
    "critique": "The design has these issues: ...",
    "refined_plan": "Here is the improved design addressing the feedback..."
  }
}
```

### Vote

Multiple models respond independently, then a judge model selects the best:

1. N models (voters) each generate a response to the same prompt
2. A judge model reviews all responses and selects the best one

```json
{
  "orchestration": {
    "mode": "vote"
  }
}
```

**Response**:

```json
{
  "negotiated_model": "gpt-4",
  "estimated_cost_usd": 0.08,
  "routing_reason": "vote-orchestration",
  "response": {
    "responses": [
      {"model": "gpt-4", "content": "Response A...", "selected": true},
      {"model": "claude-sonnet", "content": "Response B...", "selected": false},
      {"model": "gpt-3.5-turbo", "content": "Response C...", "selected": false}
    ],
    "selected": 0,
    "judge": "claude-opus"
  }
}
```

### Refine

A single model iteratively improves its own response:

1. Model generates an initial response
2. Model reviews and refines its own response (repeats for N iterations)

```json
{
  "orchestration": {
    "mode": "refine",
    "iterations": 3
  }
}
```

**Response**:

```json
{
  "negotiated_model": "claude-opus",
  "estimated_cost_usd": 0.12,
  "routing_reason": "refine-orchestration",
  "response": {
    "refined_response": "Final refined response...",
    "iterations": 3,
    "model": "claude-opus"
  }
}
```

### Planning

Simple single-route with the planning weight profile (prioritizes capable models):

```json
{
  "orchestration": {
    "mode": "planning"
  }
}
```

## Orchestration Fields

| Field | Type | Default | Range | Description |
|-------|------|---------|-------|-------------|
| `mode` | string | `planning` | See above | Orchestration strategy |
| `iterations` | int | 1-2 | 0-10 | Number of refinement iterations |
| `primary_model_id` | string | — | — | Explicit model for primary phase |
| `review_model_id` | string | — | — | Explicit model for review/judge phase |
| `primary_min_weight` | int | 0 | 0-10 | Minimum weight for primary model |
| `review_min_weight` | int | 0 | 0-10 | Minimum weight for review model |
| `return_plan_only` | bool | false | — | Return plan without executing refinement |
| `output_schema` | string | — | — | JSON Schema for structured output validation |

## Explicit Model Selection

By default, TokenHub selects models using its routing engine. You can override this with explicit model IDs:

```json
{
  "orchestration": {
    "mode": "adversarial",
    "primary_model_id": "claude-opus",
    "review_model_id": "gpt-4"
  }
}
```

Alternatively, use `primary_min_weight` and `review_min_weight` to set capability floors without specifying exact models:

```json
{
  "orchestration": {
    "mode": "adversarial",
    "primary_min_weight": 7,
    "review_min_weight": 9
  }
}
```

## Error Responses

| Status | Body | Cause |
|--------|------|-------|
| 400 | `"messages required"` | Empty messages array |
| 400 | `"iterations must be between 0 and 10"` | Invalid iteration count |
| 400 | `"unknown orchestration mode"` | Unrecognized mode value |
| 401 | `"missing or invalid api key"` | Authentication failure |
| 403 | `"scope not allowed"` | API key lacks `plan` scope |
| 502 | Error message | Orchestration failed (all models failed) |

## Cost Considerations

Orchestration modes make multiple LLM calls. Approximate cost multipliers:

| Mode | Calls per Request | Typical Cost Multiplier |
|------|-------------------|------------------------|
| Planning | 1 | 1x |
| Adversarial (2 iter) | 5 (plan + 2x(critique + refine)) | 5x |
| Vote (3 voters) | 4 (3 voters + 1 judge) | 4x |
| Refine (3 iter) | 4 (initial + 3 refinements) | 4x |

Budget accordingly when setting `max_budget_usd` in your policy.
