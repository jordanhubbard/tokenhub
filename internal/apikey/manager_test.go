package apikey

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	s, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewManager(s)
}

func TestGenerate(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	plaintext, rec, err := mgr.Generate(ctx, "test-key", `["chat","plan"]`, 30, nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Key should have the tokenhub_ prefix.
	if !strings.HasPrefix(plaintext, "tokenhub_") {
		t.Errorf("expected tokenhub_ prefix, got %s", plaintext[:10])
	}

	// Key should be 9 (prefix) + 64 (32 hex bytes) = 73 chars.
	if len(plaintext) != 73 {
		t.Errorf("expected key length 73, got %d", len(plaintext))
	}

	if rec.Name != "test-key" {
		t.Errorf("expected name test-key, got %s", rec.Name)
	}
	if rec.RotationDays != 30 {
		t.Errorf("expected rotation_days 30, got %d", rec.RotationDays)
	}
	if !rec.Enabled {
		t.Error("expected enabled")
	}
	if rec.KeyPrefix != plaintext[:17] { // tokenhub_ (9) + 8 chars
		t.Errorf("expected prefix %s, got %s", plaintext[:17], rec.KeyPrefix)
	}
}

func TestValidate(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	plaintext, _, err := mgr.Generate(ctx, "test-key", `["chat","plan"]`, 0, nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Valid key should work.
	rec, err := mgr.Validate(ctx, plaintext)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if rec.Name != "test-key" {
		t.Errorf("expected name test-key, got %s", rec.Name)
	}

	// Invalid key should fail.
	_, err = mgr.Validate(ctx, "tokenhub_invalid")
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestValidateExpiredKey(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	// Create a key that expired in the past.
	expired := time.Now().Add(-1 * time.Hour)
	plaintext, _, err := mgr.Generate(ctx, "expired-key", `["chat"]`, 0, &expired)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	_, err = mgr.Validate(ctx, plaintext)
	if err == nil {
		t.Error("expected error for expired key")
	}
	if err.Error() != "api key expired" {
		t.Errorf("expected 'api key expired', got %s", err.Error())
	}
}

func TestValidateDisabledKey(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	plaintext, rec, err := mgr.Generate(ctx, "disabled-key", `["chat"]`, 0, nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Disable the key.
	rec.Enabled = false
	if err := mgr.store.UpdateAPIKey(ctx, *rec); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// Clear cache.
	mgr.mu.Lock()
	mgr.cache = make(map[string]cachedKey)
	mgr.mu.Unlock()

	_, err = mgr.Validate(ctx, plaintext)
	if err == nil {
		t.Error("expected error for disabled key")
	}
}

func TestRotate(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	oldPlaintext, rec, err := mgr.Generate(ctx, "rotate-key", `["chat"]`, 0, nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	newPlaintext, err := mgr.Rotate(ctx, rec.ID)
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}

	// New key should be different.
	if newPlaintext == oldPlaintext {
		t.Error("expected different key after rotation")
	}

	// New key should work.
	_, err = mgr.Validate(ctx, newPlaintext)
	if err != nil {
		t.Fatalf("validate new key failed: %v", err)
	}

	// Old key should not work (clear cache first).
	mgr.mu.Lock()
	mgr.cache = make(map[string]cachedKey)
	mgr.mu.Unlock()

	_, err = mgr.Validate(ctx, oldPlaintext)
	if err == nil {
		t.Error("expected error for old key after rotation")
	}
}

func TestRotateNotFound(t *testing.T) {
	mgr := newTestManager(t)
	_, err := mgr.Rotate(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent key")
	}
}

func TestCheckScope(t *testing.T) {
	rec := &store.APIKeyRecord{Scopes: `["chat"]`}

	if !CheckScope(rec, "/v1/chat") {
		t.Error("expected chat scope to allow /v1/chat")
	}
	if CheckScope(rec, "/v1/plan") {
		t.Error("expected chat-only scope to deny /v1/plan")
	}

	// Both scopes.
	rec.Scopes = `["chat","plan"]`
	if !CheckScope(rec, "/v1/chat") {
		t.Error("expected both scopes to allow /v1/chat")
	}
	if !CheckScope(rec, "/v1/plan") {
		t.Error("expected both scopes to allow /v1/plan")
	}

	// Empty scopes = allow all.
	rec.Scopes = ""
	if !CheckScope(rec, "/v1/chat") {
		t.Error("expected empty scopes to allow /v1/chat")
	}
}

func TestValidateCache(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	plaintext, _, err := mgr.Generate(ctx, "cache-key", `["chat"]`, 0, nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// First validation populates cache.
	_, err = mgr.Validate(ctx, plaintext)
	if err != nil {
		t.Fatalf("first validate failed: %v", err)
	}

	// Second validation should hit cache (no bcrypt).
	rec, err := mgr.Validate(ctx, plaintext)
	if err != nil {
		t.Fatalf("cached validate failed: %v", err)
	}
	if rec.Name != "cache-key" {
		t.Errorf("expected cache-key, got %s", rec.Name)
	}
}

// newTestManagerWithStore creates a Manager and returns both the manager and
// the underlying store for direct manipulation in tests.
func newTestManagerWithStore(t *testing.T) (*Manager, *store.SQLiteStore) {
	t.Helper()
	s := newTestStore(t)
	return NewManager(s), s
}

func TestEnforceRotation_DisablesExpiredKeys(t *testing.T) {
	mgr, s := newTestManagerWithStore(t)
	ctx := context.Background()
	logger := slog.Default()
	bus := events.NewBus()

	// Create a key with rotation_days=1 that was created 2 days ago.
	expired := store.APIKeyRecord{
		ID:           "key-expired",
		KeyHash:      "$2a$10$fakehash",
		KeyPrefix:    "tokenhub_aaaaaaaa",
		Name:         "expired-rotation-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, expired); err != nil {
		t.Fatalf("create expired key failed: %v", err)
	}

	// Create a fresh key that should not be affected.
	fresh := store.APIKeyRecord{
		ID:           "key-fresh",
		KeyHash:      "$2a$10$fakehash2",
		KeyPrefix:    "tokenhub_bbbbbbbb",
		Name:         "fresh-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC(),
		RotationDays: 90,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, fresh); err != nil {
		t.Fatalf("create fresh key failed: %v", err)
	}

	count, err := mgr.EnforceRotation(ctx, bus, logger)
	if err != nil {
		t.Fatalf("enforce rotation failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 disabled key, got %d", count)
	}

	// Verify the expired key was disabled.
	got, err := s.GetAPIKey(ctx, "key-expired")
	if err != nil {
		t.Fatalf("get expired key failed: %v", err)
	}
	if got.Enabled {
		t.Error("expected expired key to be disabled")
	}

	// Verify the fresh key is still enabled.
	got, err = s.GetAPIKey(ctx, "key-fresh")
	if err != nil {
		t.Fatalf("get fresh key failed: %v", err)
	}
	if !got.Enabled {
		t.Error("expected fresh key to still be enabled")
	}
}

func TestEnforceRotation_NoExpiredKeys(t *testing.T) {
	mgr, s := newTestManagerWithStore(t)
	ctx := context.Background()
	logger := slog.Default()

	// Create only a fresh key.
	fresh := store.APIKeyRecord{
		ID:           "key-fresh",
		KeyHash:      "$2a$10$fakehash",
		KeyPrefix:    "tokenhub_aaaaaaaa",
		Name:         "fresh-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC(),
		RotationDays: 90,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, fresh); err != nil {
		t.Fatalf("create fresh key failed: %v", err)
	}

	count, err := mgr.EnforceRotation(ctx, nil, logger)
	if err != nil {
		t.Fatalf("enforce rotation failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 disabled keys, got %d", count)
	}

	// Verify key still enabled.
	got, err := s.GetAPIKey(ctx, "key-fresh")
	if err != nil {
		t.Fatalf("get key failed: %v", err)
	}
	if !got.Enabled {
		t.Error("expected key to still be enabled")
	}
}

func TestEnforceRotation_EmitsEvent(t *testing.T) {
	mgr, s := newTestManagerWithStore(t)
	ctx := context.Background()
	logger := slog.Default()
	bus := events.NewBus()

	// Subscribe to events.
	sub := bus.Subscribe(10)
	defer bus.Unsubscribe(sub)

	// Create an expired key.
	expired := store.APIKeyRecord{
		ID:           "key-event",
		KeyHash:      "$2a$10$fakehash",
		KeyPrefix:    "tokenhub_eeeeeeee",
		Name:         "event-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, expired); err != nil {
		t.Fatalf("create expired key failed: %v", err)
	}

	count, err := mgr.EnforceRotation(ctx, bus, logger)
	if err != nil {
		t.Fatalf("enforce rotation failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 disabled key, got %d", count)
	}

	// Check that an event was emitted.
	select {
	case evt := <-sub.C:
		if evt.Type != events.EventKeyRotationExpired {
			t.Errorf("expected event type %s, got %s", events.EventKeyRotationExpired, evt.Type)
		}
		if evt.APIKeyName != "event-key" {
			t.Errorf("expected api_key_name event-key, got %s", evt.APIKeyName)
		}
		if evt.Reason == "" {
			t.Error("expected non-empty reason in event")
		}
	default:
		t.Error("expected event to be published, but channel was empty")
	}
}

func TestEnforceRotation_NilBusDoesNotPanic(t *testing.T) {
	mgr, s := newTestManagerWithStore(t)
	ctx := context.Background()
	logger := slog.Default()

	// Create an expired key.
	expired := store.APIKeyRecord{
		ID:           "key-nil-bus",
		KeyHash:      "$2a$10$fakehash",
		KeyPrefix:    "tokenhub_ffffffff",
		Name:         "nil-bus-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, expired); err != nil {
		t.Fatalf("create expired key failed: %v", err)
	}

	// Should not panic with nil bus.
	count, err := mgr.EnforceRotation(ctx, nil, logger)
	if err != nil {
		t.Fatalf("enforce rotation failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 disabled key, got %d", count)
	}
}

func TestEnforceRotation_InvalidatesCachedKeys(t *testing.T) {
	mgr, s := newTestManagerWithStore(t)
	ctx := context.Background()
	logger := slog.Default()

	// Create an expired key and pre-populate cache.
	expired := store.APIKeyRecord{
		ID:           "key-cached",
		KeyHash:      "$2a$10$fakehash",
		KeyPrefix:    "tokenhub_gggggggg",
		Name:         "cached-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, expired); err != nil {
		t.Fatalf("create expired key failed: %v", err)
	}

	// Manually insert a cache entry for this key.
	mgr.mu.Lock()
	mgr.cache["fake-cache-key"] = cachedKey{
		record:    &expired,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	mgr.mu.Unlock()

	count, err := mgr.EnforceRotation(ctx, nil, logger)
	if err != nil {
		t.Fatalf("enforce rotation failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 disabled key, got %d", count)
	}

	// Verify the cache entry was removed.
	mgr.mu.RLock()
	_, found := mgr.cache["fake-cache-key"]
	mgr.mu.RUnlock()
	if found {
		t.Error("expected cache entry to be invalidated after rotation enforcement")
	}
}
