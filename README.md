# TokenHub

TokenHub is a containerized **LLM routing proxy** that routes, arbitrates, and orchestrates requests across multiple providers (OpenAI, Anthropic, vLLM) to optimize cost, latency, reliability, and model quality.

## Features

**Routing Engine**
- Weighted model selection with budget, latency, and quality constraints
- Automatic escalation and failover across providers
- Thompson Sampling bandit policy for RL-based model selection
- In-band directive parsing (`@@tokenhub` annotations in messages)

**Orchestration**
- Adversarial mode: plan, critique, refine loops
- Vote mode: fan-out to N models, judge selects best
- Refine mode: iterative improvement pipeline
- Configurable iterations and model hints

**Provider Adapters**
- OpenAI, Anthropic, and vLLM with streaming support
- HTTP timeouts, retry with exponential backoff
- Request parameter forwarding (temperature, top_p, max_tokens, etc.)

**Security**
- AES-256-GCM encrypted credential vault with Argon2id key derivation and auto-lock
- API key management: issue, rotate, revoke, scope-based access control
- Admin endpoint authentication via Bearer token
- Per-IP rate limiting (token bucket)
- Configurable CORS origins

**Observability**
- Prometheus metrics (`/metrics`)
- Embedded time-series database with retention management
- Structured JSON logging with request tracing
- Proactive health checking of provider endpoints
- SSE real-time event stream
- Audit trail for all admin operations

**Admin UI**
- Real-time request flow graph (Cytoscape.js)
- Cost and latency trend charts (D3.js)
- Model leaderboard with weight adjustment sliders
- Panels: vault, providers, routing, health, audit, request logs, API keys, workflows

**Temporal Workflows** (optional)
- Every request dispatched as a durable Temporal workflow
- Parallel activity execution for orchestration modes
- Workflow visibility in admin UI
- Graceful fallback to direct engine calls when Temporal is unavailable

**Infrastructure**
- SQLite with WAL mode for concurrent access
- Docker with multi-stage build (28 MB image) and HEALTHCHECK
- Docker Compose with health checks, resource limits, restart policies
- CI/CD via GitHub Actions (build, lint, test, Docker)
- Comprehensive mdbook documentation served at `/docs/`

## Quick Start

### Docker Compose

```bash
cp .env.example .env
# Edit .env with your provider API keys
docker compose up --build
```

### Run Locally

```bash
export TOKENHUB_OPENAI_API_KEY="sk-..."
make build && make run
```

### Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /v1/chat` | Route a chat request |
| `POST /v1/plan` | Orchestrated multi-model request |
| `GET /healthz` | Health check |
| `GET /admin` | Admin dashboard |
| `GET /admin/v1/*` | Admin API (see [docs](/docs/)) |
| `GET /metrics` | Prometheus metrics |
| `GET /docs/` | Documentation |

## Configuration

TokenHub is configured via environment variables. See [`.env.example`](.env.example) for the full list.

Key variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_OPENAI_API_KEY` | | OpenAI API key |
| `TOKENHUB_ANTHROPIC_API_KEY` | | Anthropic API key |
| `TOKENHUB_VLLM_ENDPOINTS` | | Comma-separated vLLM URLs |
| `TOKENHUB_ADMIN_TOKEN` | | Bearer token for admin endpoints |
| `TOKENHUB_CORS_ORIGINS` | `*` | Allowed CORS origins |
| `TOKENHUB_RATE_LIMIT_RPS` | `60` | Rate limit per IP per second |
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal workflow dispatch |

## Documentation

Build and serve the full documentation:

```bash
make docs          # Build HTML docs
make docs-serve    # Live-reload dev server
```

When running, documentation is served at [`/docs/`](http://localhost:8080/docs/).

## Development

```bash
make build        # Build binary
make test         # Run tests
make test-race    # Run tests with race detector
make vet          # Go vet
make lint         # golangci-lint (if installed)
make docker       # Build Docker image
make clean        # Remove build artifacts
```

## Production Deployment

See the [Production Checklist](docs/src/deployment/production.md) for a complete guide. Key steps:

1. Set `TOKENHUB_ADMIN_TOKEN` to protect admin endpoints
2. Set `TOKENHUB_CORS_ORIGINS` to your domain(s)
3. Place behind a TLS-terminating reverse proxy
4. Mount a persistent volume for SQLite
5. Configure Prometheus to scrape `/metrics`
6. Set up alerting with [`deploy/prometheus-alerts.yml`](deploy/prometheus-alerts.yml)
7. Schedule backups with [`scripts/backup.sh`](scripts/backup.sh)

## Architecture

```
Client --> /v1/chat --> [Rate Limiter] --> [API Key Auth] --> [Routing Engine]
                                                                    |
                                                    [Thompson Sampling Policy]
                                                                    |
                                          +------------+------------+------------+
                                          |            |            |            |
                                       OpenAI     Anthropic      vLLM       (more)
                                          |            |            |
                                     [Health Tracker + Metrics + TSDB + Audit]
```

## License

MIT (see [`LICENSE`](LICENSE))
