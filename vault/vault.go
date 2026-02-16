package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Vault manages encrypted storage of sensitive data like API keys
type Vault struct {
	key    []byte
	gcm    cipher.AEAD
	values map[string][]byte
}

// NewVault creates a new vault with AES-256 encryption
func NewVault(encryptionKey []byte) (*Vault, error) {
	if len(encryptionKey) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes for AES-256")
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	return &Vault{
		key:    encryptionKey,
		gcm:    gcm,
		values: make(map[string][]byte),
	}, nil
}

// Set encrypts and stores a value
func (v *Vault) Set(key, value string) error {
	nonce := make([]byte, v.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("failed to generate nonce: %w", err)
	}

	encrypted := v.gcm.Seal(nonce, nonce, []byte(value), nil)
	v.values[key] = encrypted
	return nil
}

// Get decrypts and retrieves a value
func (v *Vault) Get(key string) (string, error) {
	encrypted, exists := v.values[key]
	if !exists {
		return "", fmt.Errorf("key not found: %s", key)
	}

	nonceSize := v.gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return "", fmt.Errorf("invalid encrypted data")
	}

	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	plaintext, err := v.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// Delete removes a value from the vault
func (v *Vault) Delete(key string) {
	delete(v.values, key)
}

// Export exports the encrypted vault data (for persistence)
func (v *Vault) Export() map[string]string {
	exported := make(map[string]string)
	for k, v := range v.values {
		exported[k] = base64.StdEncoding.EncodeToString(v)
	}
	return exported
}

// Import imports encrypted vault data
func (v *Vault) Import(data map[string]string) error {
	for k, encValue := range data {
		decoded, err := base64.StdEncoding.DecodeString(encValue)
		if err != nil {
			return fmt.Errorf("failed to decode key %s: %w", k, err)
		}
		v.values[k] = decoded
	}
	return nil
}
