# Provider Management

Providers are the LLM services that TokenHub routes requests to. TokenHub ships with adapter support for OpenAI, Anthropic, and vLLM (OpenAI-compatible).

## Registration Methods

### Credentials File

The `~/.tokenhub/credentials` file is read at startup and can register providers and models. This is the recommended approach for managing secrets outside of environment variables. The file must have `0600` permissions.

```json
{
  "providers": [
    {
      "id": "openai",
      "type": "openai",
      "endpoint": "https://api.openai.com",
      "api_key": "sk-..."
    },
    {
      "id": "anthropic",
      "type": "anthropic",
      "endpoint": "https://api.anthropic.com",
      "api_key": "sk-ant-..."
    }
  ],
  "models": [
    {
      "id": "gpt-4o",
      "provider_id": "openai",
      "weight": 8,
      "max_context_tokens": 128000,
      "input_per_1k": 0.0025,
      "output_per_1k": 0.01,
      "enabled": true
    }
  ]
}
```

Override the default path with `TOKENHUB_CREDENTIALS_FILE`.

### bootstrap.local

A git-ignored shell script that configures a running instance via the admin API after startup. Ideal for development and staging environments:

```bash
cp bootstrap.local.example bootstrap.local
chmod +x bootstrap.local
# Edit with your provider keys
make run   # Automatically runs bootstrap.local after healthz passes
```

### Admin API

Providers can be registered and managed dynamically via the admin API or `tokenhubctl`.

## API Operations

### Create or Update a Provider

```bash
curl -X POST http://localhost:8080/admin/v1/providers \
  -H "Content-Type: application/json" \
  -d '{
    "id": "openai-prod",
    "type": "openai",
    "enabled": true,
    "base_url": "https://api.openai.com",
    "cred_store": "vault",
    "api_key": "sk-..."
  }'
```

Or with `tokenhubctl`:

```bash
tokenhubctl provider add '{"id":"openai-prod","type":"openai","base_url":"https://api.openai.com","api_key":"sk-..."}'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique provider identifier |
| `type` | string | Yes | Provider type: `openai`, `anthropic`, or `vllm` |
| `enabled` | bool | No | Whether the provider is active (default: true) |
| `base_url` | string | Yes | Provider API base URL |
| `cred_store` | string | No | Where to store credentials: `env`, `vault`, or `none` |
| `api_key` | string | No | API key (stored according to `cred_store`) |

### List Providers

```bash
curl http://localhost:8080/admin/v1/providers
tokenhubctl provider list
```

The `tokenhubctl provider list` command merges providers from both the persistent store and the runtime engine, showing base URLs derived from adapter health endpoints and indicating whether each provider is store-persisted or runtime-only.

API keys are never returned in list responses.

### Edit a Provider

Partial updates via PATCH:

```bash
curl -X PATCH http://localhost:8080/admin/v1/providers/openai \
  -H "Content-Type: application/json" \
  -d '{"base_url": "https://api.openai.com", "enabled": true}'
```

Or:

```bash
tokenhubctl provider edit openai '{"base_url":"https://api.openai.com","enabled":true}'
```

Patchable fields: `type`, `base_url`, `enabled`, `api_key`, `cred_store`.

### Delete a Provider

```bash
curl -X DELETE http://localhost:8080/admin/v1/providers/openai-staging
tokenhubctl provider delete openai-staging
```

### Discover Models

Query a provider's API to discover available models:

```bash
curl http://localhost:8080/admin/v1/providers/openai/discover
tokenhubctl provider discover openai
```

This calls the provider's `/v1/models` endpoint (using the stored API key from the vault if available) and returns the list of models with a `registered` flag indicating which are already configured in TokenHub.

## Credential Storage Options

| `cred_store` | Description |
|--------------|-------------|
| `vault` | API key is encrypted and stored in the vault (recommended) |
| `none` | No credentials needed (e.g., local vLLM without auth) |

When using `vault`, the API key is encrypted with AES-256-GCM and only available when the vault is unlocked.

## Supported Provider Types

### OpenAI (`openai`)

- **API endpoint**: `/v1/chat/completions`
- **Health probe**: `GET /v1/models`
- **Streaming**: SSE (native)
- **Authentication**: `Authorization: Bearer <key>`

### Anthropic (`anthropic`)

- **API endpoint**: `/v1/messages`
- **Health probe**: `GET /v1/messages` (405 response = healthy)
- **Streaming**: SSE (native)
- **Authentication**: `x-api-key: <key>`, `anthropic-version: 2023-06-01`

### vLLM (`vllm`)

- **API endpoint**: `/v1/chat/completions` (OpenAI-compatible)
- **Health probe**: `GET /health`
- **Streaming**: SSE (OpenAI-compatible)
- **Authentication**: None (or custom header if configured)
- **Multi-endpoint**: Supports multiple endpoints with round-robin load balancing

## Audit Trail

All provider mutations are logged in the audit trail:
- `provider.upsert` — Provider created or updated
- `provider.patch` — Provider partially updated
- `provider.delete` — Provider removed
