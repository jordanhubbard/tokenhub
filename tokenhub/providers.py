"""
Provider adapter interface and base implementations
"""
from abc import ABC, abstractmethod
from typing import Dict, Any, Optional, List
from dataclasses import dataclass


@dataclass
class ChatMessage:
    """Represents a chat message."""
    role: str
    content: str


@dataclass
class ChatRequest:
    """Represents a chat completion request."""
    messages: List[ChatMessage]
    model: Optional[str] = None
    max_tokens: Optional[int] = None
    temperature: Optional[float] = None


@dataclass
class ChatResponse:
    """Represents a chat completion response."""
    content: str
    model: str
    tokens_used: int
    finish_reason: str


class ProviderAdapter(ABC):
    """
    Abstract base class for provider adapters.
    All provider implementations must implement this interface.
    """
    
    def __init__(self, api_key: str, **kwargs):
        """
        Initialize the provider adapter.
        
        Args:
            api_key: API key for authentication
            **kwargs: Additional provider-specific configuration
        """
        self.api_key = api_key
        self.config = kwargs
    
    @abstractmethod
    def chat_completion(self, request: ChatRequest) -> ChatResponse:
        """
        Execute a chat completion request.
        
        Args:
            request: Chat completion request
            
        Returns:
            Chat completion response
            
        Raises:
            Exception: If the request fails
        """
        pass
    
    @abstractmethod
    def get_available_models(self) -> List[str]:
        """
        Get list of available models from this provider.
        
        Returns:
            List of model names
        """
        pass
    
    @abstractmethod
    def validate_api_key(self) -> bool:
        """
        Validate that the API key is valid.
        
        Returns:
            True if valid, False otherwise
        """
        pass


class MockProviderAdapter(ProviderAdapter):
    """
    Mock provider adapter for testing and development.
    """
    
    def __init__(self, api_key: str, **kwargs):
        super().__init__(api_key, **kwargs)
        self.models = kwargs.get("models", ["mock-gpt-3.5", "mock-gpt-4"])
    
    def chat_completion(self, request: ChatRequest) -> ChatResponse:
        """Execute a mock chat completion."""
        # Simple mock response
        return ChatResponse(
            content=f"Mock response from {request.model or self.models[0]}",
            model=request.model or self.models[0],
            tokens_used=50,
            finish_reason="stop"
        )
    
    def get_available_models(self) -> List[str]:
        """Get mock available models."""
        return self.models
    
    def validate_api_key(self) -> bool:
        """Mock API key validation."""
        return len(self.api_key) > 0
