package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetup_Disabled(t *testing.T) {
	shutdown, err := Setup(Config{Enabled: false})
	if err != nil {
		t.Fatalf("Setup(disabled) returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup(disabled) returned nil shutdown func")
	}
	// Calling shutdown on a disabled config should be a no-op.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestSetup_Enabled(t *testing.T) {
	// Use a dummy endpoint — the exporter will fail to connect but
	// Setup should still succeed (batching is async).
	shutdown, err := Setup(Config{
		Enabled:     true,
		Endpoint:    "localhost:4318",
		ServiceName: "tokenhub-test",
	})
	if err != nil {
		t.Fatalf("Setup(enabled) returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup(enabled) returned nil shutdown func")
	}
	// Shutdown should not block indefinitely even with no collector.
	ctx, cancel := context.WithTimeout(context.Background(), 2e9) // 2 seconds
	defer cancel()
	_ = shutdown(ctx)
}

func TestMiddleware_WrapsHandler(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := Middleware()
	handler := mw(inner)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler was not called through middleware")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestHTTPTransport_NilBase(t *testing.T) {
	rt := HTTPTransport(nil)
	if rt == nil {
		t.Fatal("HTTPTransport(nil) returned nil")
	}
}

func TestHTTPTransport_CustomBase(t *testing.T) {
	base := http.DefaultTransport
	rt := HTTPTransport(base)
	if rt == nil {
		t.Fatal("HTTPTransport(base) returned nil")
	}
}
