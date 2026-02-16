"""
Tests for the model registry
"""
import pytest
from tokenhub.models import ModelRegistry, ModelMetadata


def test_register_model():
    """Test registering a model."""
    registry = ModelRegistry()
    
    model = ModelMetadata(
        name="test-model",
        provider="test",
        cost_per_1k_tokens=0.01,
        context_size=2048,
        weight=75
    )
    
    registry.register_model(model)
    
    retrieved = registry.get_model("test-model")
    assert retrieved is not None
    assert retrieved.name == "test-model"
    assert retrieved.cost_per_1k_tokens == 0.01


def test_list_models():
    """Test listing models."""
    registry = ModelRegistry()
    
    # Registry starts with default models
    models = registry.list_models()
    assert len(models) > 0


def test_list_models_by_provider():
    """Test filtering models by provider."""
    registry = ModelRegistry()
    
    models = registry.list_models(provider="mock")
    assert len(models) > 0
    assert all(m.provider == "mock" for m in models)


def test_get_models_by_context_size():
    """Test getting models by context size."""
    registry = ModelRegistry()
    
    models = registry.get_models_by_context_size(4096)
    assert len(models) > 0
    assert all(m.context_size >= 4096 for m in models)


def test_get_models_by_max_cost():
    """Test getting models by maximum cost."""
    registry = ModelRegistry()
    
    models = registry.get_models_by_max_cost(0.01)
    assert all(m.cost_per_1k_tokens <= 0.01 for m in models)


def test_remove_model():
    """Test removing a model."""
    registry = ModelRegistry()
    
    model = ModelMetadata(
        name="temp-model",
        provider="test",
        cost_per_1k_tokens=0.01,
        context_size=2048,
        weight=50
    )
    
    registry.register_model(model)
    assert registry.get_model("temp-model") is not None
    
    assert registry.remove_model("temp-model") is True
    assert registry.get_model("temp-model") is None
    assert registry.remove_model("temp-model") is False


def test_export_import_models():
    """Test exporting and importing models."""
    registry1 = ModelRegistry()
    
    model = ModelMetadata(
        name="export-test",
        provider="test",
        cost_per_1k_tokens=0.01,
        context_size=2048,
        weight=50
    )
    registry1.register_model(model)
    
    exported = registry1.export_models()
    
    registry2 = ModelRegistry()
    registry2.import_models(exported)
    
    retrieved = registry2.get_model("export-test")
    assert retrieved is not None
    assert retrieved.name == "export-test"
