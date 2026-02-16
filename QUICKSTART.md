# TokenHub Quick Reference

## Installation

### Docker (Recommended)
```bash
# Clone repository
git clone https://github.com/jordanhubbard/tokenhub.git
cd tokenhub

# Set admin password
export TOKENHUB_ADMIN_PASSWORD="your-secure-password"

# Start with docker-compose
docker-compose up -d

# Check status
curl http://localhost:8080/health
```

### Manual
```bash
# Install dependencies
pip install -r requirements.txt

# Set environment
export TOKENHUB_ADMIN_PASSWORD="your-secure-password"

# Run server
python -m tokenhub.main
```

## API Examples

### Add Provider
```bash
curl -X POST http://localhost:8080/admin/providers \
  -H "Authorization: Bearer YOUR_ADMIN_PASSWORD" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-provider",
    "provider_type": "mock",
    "api_key": "your-api-key"
  }'
```

### Simple Chat
```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### Adversarial Chat
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
    "max_refinement_iterations": 2
  }'
```

### Add Custom Model
```bash
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer YOUR_ADMIN_PASSWORD" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "gpt-4-turbo",
    "provider": "openai",
    "cost_per_1k_tokens": 0.01,
    "context_size": 128000,
    "weight": 200
  }'
```

### View Metrics
```bash
curl http://localhost:8080/metrics
```

## Python SDK Usage

```python
from tokenhub.api import create_app
from tokenhub.providers import ChatRequest, ChatMessage
from tokenhub.orchestration import OrchestrationConfig, OrchestrationMode

# Create API instance
api = create_app("admin-password", "tokenhub.db")

# Add provider
api.provider_registry.add_provider(
    name="openai",
    provider_type="openai",  # Requires OpenAI adapter implementation
    api_key="sk-..."
)

# Simple chat
request = ChatRequest(
    messages=[ChatMessage(role="user", content="Hello!")]
)
result = api.orchestration_engine.execute(request)
print(result.final_response.content)

# Adversarial mode
config = OrchestrationConfig(
    mode=OrchestrationMode.ADVERSARIAL,
    planner_model="gpt-4",
    critic_model="gpt-3.5-turbo",
    enable_refinement=True,
    max_refinement_iterations=2
)
result = api.orchestration_engine.execute(request, config)
```

## Extending with Custom Providers

```python
from tokenhub.providers import ProviderAdapter, ChatRequest, ChatResponse

class MyProviderAdapter(ProviderAdapter):
    def chat_completion(self, request: ChatRequest) -> ChatResponse:
        # Implement your provider's API call
        response = your_api_call(
            messages=[(m.role, m.content) for m in request.messages],
            model=request.model,
            max_tokens=request.max_tokens
        )
        
        return ChatResponse(
            content=response.text,
            model=request.model or "default",
            tokens_used=response.tokens,
            finish_reason=response.finish_reason
        )
    
    def get_available_models(self):
        return ["model-1", "model-2"]
    
    def validate_api_key(self):
        # Validate API key
        return True

# Register
api.provider_registry.register_provider_class("myprovider", MyProviderAdapter)
```

## Configuration Files

### .env
```bash
TOKENHUB_ADMIN_PASSWORD=your-secure-password
TOKENHUB_HOST=0.0.0.0
TOKENHUB_PORT=8080
TOKENHUB_DB_PATH=/data/tokenhub.db
TOKENHUB_DEBUG=false
```

### docker-compose.yml
```yaml
version: '3.8'

services:
  tokenhub:
    build: .
    ports:
      - "8080:8080"
    environment:
      - TOKENHUB_ADMIN_PASSWORD=${TOKENHUB_ADMIN_PASSWORD}
    volumes:
      - tokenhub-data:/data
    restart: unless-stopped

volumes:
  tokenhub-data:
```

## Routing Policies

```python
from tokenhub.routing import RoutingPolicy

# Cost-optimized
policy = RoutingPolicy(
    max_cost_per_1k_tokens=0.01,
    prefer_higher_weight=False  # Prefer cheaper models
)

# Context-optimized
policy = RoutingPolicy(
    min_context_size=32000,
    prefer_higher_weight=True
)

# Balanced
policy = RoutingPolicy(
    max_cost_per_1k_tokens=0.05,
    min_context_size=8000,
    prefer_higher_weight=True,
    allow_escalation=True
)
```

## Monitoring

### Prometheus Metrics
- `tokenhub_requests_total{endpoint, status}`
- `tokenhub_request_duration_seconds`
- `tokenhub_tokens_used_total{model}`
- `tokenhub_model_calls_total{model, status}`

### Grafana Dashboard
Configure Prometheus scraping:
```yaml
scrape_configs:
  - job_name: 'tokenhub'
    static_configs:
      - targets: ['tokenhub:8080']
```

## Troubleshooting

### Connection Refused
Check if service is running:
```bash
docker ps
curl http://localhost:8080/health
```

### Authentication Failed
Verify admin password:
```bash
echo $TOKENHUB_ADMIN_PASSWORD
```

### Provider Not Found
List registered providers:
```bash
curl -H "Authorization: Bearer PASSWORD" \
  http://localhost:8080/admin/providers
```

### No Suitable Model
Check model registry:
```python
models = api.model_registry.list_models()
for m in models:
    print(f"{m.name}: context={m.context_size}, cost={m.cost_per_1k_tokens}")
```

## Testing

### Run Unit Tests
```bash
pytest tests/ -v
```

### Run Integration Tests
```bash
python test_api.py
```

### Test in Docker
```bash
docker-compose up -d
python test_api.py  # Update to use port from docker-compose
docker-compose down
```

## Security Best Practices

1. **Strong Admin Password**: Use 20+ character passwords
2. **Environment Variables**: Never commit passwords to git
3. **HTTPS**: Use reverse proxy (nginx) with SSL in production
4. **API Keys**: Rotate regularly
5. **Monitoring**: Watch for unusual usage patterns
6. **Backups**: Backup `/data/tokenhub.db` regularly

## Performance Tuning

### High Traffic
- Use production WSGI server (gunicorn, uwsgi)
- Add caching layer for model selection
- Consider PostgreSQL instead of SQLite
- Scale horizontally with load balancer

### Low Latency
- Keep providers geographically close
- Use connection pooling
- Cache model metadata
- Optimize routing policy complexity

## Support

For issues and questions:
- GitHub Issues: https://github.com/jordanhubbard/tokenhub/issues
- Documentation: See README.md and ARCHITECTURE.md
