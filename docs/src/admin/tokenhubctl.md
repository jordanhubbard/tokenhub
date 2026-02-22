# tokenhubctl CLI

`tokenhubctl` is the command-line interface for managing TokenHub. It wraps every admin API endpoint into a convenient, scriptable tool.

## Installation

`tokenhubctl` is built alongside `tokenhub`:

```bash
make build    # Produces bin/tokenhub and bin/tokenhubctl
```

Or build it directly:

```bash
go build -o bin/tokenhubctl ./cmd/tokenhubctl
```

## Configuration

Two environment variables control how `tokenhubctl` connects to the server:

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKENHUB_URL` | `http://localhost:8080` | TokenHub server URL |
| `TOKENHUB_ADMIN_TOKEN` | — | Bearer token for admin endpoints |

```bash
export TOKENHUB_URL="http://tokenhub.internal:8080"
export TOKENHUB_ADMIN_TOKEN="your-admin-token"
```

## Command Reference

### General

```bash
tokenhubctl status      # Server info, health, vault state
tokenhubctl health      # Provider health table
tokenhubctl version     # CLI version
tokenhubctl help        # Full usage
```

### Vault

```bash
tokenhubctl vault unlock <password>
tokenhubctl vault lock
tokenhubctl vault rotate <old-password> <new-password>
```

### Providers

```bash
tokenhubctl provider list
tokenhubctl provider add '<json>'
tokenhubctl provider edit <id> '<json>'
tokenhubctl provider delete <id>
tokenhubctl provider discover <id>
```

The `list` command merges providers from both the persistent store and the runtime engine, showing the source of each.

The `discover` command queries a provider's `/v1/models` endpoint to list available models and whether each is already registered in TokenHub.

Example:

```bash
# Add a new provider
tokenhubctl provider add '{
  "id": "openai",
  "type": "openai",
  "base_url": "https://api.openai.com",
  "api_key": "sk-..."
}'

# Update its base URL
tokenhubctl provider edit openai '{"base_url":"https://api.openai.com"}'

# Discover available models
tokenhubctl provider discover openai
```

### Models

```bash
tokenhubctl model list
tokenhubctl model add '<json>'
tokenhubctl model edit <id> '<json>'
tokenhubctl model delete <id>
tokenhubctl model enable <id>
tokenhubctl model disable <id>
```

Model IDs can contain slashes (e.g., `Qwen/Qwen2.5-Coder-32B-Instruct`). The CLI handles them correctly.

Example:

```bash
# Add a model
tokenhubctl model add '{
  "id": "gpt-4o",
  "provider_id": "openai",
  "weight": 8,
  "max_context_tokens": 128000,
  "input_per_1k": 0.0025,
  "output_per_1k": 0.01,
  "enabled": true
}'

# Adjust its weight
tokenhubctl model edit gpt-4o '{"weight": 9}'

# Temporarily disable it
tokenhubctl model disable gpt-4o
```

### Routing

```bash
tokenhubctl routing get
tokenhubctl routing set '<json>'
```

Example:

```bash
tokenhubctl routing set '{"default_mode":"cheap","default_max_budget_usd":0.02,"default_max_latency_ms":10000}'
```

### API Keys

```bash
tokenhubctl apikey list
tokenhubctl apikey create '<json>'
tokenhubctl apikey rotate <id>
tokenhubctl apikey edit <id> '<json>'
tokenhubctl apikey delete <id>
```

The `create` command prints the API key exactly once. Save it immediately.

Example:

```bash
tokenhubctl apikey create '{"name":"prod-app","scopes":"[\"chat\",\"plan\"]","monthly_budget_usd":50.0}'
```

### Observability

```bash
tokenhubctl logs [--limit N]       # Request logs
tokenhubctl audit [--limit N]      # Audit trail
tokenhubctl rewards [--limit N]    # Thompson Sampling reward data
tokenhubctl stats                  # Aggregated statistics
tokenhubctl engine models          # Runtime model registry and adapter info
tokenhubctl events                 # Live SSE event stream (Ctrl-C to stop)
```

### Routing Simulation

Run a what-if simulation without sending a real request:

```bash
tokenhubctl simulate '{"mode":"cheap","token_count":500}'
tokenhubctl simulate '{"mode":"high_confidence","token_count":2000,"max_budget_usd":0.10}'
```

### TSDB

```bash
tokenhubctl tsdb metrics
tokenhubctl tsdb query metric=latency&model_id=gpt-4o&step_ms=60000
tokenhubctl tsdb prune
```

## Output Format

Most commands produce human-readable tabular output. For programmatic use, pipe JSON responses directly from `curl` or parse `tokenhubctl` output with standard text tools.
