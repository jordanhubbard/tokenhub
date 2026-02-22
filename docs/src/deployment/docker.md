# Docker & Compose

TokenHub provides a Dockerfile for container builds and a Docker Compose file for local development with all dependencies.

## Docker Image

### Build

```bash
make package
# or
docker buildx build --load -t tokenhub .
```

The Dockerfile uses a multi-stage build:
1. **Build stage**: `golang:1.24-alpine` — compiles the Go binary and builds mdbook documentation
2. **Runtime stage**: `alpine:3.21` — lightweight runtime with curl for health checks

The final image runs as a non-root `tokenhub` user.

### Run

```bash
docker run -d \
  -p 8080:8080 \
  -e TOKENHUB_ADMIN_TOKEN="your-admin-token" \
  -v tokenhub_data:/data \
  tokenhub
```

The container expects:
- **Port 8080**: HTTP server (binds all interfaces by default)
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
  image: tokenhub:latest
  ports:
    - "8080:8080"
  environment:
    - TOKENHUB_LISTEN_ADDR=:8080
    - TOKENHUB_DB_DSN=/data/tokenhub.sqlite
    - TOKENHUB_VAULT_ENABLED=true
    - TOKENHUB_VAULT_PASSWORD=${TOKENHUB_VAULT_PASSWORD}
    - TOKENHUB_ADMIN_TOKEN=${TOKENHUB_ADMIN_TOKEN}
  volumes:
    - tokenhub_data:/data
  restart: unless-stopped
```

Set `TOKENHUB_VAULT_PASSWORD` to auto-unlock the vault at startup (headless mode). If not set, unlock interactively via UI or `tokenhubctl`. Providers are registered at runtime via `bootstrap.local`, the admin API, `tokenhubctl`, or the admin UI.

Note: The `TOKENHUB_DB_DSN` should be a plain path (e.g., `/data/tokenhub.sqlite`) when using `modernc.org/sqlite` (the pure-Go driver). SQLite pragmas are applied programmatically, not via DSN query parameters.

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
TOKENHUB_ADMIN_TOKEN=your-secret-admin-token
```

### Without Temporal

To run without Temporal:

```bash
docker compose up -d tokenhub
```

Or set `TOKENHUB_TEMPORAL_ENABLED=false`.

### Bootstrap After Start

The `make run` target starts the container, waits for it to become healthy, and then runs `bootstrap.local` (if present) to configure providers and models via the admin API:

```bash
cp bootstrap.local.example bootstrap.local
# Edit with your secrets
make run
```

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
