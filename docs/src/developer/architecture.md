# Architecture

TokenHub is a Go application structured as a layered system with clear package boundaries and dependency injection.

## Package Layout

```
tokenhub/
├── cmd/tokenhub/          # Entry point, signal handling, HTTP server lifecycle
├── internal/
│   ├── app/               # Server construction, config loading, wiring
│   ├── apikey/            # API key manager + auth middleware
│   ├── events/            # In-memory event bus (pub/sub for SSE)
│   ├── health/            # Provider health tracker + active prober
│   ├── httpapi/           # HTTP handlers and route mounting
│   ├── logging/           # Structured logging setup (slog)
│   ├── metrics/           # Prometheus metric registry
│   ├── providers/         # Provider adapter contract + context helpers
│   │   ├── openai/        # OpenAI adapter
│   │   ├── anthropic/     # Anthropic adapter
│   │   └── vllm/          # vLLM adapter
│   ├── router/            # Routing engine, scoring, orchestration, Thompson Sampling
│   ├── stats/             # In-memory statistics collector
│   ├── store/             # Persistence layer (SQLite)
│   ├── temporal/          # Temporal workflow integration
│   ├── tsdb/              # Time-series database (SQLite-backed)
│   └── vault/             # AES-256-GCM encrypted credential vault
├── web/                   # Embedded admin UI (index.html)
└── docs/                  # This documentation
```

## Dependency Flow

```
cmd/tokenhub/main.go
  └── internal/app.NewServer(cfg)
        ├── vault.New()
        ├── router.NewEngine()
        ├── store.NewSQLite()
        ├── health.NewTracker()
        ├── health.NewProber()         → health.Tracker
        ├── loadCredentialsFile()      → router.Engine
        ├── loadPersistedProviders()   → router.Engine
        ├── router.NewThompsonSampler()
        ├── apikey.NewManager()        → store.Store
        ├── metrics.New()
        ├── events.NewBus()
        ├── stats.NewCollector()
        ├── tsdb.New()
        ├── temporal.New()             → (optional)
        └── httpapi.MountRoutes()      → Dependencies{...}
```

All dependencies flow downward. HTTP handlers receive a `Dependencies` struct containing all services they need.

## Key Interfaces

### `router.Sender`

The provider adapter contract:

```go
type Sender interface {
    ID() string
    Send(ctx context.Context, model string, req Request) (ProviderResponse, error)
    ClassifyError(err error) *ClassifiedError
}
```

### `router.StreamSender`

Optional streaming extension:

```go
type StreamSender interface {
    Sender
    SendStream(ctx context.Context, model string, req Request) (io.ReadCloser, error)
}
```

### `health.Probeable`

Health probe interface for providers:

```go
type Probeable interface {
    ID() string
    HealthEndpoint() string
}
```

### `store.Store`

Persistence interface with methods for models, providers, request logs, audit logs, reward entries, API keys, vault blobs, and routing configuration.

## Request Lifecycle

1. **HTTP handler** receives the request, validates input, extracts API key
2. **Directive parser** scans messages for `@@tokenhub` overrides and strips them
3. **Policy resolution**: Merge request policy with server defaults and directive overrides
4. **Token estimation**: Estimate input tokens (explicit or chars/4 heuristic)
5. **Model selection**: Filter eligible models, score by policy weights, sort
6. **Provider dispatch**: Call the top-scored model's adapter
7. **Error handling**: On failure, classify the error and escalate/retry/failover
8. **Output shaping**: Apply output format (JSON schema validation, think-block stripping)
9. **Observability**: Record metrics, TSDB points, request logs, reward entries, SSE events
10. **Response**: Return the provider response with routing metadata

## Concurrency Model

- The HTTP server uses Go's standard `net/http` with chi router (goroutine per request)
- The TSDB uses internal write buffering (batched inserts)
- The health prober runs as a background goroutine with configurable interval
- The Thompson Sampler refresh runs as a background goroutine
- The TSDB prune loop runs as a background goroutine (hourly)
- Temporal workflows (when enabled) are managed by the Temporal worker

All background goroutines are cleanly stopped via `Server.Close()`.

## Configuration

All configuration is via environment variables, loaded in `internal/app/config.go`. See [Configuration Reference](../deployment/configuration.md) for the complete list.

## Embedding

The admin UI (`web/index.html`) is embedded in the binary using Go's `//go:embed` directive in the root `embed.go` file. This means the entire application is a single self-contained binary.
