"""
Tests for the orchestration engine
"""
import pytest
from tokenhub.orchestration import OrchestrationEngine, OrchestrationConfig, OrchestrationMode
from tokenhub.routing import RoutingEngine
from tokenhub.models import ModelRegistry
from tokenhub.registry import ProviderRegistry
from tokenhub.providers import ChatRequest, ChatMessage


def test_simple_orchestration():
    """Test simple orchestration mode."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    provider_registry.add_provider("mock", "mock", api_key="test-key", store_in_vault=False)
    
    routing_engine = RoutingEngine(model_registry, provider_registry)
    orchestration_engine = OrchestrationEngine(routing_engine)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Hello")
    ])
    
    config = OrchestrationConfig(mode=OrchestrationMode.SIMPLE)
    
    result = orchestration_engine.execute(request, config)
    
    assert result.final_response is not None
    assert result.iterations == 1
    assert result.plan_response is None
    assert result.critique_response is None


def test_adversarial_orchestration():
    """Test adversarial orchestration mode."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    provider_registry.add_provider("mock", "mock", api_key="test-key", store_in_vault=False)
    
    routing_engine = RoutingEngine(model_registry, provider_registry)
    orchestration_engine = OrchestrationEngine(routing_engine)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Explain quantum computing")
    ])
    
    config = OrchestrationConfig(
        mode=OrchestrationMode.ADVERSARIAL,
        planner_model="mock-gpt-4",
        critic_model="mock-gpt-3.5"
    )
    
    result = orchestration_engine.execute(request, config)
    
    assert result.final_response is not None
    assert result.plan_response is not None
    assert result.critique_response is not None


def test_adversarial_with_refinement():
    """Test adversarial orchestration with refinement."""
    model_registry = ModelRegistry()
    provider_registry = ProviderRegistry()
    provider_registry.add_provider("mock", "mock", api_key="test-key", store_in_vault=False)
    
    routing_engine = RoutingEngine(model_registry, provider_registry)
    orchestration_engine = OrchestrationEngine(routing_engine)
    
    request = ChatRequest(messages=[
        ChatMessage(role="user", content="Explain quantum computing")
    ])
    
    config = OrchestrationConfig(
        mode=OrchestrationMode.ADVERSARIAL,
        planner_model="mock-gpt-4",
        critic_model="mock-gpt-3.5",
        enable_refinement=True,
        max_refinement_iterations=2
    )
    
    result = orchestration_engine.execute(request, config)
    
    assert result.final_response is not None
    assert result.refinement_responses is not None
    assert len(result.refinement_responses) <= 2
