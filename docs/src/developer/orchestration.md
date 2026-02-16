# Orchestration Modes

Orchestration enables multi-model reasoning patterns. The orchestration logic lives in `internal/router/engine.go` in the `Orchestrate()` method.

## Architecture

```
Orchestrate(req, directive)
  ├── adversarial: Plan → Critique → Refine (loop)
  ├── vote:        N Voters → Judge → Select best
  ├── refine:      Generate → Refine → Refine (loop)
  └── planning:    Single RouteAndSend with planning profile
```

## Model Selection for Orchestration

Each orchestration mode needs a "primary" model and optionally a "review" model. Models are selected by:

1. **Explicit model ID**: `primary_model_id` / `review_model_id` in the directive
2. **Weight floor**: `primary_min_weight` / `review_min_weight` sets minimum capability
3. **Automatic**: Falls back to routing engine scoring with the appropriate policy

For review models, the policy uses `high_confidence` mode by default to ensure a capable judge/critic.

## Adversarial Mode

Three-phase iterative refinement with a separate critique model:

```go
// Phase 1: Plan
planResp = RouteAndSend(req with "Create a detailed plan...")
// Phase 2: Critique (loop N iterations)
critiqueResp = RouteAndSend(req with "Critique this plan: ...")
// Phase 3: Refine
refinedResp = RouteAndSend(req with "Refine based on critique: ...")
```

The critique and refine phases repeat for `directive.Iterations` (default 1).

**Output schema:**
```json
{
  "initial_plan": "Plan text from phase 1",
  "critique": "Final critique from last iteration",
  "refined_plan": "Final refined plan from last iteration"
}
```

## Vote Mode

Multiple models respond independently, a judge selects the best:

```go
// Phase 1: Collect votes (one per eligible model, up to 3)
for model in eligibleModels:
    responses[model] = RouteAndSend(req, model)

// Phase 2: Judge
judgeResp = RouteAndSend(req with "Select the best response (1-N): ...")
selectedIdx = parseNumber(judgeResp) - 1
```

**Output schema:**
```json
{
  "responses": [
    {"model": "gpt-4", "content": "...", "selected": true},
    {"model": "claude-sonnet", "content": "...", "selected": false}
  ],
  "selected": 0,
  "judge": "claude-opus"
}
```

## Refine Mode

Single model iteratively improves its own response:

```go
// Phase 1: Initial response
resp = RouteAndSend(req)

// Phase 2: Iterative refinement (loop N iterations)
for i := 0; i < iterations; i++:
    resp = RouteAndSend(req with "Review and improve: " + resp)
```

**Output schema:**
```json
{
  "refined_response": "Final refined text",
  "iterations": 3,
  "model": "claude-opus"
}
```

## Planning Mode

Falls through to a standard `RouteAndSend` with the `planning` routing profile:

```go
decision, resp, err = RouteAndSend(req, Policy{Mode: "planning"})
```

## Cost and Latency

Orchestration makes multiple LLM calls. The `Decision` returned by `Orchestrate()` accumulates costs from all calls:

```go
totalDecision.EstimatedCostUSD += stepDecision.EstimatedCostUSD
```

The routing reason is set to `{mode}-orchestration` (e.g., `adversarial-orchestration`).

## Temporal Integration

When Temporal is enabled, orchestration runs as a `OrchestrationWorkflow`:
- Each LLM call becomes a Temporal activity
- Activities run with retry policies and timeouts
- The full execution is visible in the Temporal UI
- If Temporal is unavailable, falls back to direct orchestration

See [Temporal Workflows](temporal.md) for details.

## Adding New Orchestration Modes

To add a new mode:

1. Add the mode name to the validation list in `handlers_plan.go`
2. Add a case in `Orchestrate()` in `engine.go`
3. Implement the multi-call pattern following existing modes
4. Return a `json.RawMessage` with the composite result
5. Update the `OrchestrationWorkflow` in `temporal/workflows.go` if using Temporal
