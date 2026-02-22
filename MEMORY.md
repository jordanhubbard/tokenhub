# MEMORY.md — TokenHub Codebase Reference

Last updated: 2026-02-21 (v0.2.6)

This document is a comprehensive reference for AI assistants working on the TokenHub codebase. It covers architecture, build system, data flow, debugging, and known gotchas.

## What TokenHub Is

TokenHub is an **LLM routing gateway** written in Go. It sits between clients and multiple LLM providers (OpenAI, Anthropic, vLLM, etc.), providing:

- Intelligent model selection via a multi-objective scoring engine
- Provider failover with health tracking and cooldown
- OpenAI-compatible `/v1/chat/completions` API (translates Anthropic responses)
- Encrypted credential vault (AES-256-GCM + Argon2id)
- API key management with scoping, budgets, and rotation enforcement
- Thompson Sampling (contextual bandit) for adaptive routing
- Multi-model orchestration (adversarial, vote, refine modes)
- Full observability: Prometheus, embedded TSDB, SSE event stream, request logs
- Admin dashboard (single-file HTML with Cytoscape.js + D3.js)
- Optional Temporal workflow engine for durable orchestration

## Repository Layout

```
tokenhub/
├── cmd/
│   ├── tokenhub/main.go          # Server binary entry point
│   └── tokenhubctl/main.go       # CLI admin tool
├── internal/
│   ├── app/
│   │   ├── config.go             # Env-var config loading (TOKENHUB_*)
│   │   └── server.go             # Server init, provider registration, background loops
│   ├── apikey/
│   │   ├── manager.go            # API key CRUD (bcrypt hashing, prefix matching)
│   │   ├── budget.go             # Monthly spend budget enforcement
│   │   └── middleware.go         # HTTP auth middleware (Bearer token validation)
│   ├── circuitbreaker/
│   │   └── breaker.go            # Circuit breaker for Temporal dispatch
│   ├── events/
│   │   └── bus.go                # In-memory pub/sub for SSE streaming
│   ├── health/
│   │   ├── tracker.go            # Per-provider health state machine (healthy/degraded/down)
│   │   └── prober.go             # Background HTTP health probe goroutine
│   ├── httpapi/
│   │   ├── routes.go             # Chi router mount, admin auth middleware, Dependencies struct
│   │   ├── observe.go            # recordObservability() — central sink for all 6 o13y backends
│   │   ├── handlers_chat.go      # POST /v1/chat (native TokenHub format)
│   │   ├── handlers_openai.go    # POST /v1/chat/completions (OpenAI-compatible)
│   │   ├── handlers_plan.go      # POST /v1/plan (multi-model orchestration)
│   │   ├── handlers_admin.go     # CRUD for providers, models, vault, routing config
│   │   ├── handlers_apikeys.go   # CRUD for API keys
│   │   ├── handlers_events.go    # GET /admin/v1/events (SSE stream)
│   │   ├── handlers_stats.go     # GET /admin/v1/stats (rolling window aggregates)
│   │   ├── handlers_tsdb.go      # TSDB query/metrics/prune/retention endpoints
│   │   ├── handlers_workflows.go # Temporal workflow visibility endpoints
│   │   └── handlers_extended.go  # Simulate, discover, rewards, audit, logs handlers
│   ├── idempotency/
│   │   ├── cache.go              # In-memory TTL cache for idempotency keys
│   │   └── middleware.go         # X-Idempotency-Key middleware
│   ├── logging/
│   │   └── logging.go            # slog setup with dynamic level
│   ├── metrics/
│   │   └── metrics.go            # Prometheus counters/histograms registry
│   ├── providers/
│   │   ├── http.go               # DoRequest/DoStreamRequest shared HTTP helpers
│   │   ├── contract.go           # StatusError type
│   │   ├── context.go            # Request ID context propagation
│   │   ├── openai/adapter.go     # OpenAI adapter (Sender + StreamSender + Describer)
│   │   ├── anthropic/adapter.go  # Anthropic adapter (translates response format)
│   │   └── vllm/adapter.go       # vLLM adapter (round-robin endpoints, optional auth)
│   ├── ratelimit/
│   │   └── ratelimit.go          # Per-IP token bucket rate limiter
│   ├── router/
│   │   ├── engine.go             # Core routing engine (1121 lines) — model selection, failover
│   │   ├── types.go              # Request, Message, Policy, Decision, Model, etc.
│   │   ├── directives.go         # In-band routing directive parsing from messages
│   │   ├── thompson.go           # Thompson Sampling bandit policy
│   │   ├── thompson_refresh.go   # Background refresh loop for bandit parameters
│   │   ├── rewards.go            # Reward computation for bandit feedback
│   │   ├── format.go             # Output format shaping (JSON, markdown, strip think)
│   │   └── schema.go             # JSON Schema extraction/validation
│   ├── stats/
│   │   └── collector.go          # In-memory rolling-window stats (1m, 5m, 1h, 24h)
│   ├── store/
│   │   ├── store.go              # Store interface + domain types (ModelRecord, etc.)
│   │   └── sqlite.go             # SQLite implementation (modernc.org/sqlite, pure Go)
│   ├── temporal/
│   │   ├── types.go              # Workflow/activity input/output structs
│   │   ├── workflows.go          # ChatWorkflow, OrchestrationWorkflow, StreamLogWorkflow
│   │   ├── activities.go         # SendToProvider, LogResult, ClassifyAndEscalate, etc.
│   │   └── manager.go            # Temporal client/worker lifecycle
│   ├── tracing/
│   │   └── tracing.go            # OpenTelemetry OTLP/HTTP setup
│   ├── tsdb/
│   │   └── tsdb.go               # Embedded SQLite-backed time-series DB
│   └── vault/
│       └── vault.go              # AES-256-GCM encrypted key-value store
├── web/
│   ├── index.html                # Admin dashboard (~1240 lines, self-contained)
│   ├── cytoscape.min.js          # Topology graph library
│   └── d3.min.js                 # Charting library
├── web.go                        # go:embed web/* for the admin UI
├── config/config.example.yaml    # Example config
├── scripts/
│   ├── release.sh                # Semantic version tagging + GHCR push
│   ├── setup-docker.sh           # Fix macOS Docker symlink issues
│   └── backup.sh                 # SQLite backup helper
├── tests/
│   ├── integration.sh            # Integration test runner
│   ├── e2e-temporal.sh           # Temporal end-to-end test
│   └── mock-provider.conf        # nginx config for mock vLLM
├── k8s/                          # Kubernetes manifests
├── deploy/prometheus-alerts.yml  # Prometheus alerting rules
├── docs/src/                     # mdBook documentation source
├── Makefile                      # Primary build interface
├── Dockerfile                    # Multi-stage production image (Alpine 3.21)
├── Dockerfile.dev                # Builder image with Go + mdbook + golangci-lint
├── docker-compose.yaml           # Local dev stack (tokenhub + Temporal + mock vLLM)
├── bootstrap.local.example       # Example post-startup provider registration script
└── CLAUDE.md                     # AI assistant workflow instructions
```

## Build System

**All interactions go through the Makefile.** The build runs inside a Docker container (`tokenhub-builder`) to ensure reproducibility.

| Command | What It Does |
|---------|--------------|
| `make build` | Compiles `bin/tokenhub` and `bin/tokenhubctl` inside the builder container |
| `make docker` | Builds the production Docker image (`tokenhub:$(VERSION)`) |
| `make run` | Builds image, starts compose stack, runs `bootstrap.local`, tails logs |
| `make test` | Runs `go test ./...` inside the builder container |
| `make test-race` | Tests with race detector |
| `make test-coverage` | Tests with coverage profile |
| `make lint` | golangci-lint |
| `make docs` | Builds mdBook documentation in `docs/book/` |
| `make release` | Bumps patch version, tags, builds, pushes to GHCR |
| `make release-minor` | Bumps minor version |
| `make clean` | Removes `bin/`, `docs/book/`, `coverage.out` |

### Version tagging

`VERSION` is derived from `git describe --tags --always --dirty`. Release tags follow semver (`v0.2.6`). The `scripts/release.sh` script enforces a clean working tree.

### Docker image

- **Build stage**: `golang:1.24-alpine` — compiles binary, builds docs with mdbook
- **Runtime stage**: `alpine:3.21` — runs as non-root `tokenhub` user
- Uses `modernc.org/sqlite` (pure Go, no CGO) so `CGO_ENABLED=0` works
- Published to `ghcr.io/jordanhubbard/tokenhub:{version}` and `:latest`

## Configuration

All config is via environment variables (`TOKENHUB_*`). See `internal/app/config.go` for the full list with defaults.

### Critical variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `TOKENHUB_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `TOKENHUB_DB_DSN` | `file:/data/tokenhub.sqlite` | SQLite database path |
| `TOKENHUB_ADMIN_TOKEN` | (empty = unprotected!) | Bearer token for `/admin/v1/*` endpoints |
| `TOKENHUB_VAULT_ENABLED` | `true` | Enable encrypted credential vault |
| `TOKENHUB_OPENAI_API_KEY` | — | Registers OpenAI provider adapter on startup |
| `TOKENHUB_ANTHROPIC_API_KEY` | — | Registers Anthropic provider adapter on startup |
| `TOKENHUB_VLLM_ENDPOINTS` | — | Comma-separated vLLM endpoints (round-robin) |
| `TOKENHUB_EXTRA_PROVIDERS` | — | JSON array of `{id, endpoint, api_key}` for additional providers |
| `TOKENHUB_CREDENTIALS_FILE` | `~/.tokenhub/credentials` | JSON file with providers/models (must be mode 0600) |
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal workflow engine |
| `TOKENHUB_OTEL_ENABLED` | `false` | Enable OpenTelemetry tracing |
| `TOKENHUB_RATE_LIMIT_RPS` | `60` | Per-IP rate limit (applied to `/v1/*` only) |
| `TOKENHUB_PROVIDER_TIMEOUT_SECS` | `30` | HTTP timeout for provider requests |

### Hot reload

Send `SIGHUP` to reload rate limits, routing defaults, and log level without restart.

## Request Flow

```
Client
  │
  ├─ POST /v1/chat/completions   (OpenAI-compatible)
  ├─ POST /v1/chat               (native TokenHub format)
  └─ POST /v1/plan               (multi-model orchestration)
       │
       ├─ API key auth middleware (apikey.AuthMiddleware)
       ├─ Rate limiting (ratelimit.Limiter.Middleware)
       ├─ Idempotency check (idempotency.Middleware)
       │
       ├─ [Optional] Temporal dispatch (if enabled + circuit breaker closed)
       │   └─ ChatWorkflow → SendToProvider activity → LogResult activity
       │
       └─ Direct engine path:
           ├─ engine.RouteAndSend() or engine.RouteAndStream()
           │   ├─ Parse in-band directives from messages
           │   ├─ Score eligible models (cost, weight, latency, failure rate)
           │   ├─ Thompson Sampling (if mode=thompson)
           │   ├─ Select top model, call adapter.Send()
           │   ├─ On failure: classify error, failover/escalate/retry
           │   └─ Return Decision + ProviderResponse
           │
           ├─ extractUsage(resp) → parse actual tokens from provider response
           ├─ computeActualCost() → replace estimate with real token-based cost
           │
           └─ recordObservability() → fans out to 6 sinks:
               ├─ Prometheus (tokenhub_requests_total, _latency_ms, _cost_usd_total, _tokens_total)
               ├─ Store (request_logs table + reward_logs table)
               ├─ EventBus → SSE stream (route_success/route_error events)
               ├─ Stats collector (in-memory rolling windows: 1m, 5m, 1h, 24h)
               ├─ TSDB (embedded SQLite: latency, cost, tokens time-series)
               └─ Budget cache invalidation
```

## Observability Pipeline (6 Sinks)

The central function is `recordObservability()` in `internal/httpapi/observe.go`. Every successful or failed request passes through it.

### Data fields carried end-to-end

| Field | Source | Sinks |
|-------|--------|-------|
| `InputTokens` / `OutputTokens` | Parsed from provider response via `extractUsage()` | All 6 sinks |
| `CostUSD` | `computeActualCost()` using actual tokens + per-1K rates | All 6 sinks |
| `LatencyMs` | Wall-clock `time.Since(start)` | All 6 sinks |
| `ModelID` / `ProviderID` | From `router.Decision` | All 6 sinks |
| `Success` / `ErrorClass` | Determined by handler | All 6 sinks |
| `RequestID` / `APIKeyID` | From middleware context | Store only |

### Token extraction

`extractUsage()` in `observe.go` handles two response formats:
- **OpenAI**: `usage.prompt_tokens` / `usage.completion_tokens`
- **Anthropic**: `usage.input_tokens` / `usage.output_tokens`

Streaming responses don't include usage blocks, so tokens remain 0 for streamed requests. Cost falls back to the pre-flight estimate in that case.

### TSDB flush timing

The TSDB buffers writes and flushes every 30 seconds (or when the buffer hits 100 points). Newly written metrics won't appear in `/admin/v1/tsdb/query` until the next flush.

## Database Schema

SQLite via `modernc.org/sqlite` (pure Go, no CGO). Schema in `internal/store/sqlite.go` `Migrate()`.

### Tables

- `models` — Model configuration (id, provider_id, weight, pricing, enabled)
- `providers` — Provider configuration (id, type, base_url, cred_store)
- `request_logs` — Per-request audit trail (tokens, cost, latency, status)
- `reward_logs` — Thompson Sampling feedback data
- `audit_logs` — Admin mutation audit trail
- `api_keys` — API key records (bcrypt hashes, scopes, budgets)
- `vault_blob` — Encrypted vault data (single row, id=1)
- `routing_config` — Persisted routing policy defaults (single row, id=1)
- `tsdb_points` — Time-series data (ts, metric, model_id, provider_id, value)

### Schema migrations

New columns are added via idempotent `ALTER TABLE` migrations in `Migrate()`. Each migration checks `pragma_table_info()` before adding the column. To add a new column:

```go
alterMigrations := []struct{ table, column, ddl string }{
    {"request_logs", "new_column", "ALTER TABLE request_logs ADD COLUMN new_column TYPE NOT NULL DEFAULT value"},
}
```

## Admin Dashboard (UI)

Single HTML file at `web/index.html` (~1240 lines). Embedded via `go:embed web/*` in `web.go` and served at `/admin/`.

### Key UI components

- **Topology graph** (Cytoscape.js): Shows provider→model edges, animated on SSE events
- **Trend charts** (D3.js): Cost, latency, and tokens over time from TSDB data
- **Overview stat cards**: Requests, Tokens, Cost, Avg Latency, Errors — seeded from `/admin/v1/stats` on load, then incremented by SSE events
- **Model Leaderboard**: Rolling-window stats per model (prefers 24h > 1h > 5m > 1m)
- **Setup Wizard**: Multi-step flow for adding providers (type → endpoint → credentials → test → discover models)
- **Edit modals**: For providers and models (PATCH endpoints)
- **What-If Simulator**: Tests routing decisions without sending requests
- **SSE Decision Feed**: Real-time event stream with latency, cost, tokens, reason
- **Request Log**: Paginated historical request log from the database
- **Vault controls**: Setup/unlock/lock/rotate UI

### Cache busting

HTML is served with `no-cache, must-revalidate`. Static assets (`cytoscape.min.js`, `d3.min.js`) get `?v={hash}` query params computed from the HTML content hash.

## Provider Adapters

All adapters implement `router.Sender` (and optionally `router.StreamSender`, `router.Describer`).

| Adapter | Package | Auth | Health Endpoint |
|---------|---------|------|-----------------|
| OpenAI | `providers/openai` | `Authorization: Bearer {key}` | `{base}/v1/models` |
| Anthropic | `providers/anthropic` | `x-api-key: {key}` + `anthropic-version` | `{base}/v1/messages` (405 = healthy) |
| vLLM | `providers/vllm` | Optional Bearer key | `{endpoint}/health` |

### Dynamic registration

`registerProviderAdapter()` in `handlers_admin.go` constructs and registers adapters at runtime when providers are created/updated via the API. The engine's `RegisterAdapter()` is idempotent (replaces existing adapter with same ID).

**Caveat**: The health prober is initialized once at startup with the adapter list at that time. Dynamically registered adapters won't be probed until restart. The health tracker still receives success/failure signals from actual request routing, so it's not completely blind.

## Routing Engine

`internal/router/engine.go` (1121 lines) is the core.

### Model selection algorithm

1. Filter eligible models (enabled, adapter exists, not in cooldown, within context/budget limits)
2. For each model, compute a multi-objective score:
   - `normCost` — normalized estimated cost (lower is better)
   - `normWeight` — normalized configured weight (higher is better)
   - `normLatency` — normalized historical avg latency (lower is better)
   - `normFailure` — normalized failure rate (lower is better)
3. Apply mode-specific weights to produce final score:
   - `cheap`: cost=0.7, weight=0.1, latency=0.1, failure=0.1
   - `normal`: cost=0.25, weight=0.25, latency=0.25, failure=0.25
   - `high_confidence`: cost=0.05, weight=0.7, latency=0.1, failure=0.15
   - `planning`: cost=0.1, weight=0.6, latency=0.1, failure=0.2
   - `thompson`: Uses Thompson Sampling (Beta distribution draws)
4. Sort by score descending, attempt top model first
5. On failure: classify error (rate_limited, transient, context_overflow, fatal), failover to next model

### Orchestration modes

- **adversarial**: Send to primary model, then critique/refine with review model
- **vote**: Send to N models in parallel, use a judge model to pick best response
- **refine**: Iterative self-refinement with the same model

## Key Interfaces

```go
// Provider adapter (must implement)
type Sender interface {
    ID() string
    Send(ctx context.Context, model string, req Request) (ProviderResponse, error)
    ClassifyError(err error) *ClassifiedError
}

// Optional: streaming support
type StreamSender interface {
    Sender
    SendStream(ctx context.Context, model string, req Request) (io.ReadCloser, error)
}

// Optional: metadata for admin UI
type Describer interface {
    HealthEndpoint() string
}

// Persistence layer
type Store interface {
    // Models, Providers, RequestLogs, Vault, Routing, Audit, Rewards, API Keys, Log retention
    Migrate(ctx context.Context) error
    Close() error
}
```

## API Endpoints

### Proxy endpoints (require API key)

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/v1/chat` | Native TokenHub chat (policy hints, orchestration) |
| POST | `/v1/chat/completions` | OpenAI-compatible chat completions |
| GET | `/v1/models` | OpenAI-compatible model listing |
| POST | `/v1/plan` | Multi-model orchestration |

### Admin endpoints (require admin token)

| Method | Path | Purpose |
|--------|------|---------|
| GET/POST | `/admin/v1/providers` | List / upsert providers |
| PATCH/DELETE | `/admin/v1/providers/{id}` | Update / delete provider |
| GET/POST | `/admin/v1/models` | List / upsert models |
| PATCH/DELETE | `/admin/v1/models/*` | Update / delete model (wildcard for IDs with `/`) |
| POST | `/admin/v1/vault/unlock` | Unlock vault with master password |
| POST | `/admin/v1/vault/lock` | Lock vault |
| POST | `/admin/v1/vault/rotate` | Rotate vault master password |
| GET/PUT | `/admin/v1/routing-config` | Get / set routing policy defaults |
| GET | `/admin/v1/health` | Provider health status |
| GET | `/admin/v1/stats` | Rolling-window stats (1m/5m/1h/24h) |
| GET | `/admin/v1/logs` | Request log (paginated) |
| GET | `/admin/v1/audit` | Audit log (paginated) |
| GET | `/admin/v1/rewards` | Reward log (paginated) |
| GET | `/admin/v1/engine/models` | Runtime engine state (models + adapter_info) |
| POST | `/admin/v1/routing/simulate` | What-If routing simulation |
| GET | `/admin/v1/providers/{id}/discover` | Discover models from provider endpoint |
| GET | `/admin/v1/events` | SSE event stream |
| GET/POST/PUT | `/admin/v1/tsdb/*` | TSDB query, metrics list, prune, retention |
| GET/POST/PATCH/DELETE | `/admin/v1/apikeys[/{id}]` | API key CRUD |
| GET | `/admin/v1/workflows[/{id}]` | Temporal workflow visibility |

### Other endpoints

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/healthz` | Health check (checks adapter + model count) |
| GET | `/metrics` | Prometheus metrics |
| GET | `/admin/` | Admin dashboard UI |
| GET | `/docs/` | mdBook documentation |
| GET | `/` | Redirects to `/admin/` |

### Model ID handling

Model IDs can contain slashes (e.g., `Qwen/Qwen2.5-Coder-32B-Instruct`). The PATCH and DELETE model routes use Chi's wildcard `*` parameter (not `{id}`) to capture the full path. The `wildcardID()` helper in `handlers_admin.go` extracts the ID by trimming the leading `/`. **Do not** use `encodeURIComponent()` for model IDs in URLs — send the literal slashes.

## Debugging

### Sending a test request

```bash
# Create an API key
curl -X POST http://localhost:8080/admin/v1/apikeys \
  -H 'Content-Type: application/json' \
  -d '{"name":"test","scopes":"[\"chat\"]"}'

# Send a request (OpenAI-compatible)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}'
```

### Checking observability

```bash
# Request log (most recent)
curl http://localhost:8080/admin/v1/logs?limit=1

# Stats (all rolling windows, includes token counts)
curl http://localhost:8080/admin/v1/stats

# Prometheus metrics (check token counters)
curl http://localhost:8080/metrics | grep tokenhub_tokens

# TSDB metrics list
curl http://localhost:8080/admin/v1/tsdb/metrics

# SSE event stream (live)
curl -N http://localhost:8080/admin/v1/events

# Health status
curl http://localhost:8080/admin/v1/health
```

### Common issues

1. **"no eligible models registered"**: Either no adapters are registered (check `TOKENHUB_*_API_KEY` env vars), or the only adapter's provider is in health cooldown. Check `/admin/v1/health` for cooldown state.

2. **Health prober uses stale endpoint**: The prober is initialized once at startup. If you PATCH a provider's `base_url`, the adapter is re-registered but the prober keeps probing the old endpoint. Restart the container to fix.

3. **Tokens show 0 for streaming requests**: Streaming responses (`stream: true`) don't include a `usage` block in most providers. Token counts will be 0 for streamed requests.

4. **TSDB query returns empty right after requests**: The TSDB write buffer flushes every 30 seconds. Wait or call the flush endpoint if one exists.

5. **Model IDs with slashes fail on PATCH/DELETE**: Use the wildcard routes (`/admin/v1/models/*`) and do NOT URL-encode the slashes. The UI sends literal `/` characters.

6. **Cost is $0 for all requests**: Check that models have `input_per_1k` and `output_per_1k` set to non-zero values. The default models registered in `registerDefaultModels()` have pricing, but models registered via `bootstrap.local` or credentials file may have 0.

7. **Vault is locked after restart**: The vault salt is persisted in the database, but the master password is not stored anywhere. You must unlock the vault via the UI or API after every restart.

## Testing

```bash
make test              # All unit tests (24 packages, ~33 test files)
make test-race         # With race detector
make test-coverage     # With coverage report
make test-integration  # Integration tests against running container
make test-e2e          # End-to-end Temporal tests
```

Test files are co-located with source (`*_test.go`). Key test files:
- `internal/httpapi/handlers_test.go` (1547 lines) — comprehensive HTTP handler tests
- `internal/httpapi/handlers_extended_test.go` (1625 lines) — extended handler tests
- `internal/temporal/workflows_test.go` — Temporal workflow tests with mock environments

## Codebase Stats

- **~25,200 lines of Go** across 52 source files and 33 test files
- **~1,240 lines of HTML/JS** in the admin dashboard
- **24 Go packages** under `internal/`
- **3 provider adapters** (OpenAI, Anthropic, vLLM)
- **6 observability sinks** (Prometheus, Store, EventBus/SSE, Stats, TSDB, Budget)

## Dependencies (key ones)

| Dependency | Purpose |
|------------|---------|
| `github.com/go-chi/chi/v5` | HTTP router |
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `golang.org/x/crypto` | Argon2id for vault key derivation |
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `go.temporal.io/sdk` | Temporal workflow SDK |
| `go.opentelemetry.io/otel` | OpenTelemetry tracing |

## Release Process

```bash
make release          # Patch bump (v0.2.5 → v0.2.6)
make release-minor    # Minor bump (v0.2.6 → v0.3.0)
make release-major    # Major bump (v0.3.0 → v1.0.0)
```

The `scripts/release.sh` script:
1. Ensures clean working tree
2. Bumps the version tag
3. Builds the Docker image
4. Tags for GHCR
5. Pushes images to `ghcr.io/jordanhubbard/tokenhub`
6. Creates a git tag and pushes it

## Local Development Workflow

```bash
# First time setup
make setup                  # Fix Docker CLI issues (macOS)
cp bootstrap.local.example bootstrap.local  # Edit with your API keys
cp .env.example .env        # Edit with your tokens

# Build and run
make run                    # Builds image, starts stack, runs bootstrap, tails logs

# After code changes, rebuild
make docker                 # Rebuild image
docker compose down tokenhub && docker compose up -d tokenhub

# Or use the Makefile build for faster iteration (creates bin/tokenhub)
make build
```

## Past Bug Fixes (for context)

### v0.2.6+ audit fixes

1. **`extractUsage` false positive on OpenAI format**: The OpenAI JSON parser would succeed even when the `usage` block had zero-valued `prompt_tokens`/`completion_tokens` fields, preventing fallthrough to the Anthropic parser. Fixed by requiring at least one token count to be non-zero before accepting a parse. Same fix applied to the duplicate `extractProviderUsage` in `internal/temporal/activities.go`.

2. **Stats collector lock gap**: `Summary()`, `Global()`, and `SummaryByProvider()` called `Prune()` (write lock), released it, then acquired a read lock — creating a window where data could change. Fixed with `snapshotsAfterPrune()` that atomically prunes and copies the snapshot slice under a single write lock.

3. **Missing `/v1/models` endpoint**: OpenAI SDK clients expect `GET /v1/models`. The scope mapping existed in `routeToScope()` but no handler was mounted. Added `ModelsListPublicHandler` returning an OpenAI-compatible model list.

4. **Missing `Content-Type` on `/healthz`**: The health endpoint returned JSON without the `application/json` Content-Type header.

5. **Timestamp precision loss**: `ListRequestLogs` and other SQLite read paths used `time.Parse(time.RFC3339, ...)` which truncates sub-second precision. Go's `time.Now()` produces nanosecond timestamps stored as RFC3339Nano strings. Added `parseTime()` helper that tries RFC3339Nano first.

6. **docker-compose vLLM endpoint mismatch**: The compose file had `TOKENHUB_VLLM_ENDPOINTS=http://vllm-1:8000` (mock nginx) while the real endpoint was `http://ollama-server.hrd.nvidia.com:8000`. Since the adapter is created from the env var at startup, the prober was probing the wrong host. Updated compose to pass through from host environment.

7. **Persisted providers not restored on restart**: Providers registered via the admin API or `bootstrap.local` had their DB records preserved but no runtime adapters were created at startup. Only env-var providers (`registerProviders`) got adapters. Added `loadPersistedProviders()` in `server.go` that reads provider records from the DB and creates adapters before the health prober starts, so persisted providers survive restarts.

8. **Makefile bootstrap health check wrong port**: The `make bootstrap` target checked `http://localhost:8080/healthz` but docker-compose maps host port 8090 to container port 8080. Made the port configurable via `TOKENHUB_PORT` (default 8090).

9. **Provider upsert defaults `enabled` to `false`**: The `ProvidersUpsertHandler` decoded the JSON request into a `ProviderUpsertRequest` struct. When the JSON omitted the `enabled` field, Go's zero-value `false` was stored in the DB, overwriting previously-enabled providers. Fixed by defaulting `req.Enabled = true` before JSON decode.

## Architecture Decisions

- **Pure-Go SQLite** (`modernc.org/sqlite`) avoids CGO, enabling static builds and scratch/Alpine containers
- **Single-binary server** — no sidecar processes needed; Temporal and OTel are opt-in
- **Embedded UI** via `go:embed` — no separate frontend build step or asset server
- **Provider response treated as opaque `json.RawMessage`** — the router doesn't parse responses; it's the handler layer that extracts usage data for observability
- **Health prober includes persisted providers** — `loadPersistedProviders()` runs before the prober starts, so both env-var and DB-stored providers are probed from boot
- **Thompson Sampling parameters** are refreshed from the reward_logs table every 5 minutes by a background goroutine
- **Idempotency** is enforced via an in-memory cache with 5-minute TTL and 10k max entries
