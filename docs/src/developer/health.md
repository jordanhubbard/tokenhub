# Health System

The health system tracks provider reliability and provides both passive monitoring (based on request outcomes) and active probing (periodic HTTP checks).

## Components

### Health Tracker (`internal/health/tracker.go`)

The tracker maintains per-provider health state:

```go
type ProviderHealthState struct {
    State         string    // "healthy", "degraded", "down"
    TotalRequests int64
    TotalErrors   int64
    ConsecErrors  int
    AvgLatencyMs  float64   // Exponential moving average
    LastError     string
    LastSuccessAt time.Time
    CooldownUntil time.Time
}
```

#### State Transitions

```
                   success
   ┌─────────────────────────────────┐
   │                                 │
   ▼          2+ consec errors       │
Healthy ──────────────────────► Degraded
   ▲                                 │
   │          success                │ 5+ consec errors
   │◄────────────────────────────────┤
   │                                 ▼
   │                               Down
   │          cooldown expired       │
   │          + success              │
   └─────────────────────────────────┘
```

#### Configuration

```go
type Config struct {
    DegradedThreshold int           // Consecutive errors to enter degraded (default: 2)
    DownThreshold     int           // Consecutive errors to enter down (default: 5)
    CooldownDuration  time.Duration // Time in down state before retry (default: 30s)
}
```

#### Recording Results

```go
// Called after every provider request
tracker.RecordSuccess(providerID, latencyMs)
tracker.RecordError(providerID, errorMsg)
```

Each success resets the consecutive error counter. Each error increments it and potentially triggers a state transition.

### Health Prober (`internal/health/prober.go`)

The prober performs active health checks against provider endpoints:

```go
type Probeable interface {
    ID() string
    HealthEndpoint() string
}
```

#### Probe Logic

- Sends `GET` requests to each provider's health endpoint
- Runs all probes concurrently with a per-probe timeout
- 2xx or 405 responses are considered healthy (405 is expected from some endpoints like Anthropic's `/v1/messages`)
- Any other response or connection error records a failure

#### Configuration

```go
type ProberConfig struct {
    Interval time.Duration // Time between probe rounds (default: 30s)
    Timeout  time.Duration // Per-probe HTTP timeout (default: 10s)
}
```

#### Provider Health Endpoints

| Provider | Endpoint | Success |
|----------|----------|---------|
| OpenAI | `GET /v1/models` | 2xx |
| Anthropic | `GET /v1/messages` | 2xx or 405 |
| vLLM | `GET /health` | 2xx |

## Integration with Routing

The routing engine queries health state during model selection:

1. **Eligibility**: Models from providers in "down" state are excluded
2. **Scoring**: The failure rate (`totalErrors / totalRequests`) contributes to the model's score
3. **Latency**: The exponential moving average latency contributes to the model's score

```go
type HealthChecker interface {
    ProviderState(providerID string) ProviderHealthState
}
```

The tracker implements this interface and is passed to the engine via `engine.SetHealthChecker()`.

## Observability

Provider health is exposed via:
- `GET /admin/v1/health` — JSON health state for all providers
- Admin UI health panel — Visual health badges
- SSE events — Error events include provider state changes
