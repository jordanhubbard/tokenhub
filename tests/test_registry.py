"""
Tests for the provider registry
"""
import pytest
from tokenhub.registry import ProviderRegistry
from tokenhub.vault import SecureVault


def test_add_provider():
    """Test adding a provider."""
    vault = SecureVault("test-password")
    registry = ProviderRegistry(vault)
    
    registry.add_provider(
        name="test-provider",
        provider_type="mock",
        api_key="test-key-123"
    )
    
    provider = registry.get_provider("test-provider")
    assert provider is not None
    assert provider.api_key == "test-key-123"


def test_add_provider_stores_in_vault():
    """Test that API keys are stored in vault."""
    vault = SecureVault("test-password")
    registry = ProviderRegistry(vault)
    
    registry.add_provider(
        name="test-provider",
        provider_type="mock",
        api_key="test-key-123",
        store_in_vault=True
    )
    
    # Check vault
    stored_key = vault.retrieve("provider_test-provider_api_key")
    assert stored_key == "test-key-123"


def test_list_providers():
    """Test listing providers."""
    registry = ProviderRegistry()
    
    registry.add_provider("provider1", "mock", api_key="key1", store_in_vault=False)
    registry.add_provider("provider2", "mock", api_key="key2", store_in_vault=False)
    
    providers = registry.list_providers()
    assert "provider1" in providers
    assert "provider2" in providers


def test_remove_provider():
    """Test removing a provider."""
    vault = SecureVault("test-password")
    registry = ProviderRegistry(vault)
    
    registry.add_provider("test-provider", "mock", api_key="key1")
    
    assert registry.remove_provider("test-provider") is True
    assert registry.get_provider("test-provider") is None
    assert vault.retrieve("provider_test-provider_api_key") is None


def test_validate_providers():
    """Test validating all providers."""
    registry = ProviderRegistry()
    
    registry.add_provider("provider1", "mock", api_key="key1", store_in_vault=False)
    registry.add_provider("provider2", "mock", api_key="key2", store_in_vault=False)
    
    results = registry.validate_all_providers()
    
    assert results["provider1"] is True
    assert results["provider2"] is True
