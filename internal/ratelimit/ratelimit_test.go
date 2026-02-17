package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllow(t *testing.T) {
	l := New(5, 5, time.Second)
	defer l.Stop()

	// Should allow up to burst.
	for i := range 5 {
		if !l.allow("test") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// Next should be denied.
	if l.allow("test") {
		t.Fatal("request 6 should be denied")
	}
}

func TestRefill(t *testing.T) {
	l := New(10, 10, 50*time.Millisecond)
	defer l.Stop()

	// Exhaust tokens.
	for range 10 {
		l.allow("test")
	}
	if l.allow("test") {
		t.Fatal("should be denied after exhaustion")
	}

	// Wait for refill.
	time.Sleep(60 * time.Millisecond)

	if !l.allow("test") {
		t.Fatal("should be allowed after refill")
	}
}

func TestDifferentIPs(t *testing.T) {
	l := New(1, 1, time.Second)
	defer l.Stop()

	if !l.allow("ip1") {
		t.Fatal("ip1 should be allowed")
	}
	if l.allow("ip1") {
		t.Fatal("ip1 should be denied")
	}
	// Different IP has its own bucket.
	if !l.allow("ip2") {
		t.Fatal("ip2 should be allowed")
	}
}

func TestMiddleware(t *testing.T) {
	l := New(2, 2, time.Second)
	defer l.Stop()

	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 2 {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Real-IP", "10.0.0.1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// Third request should be rate limited.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "10.0.0.1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
}

func TestEvictionRemovesLRU(t *testing.T) {
	// Create a limiter with maxKeys=3 so eviction triggers on the 4th key.
	l := New(1, 1, time.Hour, WithMaxKeys(3))
	defer l.Stop()

	// Access keys in order: A, B, C.
	l.allow("A")
	l.allow("B")
	l.allow("C")

	// All three keys should be present.
	l.mu.Lock()
	if len(l.buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(l.buckets))
	}
	l.mu.Unlock()

	// Access A again so it becomes most recently used.
	// Order is now (front->back): A, C, B. B is the LRU.
	l.allow("A")

	// Adding D should evict B (the least recently used).
	l.allow("D")

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) != 3 {
		t.Fatalf("expected 3 buckets after eviction, got %d", len(l.buckets))
	}

	// B should have been evicted.
	if _, ok := l.buckets["B"]; ok {
		t.Error("expected B to be evicted (least recently used)")
	}

	// A, C, D should still be present.
	for _, key := range []string{"A", "C", "D"} {
		if _, ok := l.buckets[key]; !ok {
			t.Errorf("expected %s to still be present", key)
		}
	}
}

func TestEvictionWithAccessPattern(t *testing.T) {
	// Verify that accessing a key moves it to the front, preventing eviction.
	l := New(10, 10, time.Hour, WithMaxKeys(2))
	defer l.Stop()

	l.allow("X")
	l.allow("Y")

	// Access X to make it most recently used. Y is now LRU.
	l.allow("X")

	// Adding Z should evict Y (not X).
	l.allow("Z")

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.buckets["Y"]; ok {
		t.Error("expected Y to be evicted")
	}
	if _, ok := l.buckets["X"]; !ok {
		t.Error("expected X to still be present (was recently accessed)")
	}
	if _, ok := l.buckets["Z"]; !ok {
		t.Error("expected Z to still be present (just added)")
	}
}
