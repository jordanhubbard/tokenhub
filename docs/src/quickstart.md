# Quick Start

This guide gets TokenHub running and serving your first request in under five minutes.

## Prerequisites

- Go 1.24+ (for building from source) or Docker
- At least one provider API key (OpenAI or Anthropic)

## Option 1: Run with Docker Compose

```bash
# Clone the repository
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub

# Set your provider API keys
export TOKENHUB_OPENAI_API_KEY="sk-..."
export TOKENHUB_ANTHROPIC_API_KEY="sk-ant-..."

# Start TokenHub
docker compose up -d tokenhub
```

## Option 2: Build from Source

```bash
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub

# Build
make build

# Set your provider API keys
export TOKENHUB_OPENAI_API_KEY="sk-..."

# Run
./bin/tokenhub
```

TokenHub starts on port 8080 by default.

## Verify It's Running

```bash
curl http://localhost:8080/healthz
```

Expected response:

```json
{"status": "ok", "adapters": 1, "models": 4}
```

## Create an API Key

TokenHub issues its own API keys to clients. Provider keys stay in the vault.

```bash
curl -X POST http://localhost:8080/admin/v1/apikeys \
  -H "Content-Type: application/json" \
  -d '{"name": "my-first-key", "scopes": "[\"chat\",\"plan\"]"}'
```

Save the returned `key` value. It is shown only once:

```json
{
  "ok": true,
  "key": "tokenhub_a1b2c3d4...",
  "id": "a1b2c3d4e5f6g7h8",
  "prefix": "tokenhub_a1b2c3d4"
}
```

## Send Your First Chat Request

```bash
curl -X POST http://localhost:8080/v1/chat \
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

Response:

```json
{
  "negotiated_model": "gpt-4",
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

TokenHub automatically selected the best model based on its default routing policy.

## Send an Orchestrated Request

Use the plan endpoint for multi-model reasoning:

```bash
curl -X POST http://localhost:8080/v1/plan \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_a1b2c3d4..." \
  -d '{
    "request": {
      "messages": [
        {"role": "user", "content": "Design a REST API for a task management app"}
      ]
    },
    "orchestration": {
      "mode": "adversarial",
      "iterations": 2
    }
  }'
```

This runs a plan-critique-refine loop using multiple models.

## Open the Admin UI

Navigate to [http://localhost:8080/admin](http://localhost:8080/admin) to access the built-in admin dashboard where you can manage providers, models, routing policies, and monitor request flow in real time.

## Next Steps

- [Chat API details](user/chat-api.md) for request options, policies, and parameters
- [Provider Management](admin/providers.md) to configure additional providers
- [Routing Configuration](admin/routing.md) to tune model selection behavior
- [Docker deployment](deployment/docker.md) for production setup
