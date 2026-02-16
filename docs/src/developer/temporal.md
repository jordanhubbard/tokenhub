# Temporal Workflows

TokenHub optionally integrates with [Temporal](https://temporal.io/) for durable workflow execution. When enabled, every chat and orchestration request is dispatched as a Temporal workflow, providing visibility, retry guarantees, and execution history.

## Architecture

```
HTTP Handler
  │
  ├── Temporal Enabled?
  │     ├── Yes → Start Temporal Workflow → Wait for result → Return response
  │     └── No  → Direct engine call → Return response
  │
  └── Temporal Unavailable (runtime)
        └── Fall back to direct engine call
```

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal dispatch |
| `TOKENHUB_TEMPORAL_HOST` | `localhost:7233` | Temporal server address |
| `TOKENHUB_TEMPORAL_NAMESPACE` | `tokenhub` | Temporal namespace |
| `TOKENHUB_TEMPORAL_TASK_QUEUE` | `tokenhub-tasks` | Worker task queue name |

## Components

### Manager (`internal/temporal/manager.go`)

The manager creates and manages the Temporal client and worker:

```go
type Manager struct {
    client client.Client
    worker worker.Worker
}
```

- `New(cfg, activities)` — Creates Temporal client, registers workflows and activities
- `Start()` — Starts the worker (non-blocking)
- `Client()` — Returns the Temporal client for HTTP handlers
- `Stop()` — Gracefully stops worker and closes client

### Types (`internal/temporal/types.go`)

Input/output types for workflows:

```go
type ChatInput struct {
    RequestID string
    APIKeyID  string
    Request   router.Request
    Policy    router.Policy
}

type ChatOutput struct {
    Decision  router.Decision
    Response  json.RawMessage
    LatencyMs int64
    Error     string
}

type OrchestrationInput struct {
    RequestID string
    APIKeyID  string
    Request   router.Request
    Directive router.OrchestrationDirective
}
```

### Activities (`internal/temporal/activities.go`)

Activities are the atomic units of work. They receive injected dependencies:

```go
type Activities struct {
    Engine   *router.Engine
    Store    store.Store
    Health   *health.Tracker
    Metrics  *metrics.Registry
    EventBus *events.Bus
    Stats    *stats.Collector
    TSDB     *tsdb.Store
}
```

Key activities:
- **ChatActivity**: Calls `engine.RouteAndSend()` and returns the result
- **LogResultActivity**: Persists metrics, request logs, reward entries, TSDB points, and SSE events

### Workflows (`internal/temporal/workflows.go`)

- **ChatWorkflow**: Calls ChatActivity then LogResultActivity
- **OrchestrationWorkflow**: Calls ChatActivity for orchestration, then LogResultActivity

## HTTP Handler Integration

Handlers check for a Temporal client and dispatch accordingly:

```go
if d.TemporalClient != nil {
    run, err := d.TemporalClient.ExecuteWorkflow(ctx, opts, ChatWorkflow, input)
    if err != nil {
        // Temporal unavailable — fall back
        decision, resp, err = d.Engine.RouteAndSend(ctx, req, policy)
    } else {
        var output ChatOutput
        err = run.Get(ctx, &output)
        // Use output
    }
} else {
    decision, resp, err = d.Engine.RouteAndSend(ctx, req, policy)
}
```

The fallback ensures TokenHub continues to work even if Temporal becomes unavailable at runtime.

## Workflow Visibility

Admin endpoints expose Temporal workflow data:

- `GET /admin/v1/workflows?limit=50&status=RUNNING` — List workflows
- `GET /admin/v1/workflows/{id}` — Describe workflow
- `GET /admin/v1/workflows/{id}/history` — Activity history

Status values: `RUNNING`, `COMPLETED`, `FAILED`, `CANCELED`, `TERMINATED`, `CONTINUED_AS_NEW`, `TIMED_OUT`

## Docker Compose Setup

```yaml
temporal:
  image: temporalio/auto-setup:latest
  ports:
    - "7233:7233"
  environment:
    - DB=sqlite

temporal-ui:
  image: temporalio/ui:latest
  ports:
    - "8233:8080"
  environment:
    - TEMPORAL_ADDRESS=temporal:7233
```

Access the Temporal Web UI at `http://localhost:8233`.

## Streaming Note

Streaming requests (`stream: true`) bypass Temporal and use direct engine dispatch. This is because streaming requires a persistent HTTP connection for SSE, which is incompatible with Temporal's request-response workflow model.
