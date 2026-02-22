# Extending TokenHub

This guide covers common extension points for adding functionality to TokenHub.

## Adding a New Provider

1. **Create the adapter package**:

```
internal/providers/newprovider/
├── adapter.go      # Sender implementation
└── adapter_test.go # Tests
```

2. **Implement the interfaces**:

```go
package newprovider

type Adapter struct {
    id      string
    apiKey  string
    baseURL string
    client  *http.Client
}

// Required: router.Sender
func (a *Adapter) ID() string { return a.id }
func (a *Adapter) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) { ... }
func (a *Adapter) ClassifyError(err error) *router.ClassifiedError { ... }

// Optional: router.StreamSender
func (a *Adapter) SendStream(ctx context.Context, model string, req router.Request) (io.ReadCloser, error) { ... }

// Optional: health.Probeable
func (a *Adapter) HealthEndpoint() string { return a.baseURL + "/health" }
```

3. **Register in server.go**:

```go
// In registerProviders()
if key := os.Getenv("TOKENHUB_NEWPROVIDER_API_KEY"); key != "" {
    eng.RegisterAdapter(newprovider.New("newprovider", key, "https://api.newprovider.com"))
    logger.Info("registered provider", slog.String("provider", "newprovider"))
}
```

4. **Add default models**:

```go
// In registerDefaultModels()
{ID: "new-model", ProviderID: "newprovider", Weight: 6, MaxContextTokens: 32000, InputPer1K: 0.002, OutputPer1K: 0.006, Enabled: true},
```

5. **Add config**:

```go
// In config.go
NewProviderAPIKey string `env:"TOKENHUB_NEWPROVIDER_API_KEY"`
```

## Adding a New Routing Mode

1. **Define the weight profile** in `internal/router/engine.go`:

```go
var modeWeights = map[string]weights{
    // ...existing modes...
    "mymode": {Cost: 0.3, Latency: 0.2, Failure: 0.2, Weight: 0.3},
}
```

2. **Add validation** in `internal/httpapi/handlers_chat.go` and `handlers_plan.go`:

```go
case "mymode":
    // valid
```

3. **Add to routing config validation** in `handlers_routing.go`.

## Adding a New Orchestration Mode

1. **Add the case** in `engine.Orchestrate()`:

```go
case "mymode":
    // Implement multi-call pattern
    result, err := json.Marshal(map[string]any{...})
    return totalDecision, result, err
```

2. **Add validation** in `handlers_plan.go`.

3. **Update Temporal** if using workflows:

```go
// In OrchestrationWorkflow
case "mymode":
    // Implement as Temporal activities
```

## Adding New Admin Endpoints

1. **Create handler** in `internal/httpapi/handlers_newfeature.go`:

```go
func NewFeatureHandler(d Dependencies) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // Handler logic
    }
}
```

2. **Mount route** in `internal/httpapi/routes.go`:

```go
r.Get("/admin/v1/newfeature", NewFeatureHandler(d))
```

3. **Add to Dependencies** if new services are needed.

## Adding New Metrics

In `internal/metrics/metrics.go`:

```go
type Registry struct {
    // ...existing metrics...
    NewMetric *prometheus.CounterVec
}

func New() *Registry {
    r := &Registry{
        NewMetric: prometheus.NewCounterVec(prometheus.CounterOpts{
            Namespace: "tokenhub",
            Name:      "new_metric_total",
            Help:      "Description of the new metric",
        }, []string{"label1", "label2"}),
    }
    // Register with Prometheus
    return r
}
```

## Adding New Store Operations

1. **Add to the interface** in `internal/store/store.go`
2. **Implement in SQLite** in `internal/store/sqlite.go`
3. **Add migration** in `Migrate()` if new tables are needed
4. **Write tests** in `internal/store/sqlite_test.go`

## Testing

TokenHub uses Go's standard `testing` package. Key test patterns:

- **Unit tests**: Each package has `*_test.go` files
- **Integration tests**: `internal/httpapi/handlers_test.go` tests the full HTTP stack
- **Mock adapters**: `mockSender` in handler tests simulates provider responses
- **In-memory SQLite**: Tests use `:memory:` DSN for isolated databases

Run all tests:
```bash
make test        # Standard tests
make test-race   # With race detector
```

## Build

```bash
make build       # Build to bin/tokenhub
make package     # Build Docker image
make lint        # Run linter (requires golangci-lint)
make vet         # Go vet
```
