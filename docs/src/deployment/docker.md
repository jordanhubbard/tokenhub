# Docker & Compose

TokenHub provides a Dockerfile for container builds and a Docker Compose file for local development with all dependencies.

## Docker Image

### Build

```bash
make docker
# or
docker build -t tokenhub .
```

The Dockerfile uses a multi-stage build:
1. **Build stage**: `golang:1.24-alpine` — compiles the Go binary
2. **Runtime stage**: `gcr.io/distroless/static:nonroot` — minimal secure runtime

The final image is ~15MB and runs as a non-root user.

### Run

```bash
docker run -d \
  -p 8080:8080 \
  -e TOKENHUB_OPENAI_API_KEY="sk-..." \
  -v tokenhub_data:/data \
  tokenhub
```

The container expects:
- **Port 8080**: HTTP server
- **Volume `/data`**: SQLite database persistence

## Docker Compose

### Full Stack

```bash
docker compose up -d
```

This starts:

| Service | Port | Description |
|---------|------|-------------|
| `tokenhub` | 8080 | TokenHub server |
| `temporal` | 7233 | Temporal server (gRPC) |
| `temporal-ui` | 8233 | Temporal Web UI |

### Services

#### TokenHub

```yaml
tokenhub:
  build: .
  ports:
    - "8080:8080"
  environment:
    - TOKENHUB_DB_DSN=file:/data/tokenhub.sqlite?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
    - TOKENHUB_OPENAI_API_KEY=${TOKENHUB_OPENAI_API_KEY}
    - TOKENHUB_ANTHROPIC_API_KEY=${TOKENHUB_ANTHROPIC_API_KEY}
    - TOKENHUB_VLLM_ENDPOINTS=${TOKENHUB_VLLM_ENDPOINTS}
    - TOKENHUB_TEMPORAL_ENABLED=true
    - TOKENHUB_TEMPORAL_HOST=temporal:7233
  volumes:
    - tokenhub_data:/data
  depends_on:
    - temporal
```

#### Temporal

```yaml
temporal:
  image: temporalio/auto-setup:latest
  ports:
    - "7233:7233"
  environment:
    - DB=sqlite
  volumes:
    - temporal_data:/etc/temporal/data

temporal-ui:
  image: temporalio/ui:latest
  ports:
    - "8233:8080"
  environment:
    - TEMPORAL_ADDRESS=temporal:7233
```

### Environment File

Create a `.env` file for sensitive values:

```bash
TOKENHUB_OPENAI_API_KEY=sk-...
TOKENHUB_ANTHROPIC_API_KEY=sk-ant-...
TOKENHUB_VLLM_ENDPOINTS=http://vllm-1:8000
```

### Without Temporal

To run without Temporal:

```bash
docker compose up -d tokenhub
```

Or set `TOKENHUB_TEMPORAL_ENABLED=false`.

## Health Check

The Docker health check uses the `/healthz` endpoint:

```bash
curl -f http://localhost:8080/healthz
```

Returns 200 when adapters and models are registered, 503 otherwise.

## Data Persistence

All persistent data is stored in SQLite at the path configured by `TOKENHUB_DB_DSN`. In Docker, mount a volume to `/data`:

```yaml
volumes:
  - tokenhub_data:/data
```

This persists:
- Model and provider configurations
- Vault salt and encrypted credentials
- Request logs, audit logs, reward entries
- API keys
- Routing configuration
- TSDB time-series data

## Resource Requirements

TokenHub is lightweight:
- **Memory**: ~50MB baseline, scales with request concurrency
- **CPU**: Minimal (most time is spent waiting on provider APIs)
- **Disk**: Depends on log retention; ~1MB per 10,000 requests
