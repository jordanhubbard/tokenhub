"""
Routing engine for selecting models based on context size, cost, and weight
"""
from typing import Optional, List
from dataclasses import dataclass
from .models import ModelRegistry, ModelMetadata
from .registry import ProviderRegistry
from .providers import ChatRequest, ChatResponse


@dataclass
class RoutingPolicy:
    """Policy for routing requests to models."""
    max_cost_per_1k_tokens: Optional[float] = None
    min_context_size: Optional[int] = None
    prefer_higher_weight: bool = True
    allow_escalation: bool = True


class RoutingEngine:
    """
    Engine for routing requests to appropriate models based on context, cost, and weight.
    """
    
    def __init__(
        self,
        model_registry: ModelRegistry,
        provider_registry: ProviderRegistry,
        default_policy: Optional[RoutingPolicy] = None
    ):
        """
        Initialize the routing engine.
        
        Args:
            model_registry: Model registry
            provider_registry: Provider registry
            default_policy: Default routing policy
        """
        self.model_registry = model_registry
        self.provider_registry = provider_registry
        self.default_policy = default_policy or RoutingPolicy()
    
    def select_model(
        self,
        request: ChatRequest,
        policy: Optional[RoutingPolicy] = None
    ) -> Optional[ModelMetadata]:
        """
        Select the best model for a request based on the routing policy.
        
        Args:
            request: Chat request
            policy: Optional routing policy (uses default if not provided)
            
        Returns:
            Selected model metadata or None if no suitable model found
        """
        policy = policy or self.default_policy
        
        # Estimate context size needed (rough approximation)
        estimated_context = self._estimate_context_size(request)
        
        # Get candidate models
        candidates = self.model_registry.list_models()
        
        # Filter by context size
        if policy.min_context_size or estimated_context:
            required_context = max(
                policy.min_context_size or 0,
                estimated_context
            )
            candidates = [
                m for m in candidates
                if m.context_size >= required_context
            ]
        
        # Filter by cost
        if policy.max_cost_per_1k_tokens:
            candidates = [
                m for m in candidates
                if m.cost_per_1k_tokens <= policy.max_cost_per_1k_tokens
            ]
        
        if not candidates:
            return None
        
        # Sort by weight if preferred
        if policy.prefer_higher_weight:
            candidates.sort(key=lambda m: m.weight, reverse=True)
        else:
            # Sort by cost (lower is better)
            candidates.sort(key=lambda m: m.cost_per_1k_tokens)
        
        return candidates[0]
    
    def _estimate_context_size(self, request: ChatRequest) -> int:
        """
        Estimate the context size needed for a request.
        Rough approximation: ~4 chars per token.
        
        Args:
            request: Chat request
            
        Returns:
            Estimated token count
        """
        total_chars = sum(len(msg.content) for msg in request.messages)
        estimated_tokens = total_chars // 4
        
        # Add max_tokens if specified
        if request.max_tokens:
            estimated_tokens += request.max_tokens
        
        return estimated_tokens
    
    def execute_with_routing(
        self,
        request: ChatRequest,
        policy: Optional[RoutingPolicy] = None
    ) -> ChatResponse:
        """
        Execute a chat request with automatic model selection and routing.
        
        Args:
            request: Chat request
            policy: Optional routing policy
            
        Returns:
            Chat response
            
        Raises:
            Exception: If no suitable model found or request fails
        """
        policy = policy or self.default_policy
        
        # Select initial model
        selected_model = self.select_model(request, policy)
        if not selected_model:
            raise ValueError("No suitable model found for request")
        
        # Set model in request if not already set
        if not request.model:
            request.model = selected_model.name
        
        # Get provider
        provider = self.provider_registry.get_provider(selected_model.provider)
        if not provider:
            raise ValueError(f"Provider {selected_model.provider} not found")
        
        # Try to execute request
        try:
            response = provider.chat_completion(request)
            return response
        except Exception as e:
            # Try escalation if allowed
            if policy.allow_escalation:
                return self._escalate_request(request, selected_model, policy, e)
            raise
    
    def _escalate_request(
        self,
        request: ChatRequest,
        failed_model: ModelMetadata,
        policy: RoutingPolicy,
        error: Exception
    ) -> ChatResponse:
        """
        Escalate request to a higher-tier model after failure.
        
        Args:
            request: Chat request
            failed_model: Model that failed
            policy: Routing policy
            error: Original error
            
        Returns:
            Chat response from escalated model
            
        Raises:
            Exception: If escalation fails
        """
        # Get models with higher weight or larger context
        candidates = self.model_registry.list_models()
        candidates = [
            m for m in candidates
            if (m.weight > failed_model.weight or m.context_size > failed_model.context_size)
            and m.name != failed_model.name
        ]
        
        if policy.max_cost_per_1k_tokens:
            candidates = [
                m for m in candidates
                if m.cost_per_1k_tokens <= policy.max_cost_per_1k_tokens
            ]
        
        if not candidates:
            raise ValueError(f"No escalation path available. Original error: {error}")
        
        # Sort by weight
        candidates.sort(key=lambda m: m.weight, reverse=True)
        escalated_model = candidates[0]
        
        # Try with escalated model
        request.model = escalated_model.name
        provider = self.provider_registry.get_provider(escalated_model.provider)
        if not provider:
            raise ValueError(f"Provider {escalated_model.provider} not found")
        
        return provider.chat_completion(request)
