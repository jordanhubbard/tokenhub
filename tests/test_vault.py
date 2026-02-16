"""
Tests for the secure vault
"""
import pytest
from tokenhub.vault import SecureVault


def test_vault_store_and_retrieve():
    """Test storing and retrieving values."""
    vault = SecureVault("test-password")
    
    vault.store("test-key", "test-value")
    assert vault.retrieve("test-key") == "test-value"


def test_vault_nonexistent_key():
    """Test retrieving non-existent key."""
    vault = SecureVault("test-password")
    assert vault.retrieve("nonexistent") is None


def test_vault_delete():
    """Test deleting a key."""
    vault = SecureVault("test-password")
    
    vault.store("test-key", "test-value")
    assert vault.delete("test-key") is True
    assert vault.retrieve("test-key") is None
    assert vault.delete("test-key") is False


def test_vault_list_keys():
    """Test listing keys."""
    vault = SecureVault("test-password")
    
    vault.store("key1", "value1")
    vault.store("key2", "value2")
    
    keys = vault.list_keys()
    assert "key1" in keys
    assert "key2" in keys


def test_vault_encryption():
    """Test that values are actually encrypted."""
    vault = SecureVault("test-password")
    
    vault.store("key", "secret-value")
    exported = vault.export_encrypted()
    
    # Encrypted data should not contain the plaintext
    assert "secret-value" not in str(exported["data"])


def test_vault_import_export():
    """Test exporting and importing vault data."""
    vault1 = SecureVault("test-password")
    vault1.store("key1", "value1")
    vault1.store("key2", "value2")
    
    exported = vault1.export_encrypted()
    
    vault2 = SecureVault("test-password")
    vault2.import_encrypted(exported)
    
    assert vault2.retrieve("key1") == "value1"
    assert vault2.retrieve("key2") == "value2"


def test_vault_clear():
    """Test clearing the vault."""
    vault = SecureVault("test-password")
    vault.store("key1", "value1")
    vault.store("key2", "value2")
    
    vault.clear()
    
    assert len(vault.list_keys()) == 0
    assert vault.retrieve("key1") is None
