# Routing Engine

The routing engine (`internal/router/engine.go`) is TokenHub's core component. It manages the model registry, scores models against request policies, dispatches to provider adapters, and handles failover.

## Engine Structure

```go
type Engine struct {
    adapters      map[string]Sender         // provider ID → adapter
    models        []Model                   // registered models
    healthChecker HealthChecker             // optional health state provider
    banditPolicy  BanditPolicy              // optional Thompson Sampling
    defaults      EngineConfig              // default mode, budget, latency
}
```

## Model Registration

Models and adapters are registered at startup and can be modified at runtime:

```go
eng.RegisterAdapter(openai.New("openai", apiKey, baseURL))
eng.RegisterModel(router.Model{
    ID: "gpt-4", ProviderID: "openai",
    Weight: 8, MaxContextTokens: 128000,
    InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true,
})
```

## Scoring Algorithm

The `scoreModel()` function computes a composite score for each eligible model:

```
score = (costNorm * w.Cost) + (latencyNorm * w.Latency) + (failureNorm * w.Failure) - (weightNorm * w.Weight)
```

**Normalization:**
- `costNorm`: `estimatedCost / maxBudgetUSD` (clamped to 0-1)
- `latencyNorm`: `avgLatencyMs / maxLatencyMs` (from health tracker)
- `failureNorm`: `errorRate` (from health tracker, 0-1)
- `weightNorm`: `model.Weight / 10.0`

Lower scores are better. The weight term is subtracted (higher weight reduces score).

## Eligibility Filtering

`eligibleModels()` filters the model registry:

1. Must be `Enabled`
2. Must meet `min_weight` threshold
3. Must have sufficient context window (estimated tokens * 1.15 headroom)
4. Provider must not be in "down" health state
5. Estimated cost must be within budget

For `thompson` mode, eligible models are reordered by Thompson Sampling instead of the scoring function.

## RouteAndSend Flow

```go
func (e *Engine) RouteAndSend(ctx context.Context, req Request, policy Policy) (Decision, ProviderResponse, error)
```

1. Resolve defaults (fill in zero-value policy fields from server defaults)
2. Get eligible models
3. If `model_hint` is set and the model exists, try it first
4. Sort remaining models by score
5. For each model (up to 5 attempts):
   a. Look up the adapter by `model.ProviderID`
   b. Call `adapter.Send(ctx, model.ID, req)`
   c. On success: return decision + response
   d. On error: classify the error and decide next action:
      - `ErrContextOverflow`: Find a model with larger context
      - `ErrRateLimited`: Skip to next provider (honor `RetryAfter`)
      - `ErrTransient`: Retry same model with exponential backoff
      - `ErrFatal`: Try next model

## Orchestration

`Orchestrate()` handles multi-model modes:

```go
func (e *Engine) Orchestrate(ctx context.Context, req Request, dir OrchestrationDirective) (Decision, json.RawMessage, error)
```

See [Orchestration Modes](orchestration.md) for details.

## Streaming

```go
func (e *Engine) RouteAndStream(ctx context.Context, req Request, policy Policy) (Decision, io.ReadCloser, error)
```

Same model selection as `RouteAndSend`, but calls `SendStream()` on adapters that implement `StreamSender`. Returns the raw SSE stream body for the HTTP handler to proxy.

## Health Integration

The engine optionally uses a `HealthChecker` interface:

```go
type HealthChecker interface {
    ProviderState(providerID string) ProviderHealthState
}
```

This provides:
- Error rate for scoring (`failureNorm`)
- "Down" state for eligibility filtering
- Average latency for scoring (`latencyNorm`)

## Thompson Sampling Integration

When a `BanditPolicy` is set:

```go
type BanditPolicy interface {
    Sample(models []Model, tokenBucket string) []Model
}
```

In `thompson` mode, `eligibleModels()` calls `banditPolicy.Sample()` instead of the scoring function. The sampler draws from Beta distributions parameterized by historical reward data.

## Thread Safety

The engine uses `sync.RWMutex` to protect the model registry and adapter map. Reads (model selection, routing) take a read lock. Writes (register/unregister) take a write lock.
