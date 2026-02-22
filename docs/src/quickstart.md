# Quick Start

This guide gets TokenHub running and serving your first request in under five minutes.

## Prerequisites

- Docker (for Docker Compose), or Go 1.24+ (for building from source)
- At least one LLM provider endpoint and API key

TokenHub works with any OpenAI-compatible API, the Anthropic API, or vLLM.
This includes services like NVIDIA NIM, Azure OpenAI, Together AI, Groq,
Fireworks, Mistral, local Ollama instances — anything that speaks the
OpenAI `/v1/chat/completions` protocol.

## 1. Start the Server

### Docker Compose (recommended)

```bash
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub
docker compose up -d tokenhub
```

### Build from Source

```bash
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub
make build        # produces bin/tokenhub and bin/tokenhubctl
./bin/tokenhub
```

TokenHub starts on port 8080 by default. Docker Compose maps this to
host port 8090. Adjust the examples below accordingly.

## 2. Register Providers

A freshly started TokenHub has no providers configured. You need to tell
it where your LLM endpoints are. There are several ways to do this, from
a one-time script to the admin UI. Pick whichever fits your workflow.

### Option A: bootstrap.local (recommended for development)

`bootstrap.local` is a git-ignored shell script that runs automatically
after `make run`, or manually via `make bootstrap`. It uses `tokenhubctl`
to register providers, models, and API keys against the running server.

```bash
cp bootstrap.local.example bootstrap.local
chmod +x bootstrap.local
```

Edit it to add your providers. Here's an example registering a local
Ollama instance and an NVIDIA cloud endpoint:

```bash
#!/bin/bash
set -euo pipefail
export TOKENHUB_URL="${TOKENHUB_URL:-http://localhost:8090}"
TOKENHUBCTL="${TOKENHUBCTL:-tokenhubctl}"

# Local Ollama (no API key needed)
$TOKENHUBCTL provider add '{
    "id": "ollama",
    "type": "openai",
    "base_url": "http://localhost:11434",
    "enabled": true
}'

# NVIDIA NIM cloud
$TOKENHUBCTL provider add '{
    "id": "nvidia",
    "type": "openai",
    "base_url": "https://integrate.api.nvidia.com",
    "api_key": "nvapi-...",
    "enabled": true
}'

# Register models on those providers
$TOKENHUBCTL model add '{
    "id": "llama3.1:8b",
    "provider_id": "ollama",
    "weight": 5,
    "max_context_tokens": 8192,
    "input_per_1k": 0.0,
    "output_per_1k": 0.0,
    "enabled": true
}'

$TOKENHUBCTL model add '{
    "id": "meta/llama-3.1-70b-instruct",
    "provider_id": "nvidia",
    "weight": 8,
    "max_context_tokens": 128000,
    "input_per_1k": 0.0003,
    "output_per_1k": 0.0003,
    "enabled": true
}'

$TOKENHUBCTL model list
```

Then start everything:

```bash
make run    # builds image, starts compose, runs bootstrap.local, tails logs
```

### Option B: tokenhubctl (interactive)

With the server already running, use the CLI directly:

```bash
export TOKENHUB_URL="http://localhost:8090"

# Register a provider
tokenhubctl provider add '{
    "id": "openai",
    "type": "openai",
    "base_url": "https://api.openai.com",
    "api_key": "sk-..."
}'

# Register a model on that provider
tokenhubctl model add '{
    "id": "gpt-4o",
    "provider_id": "openai",
    "weight": 8,
    "max_context_tokens": 128000,
    "input_per_1k": 0.0025,
    "output_per_1k": 0.01,
    "enabled": true
}'
```

### Option C: Admin UI

Open [http://localhost:8090/admin](http://localhost:8090/admin) in your browser.
The setup wizard walks you through adding your first provider: select the type,
enter the base URL and API key, test the connection, then discover and register
available models — all without touching the command line.

### Option D: Admin API (curl)

```bash
# Register a provider
curl -X POST http://localhost:8090/admin/v1/providers \
  -H "Content-Type: application/json" \
  -d '{
    "id": "anthropic",
    "type": "anthropic",
    "base_url": "https://api.anthropic.com",
    "api_key": "sk-ant-...",
    "enabled": true
  }'

# Register a model
curl -X POST http://localhost:8090/admin/v1/models \
  -H "Content-Type: application/json" \
  -d '{
    "id": "claude-sonnet-4-5-20250514",
    "provider_id": "anthropic",
    "weight": 8,
    "max_context_tokens": 200000,
    "input_per_1k": 0.003,
    "output_per_1k": 0.015,
    "enabled": true
  }'
```

### Option E: Environment variables (legacy)

For backward compatibility, you can set provider API keys as environment
variables before starting the server. This registers hardcoded provider
IDs (`openai`, `anthropic`, `vllm`) with default endpoints:

```bash
export TOKENHUB_OPENAI_API_KEY="sk-..."
export TOKENHUB_ANTHROPIC_API_KEY="sk-ant-..."
export TOKENHUB_VLLM_ENDPOINTS="http://vllm-host:8000"
```

This approach is limited: it only supports three fixed provider IDs, uses
default base URLs, and requires a restart to change. The methods above are
preferred for any real deployment.

> **Providers persist across restarts.** Once registered via the API,
> `tokenhubctl`, or the UI, providers and models are stored in the database
> and restored automatically on restart. You only need to configure them once.
> API keys for vault-backed providers require the vault to be unlocked after
> restart, or re-supplied via `bootstrap.local`.

## 3. Verify It's Running

```bash
curl http://localhost:8090/healthz
```

Or:

```bash
tokenhubctl status
```

Expected response:

```json
{"status": "ok", "adapters": 2, "models": 2}
```

## 4. Create an API Key

TokenHub issues its own API keys to clients. Provider keys stay on the server.

```bash
tokenhubctl apikey create '{"name":"my-first-key","scopes":"[\"chat\",\"plan\"]"}'
```

Or via curl:

```bash
curl -X POST http://localhost:8090/admin/v1/apikeys \
  -H "Content-Type: application/json" \
  -d '{"name": "my-first-key", "scopes": "[\"chat\",\"plan\"]"}'
```

Save the returned `key` value — it is shown only once:

```json
{
  "ok": true,
  "key": "tokenhub_a1b2c3d4...",
  "id": "a1b2c3d4e5f6g7h8",
  "prefix": "tokenhub_a1b2c3d4"
}
```

## 5. Send Your First Request

```bash
curl -X POST http://localhost:8090/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_a1b2c3d4..." \
  -d '{
    "request": {
      "messages": [
        {"role": "user", "content": "What is the capital of France?"}
      ]
    }
  }'
```

TokenHub selects the best available model based on its routing policy and
returns the response:

```json
{
  "negotiated_model": "gpt-4o",
  "estimated_cost_usd": 0.0023,
  "routing_reason": "routed-weight-8",
  "response": {
    "choices": [{
      "message": {
        "role": "assistant",
        "content": "The capital of France is Paris."
      }
    }]
  }
}
```

## 6. Explore

```bash
# See all registered providers and models
tokenhubctl provider list
tokenhubctl model list

# Watch routing decisions in real time
tokenhubctl events

# Open the admin dashboard
open http://localhost:8090/admin
```

## Next Steps

- [Provider Management](admin/providers.md) for provider types, credential storage, and model discovery
- [Chat API](user/chat-api.md) for request options, routing policies, and parameters
- [Routing Configuration](admin/routing.md) to tune model selection behavior
- [tokenhubctl CLI](admin/tokenhubctl.md) for command-line administration
- [Configuration Reference](deployment/configuration.md) for all environment variables
