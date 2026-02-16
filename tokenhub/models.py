"""
Model registry for tracking model metadata (cost, context size, weight)
"""
from typing import Dict, List, Optional
from dataclasses import dataclass, asdict
import json


@dataclass
class ModelMetadata:
    """Metadata for a model."""
    name: str
    provider: str
    cost_per_1k_tokens: float
    context_size: int
    weight: int  # Priority weight (higher = preferred)
    
    def to_dict(self) -> dict:
        """Convert to dictionary."""
        return asdict(self)
    
    @classmethod
    def from_dict(cls, data: dict) -> "ModelMetadata":
        """Create from dictionary."""
        return cls(**data)


class ModelRegistry:
    """
    Registry for managing model metadata.
    """
    
    def __init__(self):
        """Initialize the model registry."""
        self._models: Dict[str, ModelMetadata] = {}
        self._initialize_default_models()
    
    def _initialize_default_models(self) -> None:
        """Initialize with some default model configurations."""
        defaults = [
            ModelMetadata(
                name="mock-gpt-3.5",
                provider="mock",
                cost_per_1k_tokens=0.002,
                context_size=4096,
                weight=50
            ),
            ModelMetadata(
                name="mock-gpt-4",
                provider="mock",
                cost_per_1k_tokens=0.03,
                context_size=8192,
                weight=100
            ),
        ]
        for model in defaults:
            self._models[model.name] = model
    
    def register_model(self, metadata: ModelMetadata) -> None:
        """
        Register a model with its metadata.
        
        Args:
            metadata: Model metadata
        """
        self._models[metadata.name] = metadata
    
    def get_model(self, name: str) -> Optional[ModelMetadata]:
        """
        Get model metadata by name.
        
        Args:
            name: Model name
            
        Returns:
            Model metadata or None if not found
        """
        return self._models.get(name)
    
    def list_models(self, provider: Optional[str] = None) -> List[ModelMetadata]:
        """
        List all registered models.
        
        Args:
            provider: Optional provider filter
            
        Returns:
            List of model metadata
        """
        models = list(self._models.values())
        if provider:
            models = [m for m in models if m.provider == provider]
        return models
    
    def remove_model(self, name: str) -> bool:
        """
        Remove a model from the registry.
        
        Args:
            name: Model name
            
        Returns:
            True if removed, False if not found
        """
        if name in self._models:
            del self._models[name]
            return True
        return False
    
    def get_models_by_context_size(self, min_context: int) -> List[ModelMetadata]:
        """
        Get models with at least the specified context size.
        
        Args:
            min_context: Minimum context size required
            
        Returns:
            List of matching models sorted by weight (descending)
        """
        models = [m for m in self._models.values() if m.context_size >= min_context]
        return sorted(models, key=lambda m: m.weight, reverse=True)
    
    def get_models_by_max_cost(self, max_cost: float) -> List[ModelMetadata]:
        """
        Get models with cost at or below the specified threshold.
        
        Args:
            max_cost: Maximum cost per 1k tokens
            
        Returns:
            List of matching models sorted by weight (descending)
        """
        models = [m for m in self._models.values() if m.cost_per_1k_tokens <= max_cost]
        return sorted(models, key=lambda m: m.weight, reverse=True)
    
    def export_models(self) -> Dict:
        """
        Export all models to a dictionary.
        
        Returns:
            Dictionary of model data
        """
        return {name: model.to_dict() for name, model in self._models.items()}
    
    def import_models(self, data: Dict) -> None:
        """
        Import models from a dictionary.
        
        Args:
            data: Dictionary of model data
        """
        for name, model_data in data.items():
            self._models[name] = ModelMetadata.from_dict(model_data)
