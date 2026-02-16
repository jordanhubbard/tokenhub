"""
Provider registry for managing multiple AI providers
"""
import os
from typing import Dict, Optional, List
from .providers import ProviderAdapter, MockProviderAdapter
from .vault import SecureVault


class ProviderRegistry:
    """
    Registry for managing AI provider adapters and their API keys.
    """
    
    def __init__(self, vault: Optional[SecureVault] = None):
        """
        Initialize the provider registry.
        
        Args:
            vault: Optional secure vault for storing API keys
        """
        self.vault = vault
        self._providers: Dict[str, ProviderAdapter] = {}
        self._provider_classes: Dict[str, type] = {
            "mock": MockProviderAdapter,
        }
    
    def register_provider_class(self, name: str, provider_class: type) -> None:
        """
        Register a provider adapter class.
        
        Args:
            name: Provider name
            provider_class: Provider adapter class
        """
        self._provider_classes[name] = provider_class
    
    def add_provider(
        self,
        name: str,
        provider_type: str,
        api_key: Optional[str] = None,
        store_in_vault: bool = True,
        **config
    ) -> None:
        """
        Add a provider to the registry.
        
        Args:
            name: Unique name for this provider instance
            provider_type: Type of provider (e.g., 'openai', 'anthropic')
            api_key: API key (if None, will try to get from vault or env)
            store_in_vault: Whether to store the API key in the vault
            **config: Additional provider-specific configuration
        """
        # Get API key from various sources
        if api_key is None:
            # Try vault first
            if self.vault:
                api_key = self.vault.retrieve(f"provider_{name}_api_key")
            # Try environment variable
            if api_key is None:
                api_key = os.getenv(f"{name.upper()}_API_KEY")
        
        if api_key is None:
            raise ValueError(f"No API key provided for provider {name}")
        
        # Store in vault if requested
        if store_in_vault and self.vault:
            self.vault.store(f"provider_{name}_api_key", api_key)
        
        # Get provider class
        provider_class = self._provider_classes.get(provider_type)
        if provider_class is None:
            raise ValueError(f"Unknown provider type: {provider_type}")
        
        # Create provider instance
        provider = provider_class(api_key, **config)
        self._providers[name] = provider
    
    def remove_provider(self, name: str) -> bool:
        """
        Remove a provider from the registry.
        
        Args:
            name: Provider name
            
        Returns:
            True if removed, False if not found
        """
        if name in self._providers:
            del self._providers[name]
            if self.vault:
                self.vault.delete(f"provider_{name}_api_key")
            return True
        return False
    
    def get_provider(self, name: str) -> Optional[ProviderAdapter]:
        """
        Get a provider by name.
        
        Args:
            name: Provider name
            
        Returns:
            Provider adapter or None if not found
        """
        return self._providers.get(name)
    
    def list_providers(self) -> List[str]:
        """
        List all registered providers.
        
        Returns:
            List of provider names
        """
        return list(self._providers.keys())
    
    def validate_all_providers(self) -> Dict[str, bool]:
        """
        Validate all registered providers.
        
        Returns:
            Dictionary mapping provider names to validation status
        """
        return {
            name: provider.validate_api_key()
            for name, provider in self._providers.items()
        }
