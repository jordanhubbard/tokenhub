"""
Example usage of TokenHub API
"""
import os
import sys

# Add the package to path
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from tokenhub.api import create_app
from tokenhub.providers import ChatRequest, ChatMessage

def main():
    """Demonstrate TokenHub usage."""
    
    # Create the API
    print("Creating TokenHub API...")
    api = create_app("example-password", "/tmp/tokenhub-example.db")
    
    # Add a mock provider
    print("Adding mock provider...")
    api.provider_registry.add_provider(
        name="mock",
        provider_type="mock",
        api_key="test-key-123",
        store_in_vault=True
    )
    
    # Test simple chat
    print("\n=== Simple Chat ===")
    request = ChatRequest(
        messages=[ChatMessage(role="user", content="Hello, how are you?")]
    )
    
    result = api.orchestration_engine.execute(request)
    print(f"Response: {result.final_response.content}")
    print(f"Model: {result.final_response.model}")
    print(f"Tokens: {result.final_response.tokens_used}")
    
    # Test adversarial mode
    print("\n=== Adversarial Mode ===")
    from tokenhub.orchestration import OrchestrationConfig, OrchestrationMode
    
    request = ChatRequest(
        messages=[ChatMessage(role="user", content="Explain quantum computing")]
    )
    
    config = OrchestrationConfig(
        mode=OrchestrationMode.ADVERSARIAL,
        planner_model="mock-gpt-4",
        critic_model="mock-gpt-3.5"
    )
    
    result = api.orchestration_engine.execute(request, config)
    print(f"Final Response: {result.final_response.content}")
    print(f"Initial Plan: {result.plan_response.content}")
    print(f"Critique: {result.critique_response.content}")
    
    # Display model registry
    print("\n=== Available Models ===")
    models = api.model_registry.list_models()
    for model in models:
        print(f"- {model.name} (provider: {model.provider}, "
              f"cost: ${model.cost_per_1k_tokens}/1k, "
              f"context: {model.context_size}, weight: {model.weight})")
    
    print("\n✓ All examples completed successfully!")

if __name__ == "__main__":
    main()
