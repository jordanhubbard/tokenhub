# Configuration Reference

TokenHub is configured entirely via environment variables. All variables are optional and have sensible defaults.

## Environment Variables

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_LISTEN_ADDR` | `:8080` | HTTP server listen address (binds all interfaces) |
| `TOKENHUB_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `TOKENHUB_DB_DSN` | `/data/tokenhub.sqlite` | SQLite database path |
| `TOKENHUB_VAULT_ENABLED` | `true` | Enable encrypted credential vault |
| `TOKENHUB_VAULT_PASSWORD` | — | Auto-unlock vault at startup (headless mode) |
| `TOKENHUB_PROVIDER_TIMEOUT_SECS` | `30` | HTTP timeout for provider API calls |

### Routing Defaults

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_DEFAULT_MODE` | `normal` | Default routing mode |
| `TOKENHUB_DEFAULT_MAX_BUDGET_USD` | `0.05` | Default max cost per request (USD) |
| `TOKENHUB_DEFAULT_MAX_LATENCY_MS` | `20000` | Default max latency (milliseconds) |

### Security & Hardening

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_ADMIN_TOKEN` | — | Bearer token for `/admin/v1/*` access (required in production) |
| `TOKENHUB_CORS_ORIGINS` | `*` | Comma-separated allowed CORS origins |
| `TOKENHUB_RATE_LIMIT_RPS` | `60` | Max requests per second per IP |
| `TOKENHUB_RATE_LIMIT_BURST` | `120` | Burst capacity per IP |

### Credentials

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_CREDENTIALS_FILE` | `~/.tokenhub/credentials` | Path to external credentials JSON file |

Providers are registered at startup via `~/.tokenhub/credentials` or at runtime via the admin API, `tokenhubctl`, or the admin UI. At least one provider must be registered for TokenHub to route requests.

### Temporal (Optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal workflow dispatch |
| `TOKENHUB_TEMPORAL_HOST` | `localhost:7233` | Temporal server host:port |
| `TOKENHUB_TEMPORAL_NAMESPACE` | `tokenhub` | Temporal namespace |
| `TOKENHUB_TEMPORAL_TASK_QUEUE` | `tokenhub-tasks` | Temporal task queue name |

### OpenTelemetry (Optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_OTEL_ENABLED` | `false` | Enable OpenTelemetry tracing |
| `TOKENHUB_OTEL_ENDPOINT` | `localhost:4318` | OTLP exporter endpoint |
| `TOKENHUB_OTEL_SERVICE_NAME` | `tokenhub` | Service name for traces |

## External Credentials File

The `~/.tokenhub/credentials` file is the primary mechanism for bootstrapping
providers and models. It is processed at startup — providers are persisted to
the database and API keys are stored in the vault (when `TOKENHUB_VAULT_PASSWORD`
is set). The file must have `0600` permissions.

```json
{
  "providers": [
    {
      "id": "openai",
      "type": "openai",
      "base_url": "https://api.openai.com",
      "api_key": "sk-..."
    },
    {
      "id": "vllm-local",
      "type": "vllm",
      "base_url": "http://localhost:8000"
    }
  ],
  "models": [
    {
      "id": "gpt-4o",
      "provider_id": "openai",
      "weight": 8,
      "max_context_tokens": 128000,
      "input_per_1k": 0.0025,
      "output_per_1k": 0.01
    }
  ]
}
```

The file is idempotent — providers and models are upserted, so it can remain
in place across restarts. `api_key` is optional for keyless providers (vLLM,
Ollama). All providers default to `enabled: true` unless explicitly set to `false`.

## Example Configuration

### Minimal

```bash
./bin/tokenhub
# Then register providers via ~/.tokenhub/credentials, admin API, or UI.
```

### Full Production

```bash
export TOKENHUB_LISTEN_ADDR=":8080"
export TOKENHUB_LOG_LEVEL="info"
export TOKENHUB_DB_DSN="/data/tokenhub.sqlite"
export TOKENHUB_VAULT_ENABLED="true"
export TOKENHUB_PROVIDER_TIMEOUT_SECS="30"

# Security
export TOKENHUB_ADMIN_TOKEN="your-secret-admin-token"
export TOKENHUB_CORS_ORIGINS="https://app.example.com"
export TOKENHUB_RATE_LIMIT_RPS="100"
export TOKENHUB_RATE_LIMIT_BURST="200"

# Routing
export TOKENHUB_DEFAULT_MODE="normal"
export TOKENHUB_DEFAULT_MAX_BUDGET_USD="0.10"
export TOKENHUB_DEFAULT_MAX_LATENCY_MS="30000"

# Temporal (optional)
export TOKENHUB_TEMPORAL_ENABLED="true"
export TOKENHUB_TEMPORAL_HOST="temporal:7233"

# OpenTelemetry (optional)
export TOKENHUB_OTEL_ENABLED="true"
export TOKENHUB_OTEL_ENDPOINT="otel-collector:4318"

./bin/tokenhub
# Providers are loaded from ~/.tokenhub/credentials, or registered via admin API/UI.
```

## Runtime Configuration

The following settings can be changed at runtime via the admin API or `tokenhubctl` without restarting:

- **Routing defaults**: `PUT /admin/v1/routing-config` or `tokenhubctl routing set`
- **Models**: `POST/PATCH/DELETE /admin/v1/models` or `tokenhubctl model add/edit/delete`
- **Providers**: `POST/PATCH/DELETE /admin/v1/providers` or `tokenhubctl provider add/edit/delete`
- **API keys**: `POST/PATCH/DELETE /admin/v1/apikeys` or `tokenhubctl apikey create/edit/delete`
- **TSDB retention**: `PUT /admin/v1/tsdb/retention` or `tokenhubctl tsdb`
