"""
REST API for TokenHub
"""
import os
from flask import Flask, request, jsonify
from prometheus_client import Counter, Histogram, generate_latest, CONTENT_TYPE_LATEST
from typing import Optional
import time

from .vault import SecureVault
from .registry import ProviderRegistry
from .models import ModelRegistry, ModelMetadata
from .routing import RoutingEngine, RoutingPolicy
from .orchestration import OrchestrationEngine, OrchestrationConfig, OrchestrationMode
from .providers import ChatRequest, ChatMessage
from .persistence import PersistenceManager


# Prometheus metrics
REQUEST_COUNT = Counter('tokenhub_requests_total', 'Total requests', ['endpoint', 'status'])
REQUEST_DURATION = Histogram('tokenhub_request_duration_seconds', 'Request duration')
TOKENS_USED = Counter('tokenhub_tokens_used_total', 'Total tokens used', ['model'])
MODEL_CALLS = Counter('tokenhub_model_calls_total', 'Total model calls', ['model', 'status'])


class TokenHubAPI:
    """
    REST API for TokenHub service.
    """
    
    def __init__(self, admin_password: str, db_path: str = "tokenhub.db"):
        """
        Initialize the API.
        
        Args:
            admin_password: Admin password for vault access
            db_path: Path to SQLite database
        """
        self.app = Flask(__name__)
        self.admin_password = admin_password
        
        # Initialize components
        self.persistence = PersistenceManager(db_path)
        self.vault = SecureVault(admin_password)
        self.provider_registry = ProviderRegistry(self.vault)
        self.model_registry = ModelRegistry()
        self.routing_engine = RoutingEngine(self.model_registry, self.provider_registry)
        self.orchestration_engine = OrchestrationEngine(self.routing_engine)
        
        # Load persisted data
        self._load_persisted_data()
        
        # Register routes
        self._register_routes()
    
    def _load_persisted_data(self) -> None:
        """Load persisted data from database."""
        # Load vault data
        vault_data = self.persistence.load_vault_data()
        if vault_data:
            self.vault.import_encrypted(vault_data)
        
        # Load models
        models_data = self.persistence.load_models()
        if models_data:
            self.model_registry.import_models(models_data)
    
    def _save_vault(self) -> None:
        """Save vault data to database."""
        vault_data = self.vault.export_encrypted()
        self.persistence.save_vault_data(vault_data)
    
    def _save_models(self) -> None:
        """Save model registry to database."""
        models_data = self.model_registry.export_models()
        self.persistence.save_models(models_data)
    
    def _register_routes(self) -> None:
        """Register API routes."""
        
        @self.app.route('/v1/chat', methods=['POST'])
        @REQUEST_DURATION.time()
        def chat():
            """Handle chat completion requests."""
            start_time = time.time()
            
            try:
                data = request.json
                
                # Parse request
                messages = [
                    ChatMessage(role=msg["role"], content=msg["content"])
                    for msg in data.get("messages", [])
                ]
                
                chat_request = ChatRequest(
                    messages=messages,
                    model=data.get("model"),
                    max_tokens=data.get("max_tokens"),
                    temperature=data.get("temperature")
                )
                
                # Parse orchestration config
                orchestration_mode = data.get("orchestration_mode", "simple")
                config = OrchestrationConfig(
                    mode=OrchestrationMode(orchestration_mode),
                    planner_model=data.get("planner_model"),
                    critic_model=data.get("critic_model"),
                    max_refinement_iterations=data.get("max_refinement_iterations", 1),
                    enable_refinement=data.get("enable_refinement", False)
                )
                
                # Execute request
                result = self.orchestration_engine.execute(chat_request, config)
                
                # Record metrics
                REQUEST_COUNT.labels(endpoint='/v1/chat', status='success').inc()
                TOKENS_USED.labels(model=result.final_response.model).inc(result.final_response.tokens_used)
                MODEL_CALLS.labels(model=result.final_response.model, status='success').inc()
                
                # Log to persistence
                self.persistence.log_metric('chat_completion', {
                    'model': result.final_response.model,
                    'tokens_used': result.final_response.tokens_used,
                    'duration_ms': int((time.time() - start_time) * 1000),
                    'orchestration_mode': orchestration_mode
                })
                
                # Build response
                response_data = {
                    "response": result.final_response.content,
                    "model": result.final_response.model,
                    "tokens_used": result.final_response.tokens_used,
                    "finish_reason": result.final_response.finish_reason
                }
                
                # Add adversarial details if applicable
                if config.mode == OrchestrationMode.ADVERSARIAL:
                    response_data["adversarial"] = self.orchestration_engine.create_adversarial_summary(result)
                
                return jsonify(response_data), 200
                
            except Exception as e:
                REQUEST_COUNT.labels(endpoint='/v1/chat', status='error').inc()
                return jsonify({"error": str(e)}), 500
        
        @self.app.route('/admin/providers', methods=['POST'])
        def add_provider():
            """Add or update a provider."""
            try:
                # Verify admin password
                auth = request.headers.get('Authorization', '')
                if not auth.startswith('Bearer ') or auth[7:] != self.admin_password:
                    return jsonify({"error": "Unauthorized"}), 401
                
                data = request.json
                name = data.get("name")
                provider_type = data.get("provider_type")
                api_key = data.get("api_key")
                
                if not all([name, provider_type, api_key]):
                    return jsonify({"error": "Missing required fields"}), 400
                
                # Add provider
                self.provider_registry.add_provider(
                    name=name,
                    provider_type=provider_type,
                    api_key=api_key,
                    store_in_vault=True
                )
                
                # Save vault
                self._save_vault()
                
                REQUEST_COUNT.labels(endpoint='/admin/providers', status='success').inc()
                return jsonify({"message": "Provider added successfully"}), 200
                
            except Exception as e:
                REQUEST_COUNT.labels(endpoint='/admin/providers', status='error').inc()
                return jsonify({"error": str(e)}), 500
        
        @self.app.route('/admin/providers', methods=['GET'])
        def list_providers():
            """List all providers."""
            try:
                # Verify admin password
                auth = request.headers.get('Authorization', '')
                if not auth.startswith('Bearer ') or auth[7:] != self.admin_password:
                    return jsonify({"error": "Unauthorized"}), 401
                
                providers = self.provider_registry.list_providers()
                return jsonify({"providers": providers}), 200
                
            except Exception as e:
                return jsonify({"error": str(e)}), 500
        
        @self.app.route('/admin/models', methods=['POST'])
        def add_model():
            """Add or update a model."""
            try:
                # Verify admin password
                auth = request.headers.get('Authorization', '')
                if not auth.startswith('Bearer ') or auth[7:] != self.admin_password:
                    return jsonify({"error": "Unauthorized"}), 401
                
                data = request.json
                model = ModelMetadata(
                    name=data["name"],
                    provider=data["provider"],
                    cost_per_1k_tokens=data["cost_per_1k_tokens"],
                    context_size=data["context_size"],
                    weight=data["weight"]
                )
                
                self.model_registry.register_model(model)
                self._save_models()
                
                return jsonify({"message": "Model added successfully"}), 200
                
            except Exception as e:
                return jsonify({"error": str(e)}), 500
        
        @self.app.route('/metrics', methods=['GET'])
        def metrics():
            """Expose Prometheus metrics."""
            return generate_latest(), 200, {'Content-Type': CONTENT_TYPE_LATEST}
        
        @self.app.route('/health', methods=['GET'])
        def health():
            """Health check endpoint."""
            return jsonify({"status": "healthy"}), 200
    
    def run(self, host: str = '0.0.0.0', port: int = 8080, debug: bool = False):
        """
        Run the API server.
        
        Args:
            host: Host to bind to
            port: Port to bind to
            debug: Enable debug mode
        """
        self.app.run(host=host, port=port, debug=debug)


def create_app(admin_password: Optional[str] = None, db_path: str = "tokenhub.db") -> TokenHubAPI:
    """
    Create a TokenHub API instance.
    
    Args:
        admin_password: Admin password (uses env var if not provided)
        db_path: Path to SQLite database
        
    Returns:
        TokenHubAPI instance
    """
    if admin_password is None:
        admin_password = os.getenv('TOKENHUB_ADMIN_PASSWORD', 'changeme')
    
    return TokenHubAPI(admin_password, db_path)
