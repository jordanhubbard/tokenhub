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

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommended minimums).
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

// Vault provides encrypted credential storage with a lock/unlock lifecycle.
// API keys and other secrets are encrypted at rest using AES-256-GCM.
// Key derivation uses Argon2id.
type Vault struct {
	enabled bool

	mu     sync.RWMutex
	locked bool

	// salt for Argon2id (persisted alongside encrypted data)
	salt []byte

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

	if len(master) < 8 {
		return errors.New("password too short")
	}

	// Generate salt on first unlock; reuse existing salt on subsequent unlocks.
	if v.salt == nil {
		v.salt = make([]byte, saltLen)
		if _, err := io.ReadFull(rand.Reader, v.salt); err != nil {
			return fmt.Errorf("failed to generate salt: %w", err)
		}
	}

	v.key = argon2.IDKey(master, v.salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
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

// Salt returns the vault salt (for persistence). Returns nil if no salt yet.
func (v *Vault) Salt() []byte {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.salt == nil {
		return nil
	}
	s := make([]byte, len(v.salt))
	copy(s, v.salt)
	return s
}

// SetSalt restores a previously persisted salt (call before Unlock).
func (v *Vault) SetSalt(salt []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.salt = make([]byte, len(salt))
	copy(v.salt, salt)
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
