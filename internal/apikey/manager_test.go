package apikey

import (
	"context"
	"strings"
	"testing"
	"time"

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
	t.Cleanup(func() { s.Close() })
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
