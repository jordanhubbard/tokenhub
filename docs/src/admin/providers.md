# Provider Management

Providers are the LLM services that TokenHub routes requests to. TokenHub ships with adapter support for OpenAI, Anthropic, and vLLM (OpenAI-compatible).

## Environment Variable Registration

The simplest way to register providers is via environment variables at startup:

```bash
# OpenAI
export TOKENHUB_OPENAI_API_KEY="sk-..."

# Anthropic
export TOKENHUB_ANTHROPIC_API_KEY="sk-ant-..."

# vLLM (one or more comma-separated endpoints)
export TOKENHUB_VLLM_ENDPOINTS="http://vllm-1:8000,http://vllm-2:8000"
```

## API Registration

Providers can also be registered dynamically via the admin API:

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
```

Response:
```json
[
  {
    "id": "openai",
    "type": "openai",
    "enabled": true,
    "base_url": "https://api.openai.com",
    "cred_store": "env"
  },
  {
    "id": "anthropic",
    "type": "anthropic",
    "enabled": true,
    "base_url": "https://api.anthropic.com",
    "cred_store": "vault"
  }
]
```

API keys are never returned in list responses.

### Delete a Provider

```bash
curl -X DELETE http://localhost:8080/admin/v1/providers/openai-staging
```

## Credential Storage Options

| `cred_store` | Description |
|--------------|-------------|
| `env` | API key is provided via environment variable (e.g., `TOKENHUB_OPENAI_API_KEY`) |
| `vault` | API key is encrypted and stored in the vault |
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
- `provider.delete` — Provider removed
