"""
Tests for the routing engine
"""
import pytest
from tokenhub.routing import RoutingEngine, RoutingPolicy
from tokenhub.models import ModelRegistry
from tokenhub.registry import ProviderRegistry
from tokenhub.providers import ChatRequest, ChatMessage


def test_select_model():
    """Test model selection."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    provider_registry.add_provider("mock", "mock", api_key="test-key", store_in_vault=False)
    
    engine = RoutingEngine(model_registry, provider_registry)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Hello")
    ])
    
    selected = engine.select_model(request)
    assert selected is not None


def test_select_model_by_context():
    """Test model selection based on context size."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    
    engine = RoutingEngine(model_registry, provider_registry)
    
    policy = RoutingPolicy(min_context_size=8000)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Hello")
    ])
    
    selected = engine.select_model(request, policy)
    if selected:
        assert selected.context_size >= 8000


def test_select_model_by_cost():
    """Test model selection based on cost."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    
    engine = RoutingEngine(model_registry, provider_registry)
    
    policy = RoutingPolicy(max_cost_per_1k_tokens=0.01)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Hello")
    ])
    
    selected = engine.select_model(request, policy)
    if selected:
        assert selected.cost_per_1k_tokens <= 0.01


def test_execute_with_routing():
    """Test executing a request with routing."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    provider_registry.add_provider("mock", "mock", api_key="test-key", store_in_vault=False)
    
    engine = RoutingEngine(model_registry, provider_registry)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Hello, how are you?")
    ])
    
    response = engine.execute_with_routing(request)
    
    assert response is not None
    assert response.content
    assert response.model
