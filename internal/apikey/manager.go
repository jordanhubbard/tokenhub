package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordanhubbard/tokenhub/internal/store"
)

// hashForBcrypt pre-hashes a key with SHA-256 to stay within bcrypt's 72-byte limit.
func hashForBcrypt(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return []byte(hex.EncodeToString(h[:]))
}

const (
	keyPrefix    = "tokenhub_"
	keyRandBytes = 32 // 32 hex chars
	bcryptCost   = 10
	cacheTTL     = 5 * time.Minute
)

type cachedKey struct {
	record    *store.APIKeyRecord
	expiresAt time.Time
}

// Manager handles API key generation, validation, and rotation.
type Manager struct {
	store store.Store

	mu    sync.RWMutex
	cache map[string]cachedKey // keyString -> cached record
}

// NewManager creates a new API key manager.
func NewManager(s store.Store) *Manager {
	return &Manager{
		store: s,
		cache: make(map[string]cachedKey),
	}
}

// Generate creates a new API key, stores its bcrypt hash, and returns the
// plaintext key exactly once.
func (m *Manager) Generate(ctx context.Context, name string, scopes string, rotationDays int, expiresAt *time.Time) (string, *store.APIKeyRecord, error) {
	raw := make([]byte, keyRandBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate random: %w", err)
	}
	plaintext := keyPrefix + hex.EncodeToString(raw)

	hash, err := bcrypt.GenerateFromPassword(hashForBcrypt(plaintext), bcryptCost)
	if err != nil {
		return "", nil, fmt.Errorf("bcrypt hash: %w", err)
	}

	id := hex.EncodeToString(raw[:8]) // 16-char hex ID
	rec := store.APIKeyRecord{
		ID:           id,
		KeyHash:      string(hash),
		KeyPrefix:    plaintext[:len(keyPrefix)+8],
		Name:         name,
		Scopes:       scopes,
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
		RotationDays: rotationDays,
		Enabled:      true,
	}

	if err := m.store.CreateAPIKey(ctx, rec); err != nil {
		return "", nil, fmt.Errorf("store api key: %w", err)
	}
	return plaintext, &rec, nil
}

// Validate checks a plaintext API key and returns the associated record.
// Uses a short TTL cache to avoid bcrypt on every request.
func (m *Manager) Validate(ctx context.Context, keyString string) (*store.APIKeyRecord, error) {
	// Check cache first.
	m.mu.RLock()
	if cached, ok := m.cache[keyString]; ok && time.Now().Before(cached.expiresAt) {
		m.mu.RUnlock()
		return cached.record, nil
	}
	m.mu.RUnlock()

	// Load all enabled keys and bcrypt-compare.
	keys, err := m.store.ListAPIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}

	for i := range keys {
		k := &keys[i]
		if !k.Enabled {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(k.KeyHash), hashForBcrypt(keyString)); err != nil {
			continue
		}
		// Found a match — check expiry.
		if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			return nil, errors.New("api key expired")
		}
		// Update last_used_at.
		now := time.Now().UTC()
		k.LastUsedAt = &now
		_ = m.store.UpdateAPIKey(ctx, *k)

		// Cache the result.
		m.mu.Lock()
		m.cache[keyString] = cachedKey{
			record:    k,
			expiresAt: time.Now().Add(cacheTTL),
		}
		m.mu.Unlock()

		return k, nil
	}

	return nil, errors.New("invalid api key")
}

// Rotate generates a new key for an existing key record, replacing the hash.
// Returns the new plaintext key exactly once.
func (m *Manager) Rotate(ctx context.Context, id string) (string, error) {
	rec, err := m.store.GetAPIKey(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get key: %w", err)
	}
	if rec == nil {
		return "", errors.New("api key not found")
	}

	raw := make([]byte, keyRandBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random: %w", err)
	}
	plaintext := keyPrefix + hex.EncodeToString(raw)

	hash, err := bcrypt.GenerateFromPassword(hashForBcrypt(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash: %w", err)
	}

	rec.KeyHash = string(hash)
	rec.KeyPrefix = plaintext[:len(keyPrefix)+8]

	if err := m.store.UpdateAPIKey(ctx, *rec); err != nil {
		return "", fmt.Errorf("update key: %w", err)
	}

	// Invalidate cache entries that matched the old key.
	m.mu.Lock()
	for k, v := range m.cache {
		if v.record.ID == id {
			delete(m.cache, k)
		}
	}
	m.mu.Unlock()

	return plaintext, nil
}

// CheckScope checks if a key's scopes allow access to the given endpoint.
func CheckScope(record *store.APIKeyRecord, endpoint string) bool {
	// Simple scope check: scopes is a JSON array like ["chat","plan"].
	// For /v1/chat -> need "chat", for /v1/plan -> need "plan".
	scopes := record.Scopes
	switch endpoint {
	case "/v1/chat":
		return contains(scopes, "chat")
	case "/v1/plan":
		return contains(scopes, "plan")
	default:
		// Allow by default for unknown endpoints (admin routes have separate auth).
		return true
	}
}

func contains(scopes, scope string) bool {
	// Simple string search in the JSON array text.
	return len(scopes) == 0 || // empty scopes = allow all
		scopes == "[]" || // explicit empty = allow all
		stringContains(scopes, `"`+scope+`"`)
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
