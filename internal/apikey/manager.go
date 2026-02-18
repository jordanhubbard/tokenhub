package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordanhubbard/tokenhub/internal/events"
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
	cache map[string]cachedKey // SHA-256 hash of key -> cached record
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
	// Check cache first (keyed by SHA-256 hash, not plaintext).
	cacheKey := string(hashForBcrypt(keyString))
	m.mu.RLock()
	if cached, ok := m.cache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		m.mu.RUnlock()
		return cached.record, nil
	}
	m.mu.RUnlock()

	// Extract prefix for indexed lookup.
	if len(keyString) < len(keyPrefix)+8 {
		return nil, errors.New("invalid api key")
	}
	prefix := keyString[:len(keyPrefix)+8]

	keys, err := m.store.GetAPIKeysByPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("lookup keys: %w", err)
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

		// Cache the result (deep copy to prevent mutation of cached data).
		cachedRecord := *k
		m.mu.Lock()
		m.cache[cacheKey] = cachedKey{
			record:    &cachedRecord,
			expiresAt: time.Now().Add(cacheTTL),
		}
		m.mu.Unlock()

		return &cachedRecord, nil
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
	scope := routeToScope(endpoint)
	if scope == "" {
		return false // deny unknown endpoints by default
	}
	if record.Scopes == "" || record.Scopes == "[]" {
		return true // empty scopes = allow all
	}
	var scopes []string
	if err := json.Unmarshal([]byte(record.Scopes), &scopes); err != nil {
		return false // malformed JSON = deny
	}
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func routeToScope(endpoint string) string {
	switch endpoint {
	case "/v1/chat", "/v1/chat/completions":
		return "chat"
	case "/v1/plan":
		return "plan"
	default:
		return ""
	}
}

// EnforceRotation finds all API keys that have exceeded their rotation period
// and disables them. It logs a warning for each disabled key and emits an event
// on the provided EventBus (if non-nil). Returns the count of keys that were
// disabled.
func (m *Manager) EnforceRotation(ctx context.Context, bus *events.Bus, logger *slog.Logger) (int, error) {
	expired, err := m.store.ListExpiredRotationKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("list expired rotation keys: %w", err)
	}

	disabled := 0
	for i := range expired {
		k := &expired[i]
		k.Enabled = false
		if err := m.store.UpdateAPIKey(ctx, *k); err != nil {
			logger.Error("failed to disable expired rotation key",
				slog.String("key_id", k.ID),
				slog.String("key_name", k.Name),
				slog.String("error", err.Error()),
			)
			continue
		}

		// Invalidate any cached entries for this key.
		m.mu.Lock()
		for ck, cv := range m.cache {
			if cv.record.ID == k.ID {
				delete(m.cache, ck)
			}
		}
		m.mu.Unlock()

		logger.Warn("disabled API key: rotation period exceeded",
			slog.String("key_id", k.ID),
			slog.String("key_name", k.Name),
			slog.Int("rotation_days", k.RotationDays),
			slog.Time("created_at", k.CreatedAt),
		)

		if bus != nil {
			bus.Publish(events.Event{
				Type:       events.EventKeyRotationExpired,
				Timestamp:  time.Now().UTC(),
				APIKeyName: k.Name,
				Reason:     fmt.Sprintf("key %q exceeded %d-day rotation period", k.Name, k.RotationDays),
			})
		}

		disabled++
	}

	return disabled, nil
}
