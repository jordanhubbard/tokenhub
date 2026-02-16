# Configuration Reference

TokenHub is configured entirely via environment variables. All variables are optional and have sensible defaults.

## Environment Variables

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_LISTEN_ADDR` | `:8080` | HTTP server listen address |
| `TOKENHUB_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `TOKENHUB_DB_DSN` | `file:/data/tokenhub.sqlite` | SQLite database path |
| `TOKENHUB_VAULT_ENABLED` | `true` | Enable encrypted credential vault |
| `TOKENHUB_PROVIDER_TIMEOUT_SECS` | `30` | HTTP timeout for provider API calls |

### Routing Defaults

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_DEFAULT_MODE` | `normal` | Default routing mode |
| `TOKENHUB_DEFAULT_MAX_BUDGET_USD` | `0.05` | Default max cost per request (USD) |
| `TOKENHUB_DEFAULT_MAX_LATENCY_MS` | `20000` | Default max latency (milliseconds) |

### Provider API Keys

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_OPENAI_API_KEY` | — | OpenAI API key (registers OpenAI provider) |
| `TOKENHUB_ANTHROPIC_API_KEY` | — | Anthropic API key (registers Anthropic provider) |
| `TOKENHUB_VLLM_ENDPOINTS` | — | Comma-separated vLLM endpoint URLs |

At least one provider must be configured for TokenHub to route requests.

### Temporal (Optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_TEMPORAL_ENABLED` | `false` | Enable Temporal workflow dispatch |
| `TOKENHUB_TEMPORAL_HOST` | `localhost:7233` | Temporal server host:port |
| `TOKENHUB_TEMPORAL_NAMESPACE` | `tokenhub` | Temporal namespace |
| `TOKENHUB_TEMPORAL_TASK_QUEUE` | `tokenhub-tasks` | Temporal task queue name |

## Example Configuration

### Minimal (OpenAI only)

```bash
export TOKENHUB_OPENAI_API_KEY="sk-..."
./bin/tokenhub
```

### Full Production

```bash
export TOKENHUB_LISTEN_ADDR=":8080"
export TOKENHUB_LOG_LEVEL="info"
export TOKENHUB_DB_DSN="file:/data/tokenhub.sqlite?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
export TOKENHUB_VAULT_ENABLED="true"
export TOKENHUB_PROVIDER_TIMEOUT_SECS="30"

# Routing
export TOKENHUB_DEFAULT_MODE="normal"
export TOKENHUB_DEFAULT_MAX_BUDGET_USD="0.10"
export TOKENHUB_DEFAULT_MAX_LATENCY_MS="30000"

# Providers
export TOKENHUB_OPENAI_API_KEY="sk-..."
export TOKENHUB_ANTHROPIC_API_KEY="sk-ant-..."
export TOKENHUB_VLLM_ENDPOINTS="http://vllm-1:8000,http://vllm-2:8000"

# Temporal (optional)
export TOKENHUB_TEMPORAL_ENABLED="true"
export TOKENHUB_TEMPORAL_HOST="temporal:7233"

./bin/tokenhub
```

## SQLite DSN Options

The database DSN supports SQLite pragmas for tuning:

```
file:/path/to/db.sqlite?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
```

| Pragma | Recommended | Description |
|--------|-------------|-------------|
| `busy_timeout` | 5000 | Wait ms for locks instead of failing |
| `journal_mode` | WAL | Enables concurrent reads during writes |
| `synchronous` | NORMAL | Balance durability and speed |
| `cache_size` | -20000 | 20MB page cache |

## Runtime Configuration

The following settings can be changed at runtime via the admin API without restarting:

- **Routing defaults**: `PUT /admin/v1/routing-config`
- **Models**: `POST/PATCH/DELETE /admin/v1/models`
- **Providers**: `POST/DELETE /admin/v1/providers`
- **API keys**: `POST/PATCH/DELETE /admin/v1/apikeys`
- **TSDB retention**: `PUT /admin/v1/tsdb/retention`
