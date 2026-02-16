"""
Secure Vault for API key storage with AES-256 encryption
"""
import os
import json
from typing import Dict, Optional
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC
import base64


class SecureVault:
    """
    Secure vault for storing API keys and sensitive data with AES-256 encryption.
    """
    
    def __init__(self, password: str, salt: Optional[bytes] = None):
        """
        Initialize the vault with a password.
        
        Args:
            password: Admin password for encryption/decryption
            salt: Optional salt for key derivation (generated if not provided)
        """
        self.password = password
        self.salt = salt or os.urandom(16)
        self.key = self._derive_key(password, self.salt)
        self._data: Dict[str, str] = {}
        
    def _derive_key(self, password: str, salt: bytes) -> bytes:
        """Derive encryption key from password using PBKDF2."""
        kdf = PBKDF2HMAC(
            algorithm=hashes.SHA256(),
            length=32,  # 256 bits for AES-256
            salt=salt,
            iterations=100000,
            backend=default_backend()
        )
        return kdf.derive(password.encode())
    
    def _encrypt(self, plaintext: str) -> str:
        """Encrypt plaintext using AES-256-CBC."""
        iv = os.urandom(16)
        cipher = Cipher(
            algorithms.AES(self.key),
            modes.CBC(iv),
            backend=default_backend()
        )
        encryptor = cipher.encryptor()
        
        # Pad plaintext to block size
        padding_length = 16 - (len(plaintext) % 16)
        padded_plaintext = plaintext + (chr(padding_length) * padding_length)
        
        ciphertext = encryptor.update(padded_plaintext.encode()) + encryptor.finalize()
        
        # Return base64 encoded iv + ciphertext
        return base64.b64encode(iv + ciphertext).decode()
    
    def _decrypt(self, encrypted: str) -> str:
        """Decrypt ciphertext using AES-256-CBC."""
        data = base64.b64decode(encrypted.encode())
        iv = data[:16]
        ciphertext = data[16:]
        
        cipher = Cipher(
            algorithms.AES(self.key),
            modes.CBC(iv),
            backend=default_backend()
        )
        decryptor = cipher.decryptor()
        
        padded_plaintext = decryptor.update(ciphertext) + decryptor.finalize()
        
        # Remove padding
        padding_length = padded_plaintext[-1]
        plaintext = padded_plaintext[:-padding_length]
        
        return plaintext.decode()
    
    def store(self, key: str, value: str) -> None:
        """Store a key-value pair in the vault."""
        self._data[key] = self._encrypt(value)
    
    def retrieve(self, key: str) -> Optional[str]:
        """Retrieve a value from the vault."""
        encrypted = self._data.get(key)
        if encrypted is None:
            return None
        return self._decrypt(encrypted)
    
    def delete(self, key: str) -> bool:
        """Delete a key from the vault."""
        if key in self._data:
            del self._data[key]
            return True
        return False
    
    def list_keys(self) -> list:
        """List all keys in the vault."""
        return list(self._data.keys())
    
    def export_encrypted(self) -> Dict:
        """Export vault data in encrypted form."""
        return {
            "salt": base64.b64encode(self.salt).decode(),
            "data": self._data
        }
    
    def import_encrypted(self, exported_data: Dict) -> None:
        """Import vault data from encrypted form."""
        self.salt = base64.b64decode(exported_data["salt"].encode())
        # Re-derive the key with the imported salt
        self.key = self._derive_key(self.password, self.salt)
        self._data = exported_data["data"]
    
    def clear(self) -> None:
        """Clear all data from the vault."""
        self._data.clear()
