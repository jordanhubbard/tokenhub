# Routing & Policy

Tokenhub routing is multi-objective optimization with constraints.

## Inputs

Per-request:
- estimated input tokens
- predicted output tokens (or bounded default)
- min weight requirement
- max USD budget
- max latency
- mode: normal|cheap|high_confidence|planning|adversarial

Per-model:
- weight
- max context
- pricing
- health state
- rate limit state
- latency EWMA

## Constraints

A model is eligible iff:
- enabled
- provider enabled and healthy enough (or allowed in degraded mode)
- context_estimate <= max_context (with safety margin)
- weight >= min_weight (if specified)
- estimated_cost <= max_budget (if specified)

## Scoring

Example scoring function (lower is better):

score = w_cost * norm(cost)
      + w_latency * norm(latency)
      + w_failure * norm(failure_rate)
      - w_weight * norm(weight)

Mode sets weights:

- cheap: w_cost high, w_weight low
- high_confidence: w_weight high, w_cost low
- normal: balanced
- planning/adversarial: enforce min weights for planner/critic roles

## Fallbacks

If selected model fails:
- context overflow: escalate to next larger-context model
- 429 rate limited: respect Retry-After if present; else reroute
- 5xx/provider error: reroute to same-weight peer or next option
- timeout: reroute or fail based on max_latency

Maintain a per-request attempt budget.

## Token estimation

Tokenhub can do:
- quick heuristic estimator (characters/4-ish) for routing
- optional exact tokenization per provider/model (future)

Routing should be conservative: reserve headroom (e.g. 10-15%) to avoid context overflow.

