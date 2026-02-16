package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Vault provides encrypted credential storage with a lock/unlock lifecycle.
// API keys and other secrets are encrypted at rest using AES-256-GCM.
type Vault struct {
	enabled bool

	mu     sync.RWMutex
	locked bool

	// derived key (in-memory only; cleared on lock)
	key []byte

	// encrypted KV store
	values map[string][]byte
}

func New(enabled bool) (*Vault, error) {
	return &Vault{
		enabled: enabled,
		locked:  enabled, // locked on start if enabled
		values:  make(map[string][]byte),
	}, nil
}

func (v *Vault) IsLocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.enabled && v.locked
}

func (v *Vault) Unlock(master []byte) error {
	if !v.enabled {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// TODO: derive key via argon2id with stored salt.
	// Placeholder: pad/truncate master to 32 bytes.
	if len(master) < 8 {
		return errors.New("password too short")
	}
	v.key = make([]byte, 32)
	copy(v.key, master)
	v.locked = false
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.key {
		v.key[i] = 0
	}
	v.key = nil
	v.locked = true
}

// Set encrypts and stores a value.
func (v *Vault) Set(key, value string) error {
	encrypted, err := v.Encrypt([]byte(value))
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.values[key] = encrypted
	v.mu.Unlock()
	return nil
}

// Get decrypts and retrieves a value.
func (v *Vault) Get(key string) (string, error) {
	v.mu.RLock()
	encrypted, exists := v.values[key]
	v.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("key not found: %s", key)
	}

	plaintext, err := v.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}
	return string(plaintext), nil
}

// Delete removes a value from the vault.
func (v *Vault) Delete(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, key)
}

// Export exports the encrypted vault data (for persistence).
func (v *Vault) Export() map[string]string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	exported := make(map[string]string, len(v.values))
	for k, val := range v.values {
		exported[k] = base64.StdEncoding.EncodeToString(val)
	}
	return exported
}

// Import imports encrypted vault data.
func (v *Vault) Import(data map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, encValue := range data {
		decoded, err := base64.StdEncoding.DecodeString(encValue)
		if err != nil {
			return fmt.Errorf("failed to decode key %s: %w", k, err)
		}
		v.values[k] = decoded
	}
	return nil
}

func (v *Vault) Encrypt(plaintext []byte) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.enabled && v.locked {
		return nil, errors.New("vault locked")
	}
	if len(v.key) != 32 {
		return nil, errors.New("no key")
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := gcm.Seal(nonce, nonce, plaintext, nil)
	return out, nil
}

func (v *Vault) Decrypt(ciphertext []byte) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.enabled && v.locked {
		return nil, errors.New("vault locked")
	}
	if len(v.key) != 32 {
		return nil, errors.New("no key")
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	data := ciphertext[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, err
	}
	return plain, nil
}
