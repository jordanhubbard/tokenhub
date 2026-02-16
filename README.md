# TokenHub

Organize your token providers by cost, complexity, and reliability. TokenHub is an intelligent AI model router and orchestrator that automatically selects the best model for your requests based on context size, cost, and performance requirements.

## Features

- **Provider Registry**: Manage multiple AI providers with encrypted API key storage (AES-256)
- **Model Registry**: Track model metadata including cost, context size, and priority weights
- **Intelligent Routing**: Automatically select models based on context requirements and cost constraints
- **Escalation Support**: Automatically escalate to higher-tier models on failure or context overflow
- **Adversarial Orchestration**: Generate responses with one model, critique with another, optional refinement loop
- **REST API**: Clean REST endpoints for chat, provider management, and metrics
- **Secure Vault**: AES-256 encrypted storage for API keys with admin password protection
- **Prometheus Metrics**: Built-in metrics for monitoring and observability
- **SQLite Persistence**: Default database for configuration and state
- **Fully Containerized**: Docker and docker-compose support

## Architecture

TokenHub follows a clean, modular architecture with separation of concerns:

```
┌─────────────────────────────────────────────────────────────┐
│                         API Layer                            │
│              (Flask REST API + Prometheus)                   │
└────────────────┬────────────────────────────────────────────┘
                 │
┌────────────────┴────────────────────────────────────────────┐
│                  Orchestration Engine                        │
│         (Simple mode / Adversarial mode)                     │
└────────────────┬────────────────────────────────────────────┘
                 │
┌────────────────┴────────────────────────────────────────────┐
│                    Routing Engine                            │
│    (Model selection, escalation, policy enforcement)         │
└────────┬────────────────────────┬──────────────────────────┘
         │                        │
┌────────┴────────┐      ┌────────┴──────────┐
│ Model Registry  │      │ Provider Registry │
│ (cost, context, │      │  (API adapters)   │
│     weight)     │      └────────┬──────────┘
└─────────────────┘               │
                         ┌────────┴──────────┐
                         │   Secure Vault    │
                         │  (AES-256 keys)   │
                         └───────────────────┘
```

## Quick Start

### Using Docker (Recommended)

1. Clone the repository:
```bash
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub
```

2. Set your admin password:
```bash
export TOKENHUB_ADMIN_PASSWORD="your-secure-password"
```

3. Start the service:
```bash
docker-compose up -d
```

The service will be available at `http://localhost:8080`

### Manual Installation

1. Install dependencies:
```bash
pip install -r requirements.txt
```

2. Set environment variables:
```bash
export TOKENHUB_ADMIN_PASSWORD="your-secure-password"
```

3. Run the service:
```bash
python -m tokenhub.main
```

## API Usage

### Add a Provider

```bash
curl -X POST http://localhost:8080/admin/providers \
  -H "Authorization: Bearer your-admin-password" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-provider",
    "provider_type": "mock",
    "api_key": "your-api-key"
  }'
```

### Chat Completion (Simple Mode)

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ],
    "orchestration_mode": "simple"
  }'
```

### Chat Completion (Adversarial Mode)

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "Explain quantum computing"}
    ],
    "orchestration_mode": "adversarial",
    "planner_model": "mock-gpt-4",
    "critic_model": "mock-gpt-3.5",
    "enable_refinement": true,
    "max_refinement_iterations": 1
  }'
```

### Prometheus Metrics

```bash
curl http://localhost:8080/metrics
```

## Configuration

Configuration is managed through environment variables:

- `TOKENHUB_ADMIN_PASSWORD`: Admin password for vault access (required)
- `TOKENHUB_HOST`: Host to bind to (default: 0.0.0.0)
- `TOKENHUB_PORT`: Port to bind to (default: 8080)
- `TOKENHUB_DB_PATH`: SQLite database path (default: tokenhub.db)
- `TOKENHUB_DEBUG`: Enable debug mode (default: false)

## Extending with Custom Providers

To add support for a new AI provider, implement the `ProviderAdapter` interface:

```python
from tokenhub.providers import ProviderAdapter, ChatRequest, ChatResponse

class MyProviderAdapter(ProviderAdapter):
    def chat_completion(self, request: ChatRequest) -> ChatResponse:
        # Implement your provider's API call
        pass
    
    def get_available_models(self) -> List[str]:
        # Return list of available models
        pass
    
    def validate_api_key(self) -> bool:
        # Validate the API key
        pass

# Register the provider
api.provider_registry.register_provider_class("myprovider", MyProviderAdapter)
```

## Development

Run tests (when available):
```bash
pytest
```

## Security

- API keys are encrypted with AES-256-CBC encryption
- Admin password required for provider management
- Key derivation using PBKDF2 with 100,000 iterations
- Secure random salt generation for each vault instance

## License

See LICENSE file for details.
