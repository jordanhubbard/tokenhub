package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllow(t *testing.T) {
	l := New(5, 5, time.Second)

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
