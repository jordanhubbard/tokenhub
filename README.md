# TokenHub

An LLM routing proxy that routes, arbitrates, and orchestrates requests across multiple providers to optimize cost, latency, reliability, and model quality.

## Feature Highlights

- **Multi-objective routing engine** -- weighted model selection balancing cost, latency, failure rate, and model quality across OpenAI, Anthropic, and vLLM providers
- **Thompson Sampling** -- contextual bandit (Beta-distributed) policy for reinforcement-learning-based model selection, with automatic parameter refresh from reward logs
- **Orchestration modes** -- adversarial (plan/critique/refine), vote (fan-out to N models with judge), and refine (iterative self-improvement)
- **Encrypted credential vault** -- AES-256-GCM with Argon2id key derivation, auto-lock on inactivity, and password rotation
- **API key management** -- generate, rotate, revoke, scope-based access control (`chat`, `plan`), per-key monthly budget enforcement
- **Temporal workflow engine** -- optional durable execution with circuit breaker fallback to direct engine calls
- **SSE streaming** -- native server-sent event streaming through all provider adapters
- **Admin UI** -- single-page dashboard with setup wizard, real-time flow graph (Cytoscape.js), cost/latency charts (D3.js), what-if simulator, live decision feed, and full CRUD management panels
- **CLI tool** -- `tokenhubctl` for scriptable administration from the command line
- **In-band directives** -- `@@tokenhub` annotations in messages to override routing policy per-request
- **Output shaping** -- response format control (json/markdown/text), `<think>` block stripping, token truncation, and JSON schema validation
- **Observability** -- Prometheus metrics, embedded TSDB, structured logging, health tracking, SSE event bus, audit logs, and request logs
- **Idempotency** -- automatic request deduplication via `Idempotency-Key` header
- **Hot reload** -- send SIGHUP to reload configuration without restarting
- **External token injection** -- `~/.tokenhub/credentials` file for declarative, git-safe secret management

## Architecture Overview

TokenHub sits between clients and LLM providers as a reverse proxy. Its core components are:

**Routing Engine** -- The central decision-maker. For each incoming request, it estimates token count, filters eligible models by budget/latency/context-window/health constraints, then scores them using a multi-objective function with mode-specific weight profiles (`cheap`, `normal`, `high_confidence`, `planning`, `adversarial`). When Thompson Sampling is enabled, it replaces the deterministic scorer with probabilistic Beta distribution sampling.

**Provider Adapters** -- Pluggable adapters for OpenAI, Anthropic, and vLLM translate the provider-agnostic request envelope into provider-specific API calls. Each adapter classifies errors (context overflow, rate limited, transient, fatal) to drive failover and escalation. The vLLM adapter supports round-robin across multiple endpoints.

**Health Tracker** -- Monitors provider availability in real time. Tracks consecutive errors, transitions providers through healthy/degraded/down states, and enforces cooldown periods. Feeds latency and error-rate data back into the routing scorer.

**Temporal Workflows** (optional) -- When enabled, every chat and orchestration request is dispatched as a durable Temporal workflow. Activities handle model selection, provider calls, error escalation, and result logging. A circuit breaker automatically falls back to direct engine calls if Temporal becomes unavailable.

**Vault** -- Encrypted at-rest storage for provider API keys. Uses AES-256-GCM with Argon2id-derived keys. The vault starts locked and must be unlocked via the admin UI or API. It auto-locks after 30 minutes of inactivity.

**Event Bus** -- In-memory pub/sub system that broadcasts routing events (success, error, escalation, health changes, workflow lifecycle) to SSE subscribers and the admin UI in real time.

**Admin UI** -- Embedded single-page application served at `/admin` with panels for vault management, provider configuration, model registry, routing policy, health status, request/audit logs, API key management, reward data, and Temporal workflow visibility. Features a multi-step setup wizard, model discovery, what-if routing simulator, and real-time SSE decision feed.

**tokenhubctl** -- Command-line interface for all admin operations. Covers vault, providers, models, routing, API keys, logs, events, and diagnostics. Useful for scripting and CI/CD pipelines.

**TSDB** -- Lightweight embedded time-series database for latency, cost, and throughput metrics with configurable retention and pruning.

```
Client --> /v1/chat/completions or /v1/chat or /v1/plan
              |
        [Rate Limiter] --> [Idempotency Cache] --> [API Key Auth + Budget Check]
              |
        [Directive Parser] --> [Routing Engine]
              |                       |
        [Temporal Workflow]    [Direct Engine]
              |                       |
        +----------+----------+----------+
        |          |          |          |
     OpenAI   Anthropic    vLLM      (more)
        |          |          |
   [Health Tracker + Metrics + TSDB + Event Bus + Audit]
```

## Quick Start

Only **Docker** and **Make** are required on the host. All build tools run inside containers.

### 1. Start the server

```bash
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub
docker compose up -d tokenhub
```

TokenHub is now listening on `http://localhost:8090`. The admin UI is at `http://localhost:8090/admin`.

### 2. Register providers

A freshly started TokenHub has no providers. You can add any LLM endpoint
that speaks the OpenAI, Anthropic, or vLLM protocol — this includes NVIDIA NIM,
Azure OpenAI, Together AI, Groq, Fireworks, Mistral, local Ollama, and more.

The recommended approach is `~/.tokenhub/credentials` — a declarative JSON file
that seeds providers and models at startup. It lives outside the source tree,
requires `0600` permissions, and persists entries to the database on first boot:

```bash
mkdir -p ~/.tokenhub && chmod 700 ~/.tokenhub
cat > ~/.tokenhub/credentials << 'EOF'
{
  "providers": [
    {"id": "my-provider", "type": "openai", "base_url": "https://api.example.com", "api_key": "sk-..."}
  ],
  "models": [
    {"id": "my-model", "provider_id": "my-provider", "weight": 8, "max_context_tokens": 128000}
  ]
}
EOF
chmod 600 ~/.tokenhub/credentials
make run     # builds image, starts compose, tails logs
```

You can also register providers interactively via `tokenhubctl`, the admin API,
or the admin UI's setup wizard. See the [Quick Start guide](docs/src/quickstart.md) for all options.

Providers and models persist in the database and are restored automatically on restart.

### 3. Send a request

```bash
# Create an API key
tokenhubctl apikey create '{"name":"test","scopes":"[\"chat\"]"}'

# Send a chat request
curl -X POST http://localhost:8090/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_..." \
  -d '{"request":{"messages":[{"role":"user","content":"Hello!"}]}}'
```

## Configuration Reference

TokenHub is configured entirely via environment variables. See `.env.example` for the full list.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_LISTEN_ADDR` | `:8080` | HTTP listen address (binds all interfaces by default) |
| `TOKENHUB_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `TOKENHUB_DB_DSN` | `/data/tokenhub.sqlite` | SQLite database path |

### Vault

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_VAULT_ENABLED` | `true` | Enable encrypted credential vault |
| `TOKENHUB_VAULT_PASSWORD` | | Auto-unlock vault at startup (headless mode) |

### Credentials

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_CREDENTIALS_FILE` | `~/.tokenhub/credentials` | Path to external credentials JSON file |

Providers are registered at runtime via `~/.tokenhub/credentials`, the admin API, `tokenhubctl`, or the admin UI. See [Provider Management](docs/src/admin/providers.md).

### Routing Defaults

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_DEFAULT_MODE` | `normal` | Default routing mode (cheap, normal, high_confidence, planning, adversarial, thompson) |
| `TOKENHUB_DEFAULT_MAX_BUDGET_USD` | `0.05` | Max estimated cost per request (USD) |
| `TOKENHUB_DEFAULT_MAX_LATENCY_MS` | `20000` | Max latency budget per request (ms) |

### Security and Hardening

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_ADMIN_TOKEN` | | Bearer token for `/admin/v1/*` endpoints (required in production) |
| `TOKENHUB_CORS_ORIGINS` | `*` | Comma-separated allowed CORS origins |
| `TOKENHUB_RATE_LIMIT_RPS` | `60` | Requests per second per IP |
| `TOKENHUB_RATE_LIMIT_BURST` | `120` | Burst capacity per IP |
| `TOKENHUB_PROVIDER_TIMEOUT_SECS` | `30` | HTTP timeout for provider calls |

### Temporal Workflows

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal workflow dispatch |
| `TOKENHUB_TEMPORAL_HOST` | `localhost:7233` | Temporal server address |
| `TOKENHUB_TEMPORAL_NAMESPACE` | `tokenhub` | Temporal namespace |
| `TOKENHUB_TEMPORAL_TASK_QUEUE` | `tokenhub-tasks` | Temporal task queue name |

### OpenTelemetry (opt-in)

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_OTEL_ENABLED` | `false` | Enable OpenTelemetry tracing |
| `TOKENHUB_OTEL_ENDPOINT` | `localhost:4318` | OTLP endpoint |
| `TOKENHUB_OTEL_SERVICE_NAME` | `tokenhub` | Service name for traces |

## API Endpoints

### Public Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/chat` | Route a chat completion request to the best-fit model. Supports `stream: true` for SSE. |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions endpoint. Drop-in replacement for the OpenAI API. |
| `POST` | `/v1/plan` | Orchestrated multi-model request (adversarial, vote, or refine mode). |
| `GET` | `/healthz` | Health check. Returns adapter and model counts. |
| `GET` | `/metrics` | Prometheus metrics endpoint. |
| `GET` | `/admin` | Admin UI (single-page application). |
| `GET` | `/docs/` | Rendered mdbook documentation (if built). |

The `/v1/chat` and `/v1/plan` endpoints accept an OpenAI-compatible message format:

```json
{
  "messages": [{"role": "user", "content": "..."}],
  "model_hint": "gpt-4o",
  "stream": false,
  "output_schema": {"type": "object", "required": ["answer"]},
  "parameters": {"temperature": 0.7, "max_tokens": 1024}
}
```

#### OpenAI-Compatible Endpoint

The `/v1/chat/completions` endpoint accepts the standard OpenAI request format, making TokenHub a drop-in replacement for any client that targets the OpenAI API:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKENHUB_API_KEY" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}],
    "temperature": 0.7,
    "max_tokens": 256
  }'
```

Works with the OpenAI Python SDK:

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="your-tokenhub-key")
resp = client.chat.completions.create(model="gpt-4o", messages=[{"role": "user", "content": "Hi"}])
```

The `model` field maps to TokenHub's model hint — if the model is registered, it's selected directly; otherwise the routing engine selects the best available model. Responses use the standard OpenAI format (`id`, `object`, `created`, `model`, `choices`, `usage`). Streaming (`"stream": true`) returns SSE in the standard OpenAI format.

### Admin API Endpoints

All `/admin/v1/*` endpoints require `Authorization: Bearer <TOKENHUB_ADMIN_TOKEN>`.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/admin/v1/vault/unlock` | Unlock the credential vault |
| `POST` | `/admin/v1/vault/lock` | Lock the credential vault |
| `POST` | `/admin/v1/vault/rotate` | Rotate the vault password |
| `POST` | `/admin/v1/providers` | Create or update a provider |
| `GET` | `/admin/v1/providers` | List all providers |
| `PATCH` | `/admin/v1/providers/{id}` | Update a provider (type, base_url, api_key, enabled) |
| `DELETE` | `/admin/v1/providers/{id}` | Delete a provider |
| `GET` | `/admin/v1/providers/{id}/discover` | Discover models from a provider's API |
| `POST` | `/admin/v1/models` | Create or update a model |
| `GET` | `/admin/v1/models` | List all models |
| `PATCH` | `/admin/v1/models/{id}` | Update a model (weight, pricing, context, enabled) |
| `DELETE` | `/admin/v1/models/{id}` | Delete a model |
| `GET` | `/admin/v1/routing-config` | Get current routing policy defaults |
| `PUT` | `/admin/v1/routing-config` | Update routing policy defaults |
| `POST` | `/admin/v1/apikeys` | Create a new API key |
| `GET` | `/admin/v1/apikeys` | List all API keys |
| `POST` | `/admin/v1/apikeys/{id}/rotate` | Rotate an API key |
| `PATCH` | `/admin/v1/apikeys/{id}` | Update an API key (enable/disable, budget) |
| `DELETE` | `/admin/v1/apikeys/{id}` | Delete an API key |
| `GET` | `/admin/v1/workflows` | List Temporal workflows |
| `GET` | `/admin/v1/workflows/{id}` | Describe a workflow |
| `GET` | `/admin/v1/workflows/{id}/history` | Get workflow event history |
| `GET` | `/admin/v1/health` | Provider health stats |
| `GET` | `/admin/v1/stats` | Aggregated request stats |
| `GET` | `/admin/v1/logs` | Request logs |
| `GET` | `/admin/v1/audit` | Audit trail |
| `GET` | `/admin/v1/rewards` | Contextual bandit reward logs |
| `GET` | `/admin/v1/engine/models` | Models as seen by the routing engine (includes adapter_info) |
| `POST` | `/admin/v1/routing/simulate` | What-if routing simulation |
| `GET` | `/admin/v1/tsdb/query` | Query the embedded time-series database |
| `GET` | `/admin/v1/tsdb/metrics` | List available TSDB metrics |
| `POST` | `/admin/v1/tsdb/prune` | Prune old TSDB data |
| `PUT` | `/admin/v1/tsdb/retention` | Set TSDB retention policy |
| `GET` | `/admin/v1/events` | SSE stream of real-time routing events |

### In-Band Directives

Clients can override routing policy by embedding `@@tokenhub` directives in message content. These are stripped before forwarding to providers.

Single-line format:
```
@@tokenhub mode=cheap budget=0.01 latency=5000
```

Block format:
```
@@tokenhub
mode=high_confidence
min_weight=80
output_schema={"type":"object","required":["answer"]}
@@end
```

Supported keys: `mode`, `budget`, `latency`, `min_weight`, `output_schema`.

## Admin UI

The admin dashboard is served at `/admin` as an embedded single-page application. Accessing the root URL (`http://host:port/`) automatically redirects to `/admin/`. It includes:

- **Setup Wizard** -- Multi-step guided onboarding for adding new providers (type, endpoint, key, test connection, discover models)
- **Flow Graph** -- Real-time visualization of request routing through providers (Cytoscape.js) with latency-colored edges and throughput-sized nodes
- **Cost and Latency Charts** -- Multi-series D3.js trend charts broken down per model
- **What-If Simulator** -- Test model selection with custom routing parameters without sending live requests
- **SSE Decision Feed** -- Live log of every routing decision with model, provider, latency, cost, and reason
- **Model Leaderboard** -- Sortable table of models with weight adjustment sliders
- **Vault Panel** -- Lock/unlock vault, rotate password, with distinct first-time setup vs. unlock flows
- **Providers Panel** -- Full CRUD: add via wizard, edit inline, discover models, delete. Shows both store-persisted and runtime-configured providers
- **Models Panel** -- Full CRUD: add, edit (weight, pricing, context, enabled), delete. Shows both store and engine models
- **Routing Config Panel** -- Adjust default routing mode, budget, and latency caps
- **Health Panel** -- Provider health states, latency, error rates, cooldown timers
- **Request Logs** -- Searchable history of all routed requests
- **Audit Logs** -- Trail of all admin operations
- **API Keys Panel** -- Create, rotate, enable/disable, set budgets and scopes
- **Rewards Panel** -- Contextual bandit reward data for Thompson Sampling analysis
- **Workflows Panel** -- Temporal workflow list and detail views (when Temporal is enabled)

## tokenhubctl

`tokenhubctl` is a command-line tool for managing TokenHub. It covers all admin API operations and is useful for scripting, automation, and quick diagnostics.

### Installation

```bash
make build    # Builds both tokenhub and tokenhubctl to bin/
```

### Configuration

```bash
export TOKENHUB_URL="http://localhost:8080"       # default
export TOKENHUB_ADMIN_TOKEN="your-admin-token"    # if admin auth is configured
```

### Usage

```bash
# Server status
tokenhubctl status

# Vault operations
tokenhubctl vault unlock "my-password"
tokenhubctl vault lock
tokenhubctl vault rotate "old-password" "new-password"

# Provider management
tokenhubctl provider list
tokenhubctl provider add '{"id":"openai","type":"openai","base_url":"https://api.openai.com","api_key":"sk-..."}'
tokenhubctl provider edit openai '{"base_url":"https://api.openai.com/v2"}'
tokenhubctl provider delete openai
tokenhubctl provider discover openai

# Model management
tokenhubctl model list
tokenhubctl model add '{"id":"gpt-4o","provider_id":"openai","weight":8,"max_context_tokens":128000,"input_per_1k":0.0025,"output_per_1k":0.01,"enabled":true}'
tokenhubctl model edit gpt-4o '{"weight":9}'
tokenhubctl model enable gpt-4o
tokenhubctl model disable gpt-4o
tokenhubctl model delete gpt-4o-legacy

# Routing
tokenhubctl routing get
tokenhubctl routing set '{"default_mode":"cheap","default_max_budget_usd":0.02}'

# API keys
tokenhubctl apikey list
tokenhubctl apikey create '{"name":"my-app","scopes":"[\"chat\",\"plan\"]"}'
tokenhubctl apikey rotate <id>
tokenhubctl apikey delete <id>

# Observability
tokenhubctl health
tokenhubctl stats
tokenhubctl logs --limit 20
tokenhubctl audit --limit 20
tokenhubctl engine models
tokenhubctl events          # live SSE stream

# Routing simulation
tokenhubctl simulate '{"mode":"cheap","token_count":500}'
```

## Build Targets

All build operations run inside Docker containers via Make. No host Go installation is required.

| Target | Description |
|--------|-------------|
| `make build` | Build `tokenhub` and `tokenhubctl` to `bin/` |
| `make run` | Build Docker image, start via `docker compose up`, tail logs |
| `make test` | Run unit tests |
| `make test-race` | Run tests with Go race detector |
| `make test-coverage` | Run tests with coverage report (`coverage.out`) |
| `make test-integration` | Run integration tests against Docker image |
| `make test-e2e` | Run end-to-end Temporal workflow tests |
| `make vet` | Run `go vet` |
| `make lint` | Run golangci-lint |
| `make package` | Build production Docker image |
| `make docs` | Build HTML documentation (mdbook) |
| `make docs-serve` | Serve docs with live reload on port 3000 |
| `make clean` | Remove `bin/`, `docs/book/`, and `coverage.out` |

## Release Process

Releases are managed via `scripts/release.sh`, which bumps the version tag and builds the Docker image.

```bash
make release          # Bump patch version (x.y.Z)
make release-minor    # Bump minor version (x.Y.0)
make release-major    # Bump major version (X.0.0)
```

For non-interactive CI usage:

```bash
BATCH=yes make release
```

## Development Setup

### Using Docker (recommended)

No local Go installation is needed. All tools run in containers:

```bash
make build    # Compile the binaries
make test     # Run the test suite
make lint     # Run linter
```

### Using a Local Go Toolchain

Requires Go 1.24+.

```bash
go build -o bin/tokenhub ./cmd/tokenhub
go build -o bin/tokenhubctl ./cmd/tokenhubctl
go test ./...
go vet ./...
```

### Project Layout

```
cmd/
  tokenhub/        Application entry point
  tokenhubctl/     CLI administration tool
internal/
  app/             Server wiring and configuration
  httpapi/         HTTP handlers and route mounting
  router/          Routing engine, scoring, Thompson Sampling, directives, output shaping
  providers/       Provider adapter interface and shared HTTP utilities
    openai/        OpenAI adapter
    anthropic/     Anthropic adapter
    vllm/          vLLM adapter (round-robin)
  vault/           Encrypted credential storage
  apikey/          API key generation, validation, rotation, budget enforcement
  temporal/        Temporal workflow and activity definitions
  health/          Provider health tracking and probing
  metrics/         Prometheus metric definitions
  events/          In-memory pub/sub event bus
  tsdb/            Embedded time-series database
  stats/           Aggregated statistics collector
  store/           SQLite persistence layer
  circuitbreaker/  Circuit breaker for Temporal dispatch
  idempotency/     Request deduplication cache and middleware
web/               Admin UI static assets (HTML, JS, CSS)
docs/              mdbook documentation source
tests/             Integration and end-to-end test scripts
scripts/           Operational scripts (release, backup)
deploy/            Deployment artifacts (Prometheus alerts, etc.)
```

### Configuration Hot Reload

Send SIGHUP to the running process to reload environment variables without restarting:

```bash
kill -HUP $(pidof tokenhub)
```

## Production Deployment

Key steps for production:

1. Set `TOKENHUB_ADMIN_TOKEN` to a strong, random value
2. Set `TOKENHUB_CORS_ORIGINS` to your allowed domain(s)
3. Place behind a TLS-terminating reverse proxy (nginx, Caddy, etc.)
4. Mount a persistent volume for the SQLite database at `/data`
5. Configure Prometheus to scrape `/metrics`
6. Set up alerting with `deploy/prometheus-alerts.yml`
7. Schedule database backups with `scripts/backup.sh`

## License

MIT (see [`LICENSE`](LICENSE))
