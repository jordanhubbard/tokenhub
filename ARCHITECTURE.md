# Architecture Overview

TokenHub is designed with a clean, modular architecture that separates concerns and allows for easy extension.

## Components

### 1. API Layer (`api.py`)
The REST API layer built with Flask provides:
- `/v1/chat` - Chat completion endpoint (simple and adversarial modes)
- `/admin/providers` - Provider management (add, list)
- `/admin/models` - Model management (add models)
- `/metrics` - Prometheus metrics endpoint
- `/health` - Health check endpoint

Authentication for admin endpoints uses Bearer token with the admin password.

### 2. Orchestration Engine (`orchestration.py`)
Handles multi-model interactions with two modes:

**Simple Mode**: Direct single-model response
- Select model based on routing policy
- Execute request
- Return response

**Adversarial Mode**: Model A generates, Model B critiques
- Model A (planner) generates initial response
- Model B (critic) analyzes and critiques the response
- Optional refinement loop where Model A improves based on critique
- Configurable number of refinement iterations

### 3. Routing Engine (`routing.py`)
Intelligent model selection based on:
- **Context size**: Estimates tokens needed and selects models with sufficient context
- **Cost constraints**: Filters models by maximum cost per 1k tokens
- **Weight (priority)**: Selects highest-weight model among candidates
- **Escalation**: On failure or context overflow, automatically escalates to higher-tier models

Routing policies control selection:
```python
RoutingPolicy(
    max_cost_per_1k_tokens=0.01,  # Cost limit
    min_context_size=8000,         # Minimum context
    prefer_higher_weight=True,     # Prefer higher priority models
    allow_escalation=True          # Enable automatic escalation
)
```

### 4. Model Registry (`models.py`)
Tracks metadata for each model:
- **Name**: Unique identifier
- **Provider**: Which provider offers this model
- **Cost**: Cost per 1000 tokens
- **Context size**: Maximum context window
- **Weight**: Priority weight (higher = preferred)

Models are persisted to SQLite and can be dynamically added/updated.

### 5. Provider Registry (`registry.py`)
Manages provider adapters:
- Registers provider instances with their configurations
- Retrieves API keys from vault or environment variables
- Validates provider credentials
- Supports multiple instances of the same provider type

### 6. Provider Adapters (`providers.py`)
Abstract interface that all providers must implement:
```python
class ProviderAdapter(ABC):
    def chat_completion(request: ChatRequest) -> ChatResponse
    def get_available_models() -> List[str]
    def validate_api_key() -> bool
```

This allows easy addition of new providers (OpenAI, Anthropic, etc.) by implementing the interface.

### 7. Secure Vault (`vault.py`)
Encrypted storage for API keys:
- **AES-256-CBC** encryption
- **PBKDF2-HMAC-SHA256** key derivation (100,000 iterations)
- Random salt generation
- Admin password protection
- Export/import for persistence

### 8. Persistence Layer (`persistence.py`)
SQLite-based persistence for:
- Vault encrypted data
- Model registry
- Metrics and event logs

## Data Flow

### Chat Request Flow
```
1. Client -> POST /v1/chat
2. API validates request
3. Orchestration Engine determines mode
4. Routing Engine selects model(s)
5. Provider Adapter executes API call(s)
6. Response aggregated and returned
7. Metrics recorded to Prometheus + SQLite
```

### Provider Registration Flow
```
1. Admin -> POST /admin/providers with credentials
2. API validates admin password
3. Provider Registry creates adapter instance
4. API key encrypted and stored in Vault
5. Vault data persisted to SQLite
```

## Security

### API Keys
- Never stored in plaintext
- AES-256 encryption with unique salt per vault
- Key derivation uses PBKDF2 with 100k iterations
- Encrypted data persisted to database

### Admin Access
- Bearer token authentication for admin endpoints
- Password required for vault operations
- All provider management requires authentication

### Environment Variables
API keys can be provided via:
1. Direct API call (stored in vault)
2. Environment variables (e.g., `OPENAI_API_KEY`)
3. Pre-loaded vault data

## Extensibility

### Adding a New Provider
1. Implement `ProviderAdapter` interface
2. Register provider class: `registry.register_provider_class("name", MyAdapter)`
3. Add via API or directly: `registry.add_provider("instance", "name", api_key=...)`

### Adding a New Model
```python
model = ModelMetadata(
    name="gpt-5",
    provider="openai",
    cost_per_1k_tokens=0.05,
    context_size=32000,
    weight=150
)
model_registry.register_model(model)
```

### Custom Orchestration Modes
Extend `OrchestrationEngine` to add new orchestration patterns beyond simple and adversarial modes.

## Monitoring

### Prometheus Metrics
- `tokenhub_requests_total`: Total requests by endpoint and status
- `tokenhub_request_duration_seconds`: Request duration histogram
- `tokenhub_tokens_used_total`: Total tokens used by model
- `tokenhub_model_calls_total`: Model calls by model and status

### SQLite Logs
All requests logged with:
- Timestamp
- Event type
- Model used
- Tokens consumed
- Duration
- Orchestration mode

## Configuration

### Environment Variables
- `TOKENHUB_ADMIN_PASSWORD`: Admin password (required)
- `TOKENHUB_HOST`: Bind host (default: 0.0.0.0)
- `TOKENHUB_PORT`: Bind port (default: 8080)
- `TOKENHUB_DB_PATH`: SQLite database path
- `TOKENHUB_DEBUG`: Debug mode (true/false)
- `{PROVIDER}_API_KEY`: Provider API keys

### Docker Volumes
Mount `/data` for persistence:
```yaml
volumes:
  - ./data:/data
```

## Performance Considerations

### Model Selection
- Routing is O(n) where n is number of models
- Filter operations reduce candidate set early
- Consider caching selected models for repeated similar requests

### Encryption Overhead
- PBKDF2 with 100k iterations adds ~100ms per vault operation
- Vault operations are infrequent (startup, admin changes)
- Runtime decryption is fast (AES-CBC)

### Database
- SQLite suitable for moderate load
- For high traffic, consider PostgreSQL/MySQL
- Add indexes on frequently queried fields

## Testing

### Unit Tests
Run test suite:
```bash
pytest tests/
```

### Integration Tests
Test API endpoints:
```bash
python test_api.py
```

### Docker Testing
```bash
docker-compose up
curl http://localhost:8080/health
```
