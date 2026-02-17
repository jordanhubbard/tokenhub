package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Cache unit tests
// ---------------------------------------------------------------------------

func TestCache_SetAndGet(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	c.Set("k1", []byte("body1"), 200, map[string]string{"Content-Type": "application/json"})

	e, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected cache hit for k1")
	}
	if string(e.Response) != "body1" {
		t.Fatalf("unexpected body: %s", e.Response)
	}
	if e.StatusCode != 200 {
		t.Fatalf("unexpected status: %d", e.StatusCode)
	}
	if e.Headers["Content-Type"] != "application/json" {
		t.Fatalf("unexpected header: %s", e.Headers["Content-Type"])
	}
}

func TestCache_Miss(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	if _, ok := c.Get("nonexistent"); ok {
		t.Fatal("expected cache miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	// Use a very short TTL so the entry expires quickly.
	c := New(50*time.Millisecond, 100)
	defer c.Stop()

	c.Set("k1", []byte("body"), 200, nil)

	// Should be a hit immediately.
	if _, ok := c.Get("k1"); !ok {
		t.Fatal("expected cache hit before TTL")
	}

	// Wait for the TTL to expire.
	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get("k1"); ok {
		t.Fatal("expected cache miss after TTL")
	}
}

func TestCache_MaxEntriesEviction(t *testing.T) {
	c := New(time.Minute, 2)
	defer c.Stop()

	c.Set("k1", []byte("body1"), 200, nil)
	time.Sleep(time.Millisecond) // ensure k1 has earliest timestamp
	c.Set("k2", []byte("body2"), 200, nil)
	time.Sleep(time.Millisecond)

	// Adding a third entry should evict the oldest (k1).
	c.Set("k3", []byte("body3"), 200, nil)

	if _, ok := c.Get("k1"); ok {
		t.Fatal("expected k1 to be evicted")
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("expected k2 to still be cached")
	}
	if _, ok := c.Get("k3"); !ok {
		t.Fatal("expected k3 to still be cached")
	}
}

func TestCache_OverwriteExistingKey(t *testing.T) {
	c := New(time.Minute, 2)
	defer c.Stop()

	c.Set("k1", []byte("v1"), 200, nil)
	c.Set("k2", []byte("v2"), 200, nil)

	// Overwriting k1 should not trigger eviction since key already exists.
	c.Set("k1", []byte("v1-updated"), 201, nil)

	e, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected k1 to still exist")
	}
	if string(e.Response) != "v1-updated" {
		t.Fatalf("expected updated body, got: %s", e.Response)
	}
	if e.StatusCode != 201 {
		t.Fatalf("expected status 201, got: %d", e.StatusCode)
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("expected k2 to still exist")
	}
}

func TestCache_PruneRemovesExpired(t *testing.T) {
	c := New(50*time.Millisecond, 100)
	defer c.Stop()

	c.Set("k1", []byte("body"), 200, nil)

	// Wait for TTL to expire, then invoke prune directly.
	time.Sleep(100 * time.Millisecond)
	c.prune()

	c.mu.Lock()
	count := len(c.entries)
	c.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 entries after prune, got %d", count)
	}
}

func TestCache_PruneKeepsValid(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	c.Set("k1", []byte("body"), 200, nil)
	c.prune()

	c.mu.Lock()
	count := len(c.entries)
	c.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 entry after prune (not expired), got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Middleware tests
// ---------------------------------------------------------------------------

func TestMiddleware_PassThroughWithoutHeader(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if rec.Header().Get("Idempotency-Replay") != "" {
		t.Fatal("should not have Idempotency-Replay header on first request without key")
	}
}

func TestMiddleware_CachesAndReplays(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	callCount := 0
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))

	// First request: should call handler.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req1.Header.Set("Idempotency-Key", "req-123")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if callCount != 1 {
		t.Fatalf("expected handler called once, got %d", callCount)
	}
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec1.Code)
	}
	body1, _ := io.ReadAll(rec1.Result().Body)
	if string(body1) != `{"id":"abc"}` {
		t.Fatalf("unexpected body: %s", body1)
	}
	if rec1.Header().Get("Idempotency-Replay") != "" {
		t.Fatal("first request should not have Idempotency-Replay")
	}

	// Second request with same key: should return cached response.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req2.Header.Set("Idempotency-Key", "req-123")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 1 {
		t.Fatalf("expected handler NOT called again, got %d calls", callCount)
	}
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected cached 201, got %d", rec2.Code)
	}
	body2, _ := io.ReadAll(rec2.Result().Body)
	if string(body2) != `{"id":"abc"}` {
		t.Fatalf("unexpected cached body: %s", body2)
	}
	if rec2.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("replayed response should have Idempotency-Replay: true")
	}
	if rec2.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected cached Content-Type header, got: %s", rec2.Header().Get("Content-Type"))
	}
}

func TestMiddleware_DifferentKeysAreSeparate(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	callCount := 0
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response"))
	}))

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req1.Header.Set("Idempotency-Key", "key-a")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req2.Header.Set("Idempotency-Key", "key-b")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 2 {
		t.Fatalf("expected handler called twice for different keys, got %d", callCount)
	}
}
