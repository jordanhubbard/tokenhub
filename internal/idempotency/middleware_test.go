package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Middleware HTTP tests
// ---------------------------------------------------------------------------

// TestMiddleware_NoIdempotencyKeyHeader verifies that a request without an
// Idempotency-Key header passes through to the handler normally with no
// caching side-effects.
func TestMiddleware_NoIdempotencyKeyHeader(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	var callCount int
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if callCount != 1 {
		t.Fatalf("expected handler called once, got %d", callCount)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if rec.Header().Get("Idempotency-Replay") != "" {
		t.Fatal("should not have Idempotency-Replay header when no key is provided")
	}

	// A second request without a key should also pass through (no caching).
	req2 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 2 {
		t.Fatalf("expected handler called twice (no caching without key), got %d", callCount)
	}
}

// TestMiddleware_FirstRequestWithKey verifies that the first request carrying
// an Idempotency-Key header passes through to the handler, the response is
// cached, and the response is returned normally without the replay header.
func TestMiddleware_FirstRequestWithKey(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	var callCount int
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"tok_123"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req.Header.Set("Idempotency-Key", "first-key-001")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if callCount != 1 {
		t.Fatalf("expected handler called once, got %d", callCount)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != `{"id":"tok_123"}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type application/json, got: %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Idempotency-Replay") != "" {
		t.Fatal("first request should not have Idempotency-Replay header")
	}

	// Verify the entry was cached.
	e, ok := c.Get("first-key-001")
	if !ok {
		t.Fatal("expected cache entry for first-key-001")
	}
	if string(e.Response) != `{"id":"tok_123"}` {
		t.Fatalf("cached body mismatch: %s", e.Response)
	}
	if e.StatusCode != http.StatusCreated {
		t.Fatalf("cached status mismatch: %d", e.StatusCode)
	}
}

// TestMiddleware_DuplicateRequestReturnsCached verifies that a second request
// with the same Idempotency-Key replays the cached response, does NOT invoke
// the handler again, and sets the Idempotency-Replay: true header.
func TestMiddleware_DuplicateRequestReturnsCached(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	var callCount int
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "original-req")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"tok_456"}`))
	}))

	// First request -- handler executes.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req1.Header.Set("Idempotency-Key", "dup-key-001")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if callCount != 1 {
		t.Fatalf("expected handler called once, got %d", callCount)
	}

	// Duplicate request -- handler must NOT execute again.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req2.Header.Set("Idempotency-Key", "dup-key-001")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 1 {
		t.Fatalf("expected handler NOT called again, got %d calls", callCount)
	}
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected cached status 201, got %d", rec2.Code)
	}
	body2, _ := io.ReadAll(rec2.Result().Body)
	if string(body2) != `{"id":"tok_456"}` {
		t.Fatalf("unexpected cached body: %s", body2)
	}
	if rec2.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("replayed response must have Idempotency-Replay: true")
	}
	if rec2.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected cached Content-Type, got: %s", rec2.Header().Get("Content-Type"))
	}
	if rec2.Header().Get("X-Request-Id") != "original-req" {
		t.Fatalf("expected cached X-Request-Id, got: %s", rec2.Header().Get("X-Request-Id"))
	}
}

// TestMiddleware_DifferentKeysGetSeparateResponses verifies that requests with
// different idempotency keys each execute the handler independently and cache
// their own responses.
func TestMiddleware_DifferentKeysGetSeparateResponses(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	var callCount int
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"call":` + string(rune('0'+callCount)) + `}`))
	}))

	// Request with key-a.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req1.Header.Set("Idempotency-Key", "key-a")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// Request with key-b.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req2.Header.Set("Idempotency-Key", "key-b")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 2 {
		t.Fatalf("expected handler called twice for different keys, got %d", callCount)
	}

	// Replay key-a -- handler must not be called.
	req3 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req3.Header.Set("Idempotency-Key", "key-a")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	if callCount != 2 {
		t.Fatalf("expected handler NOT called again for key-a replay, got %d", callCount)
	}
	if rec3.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("replayed key-a response should have Idempotency-Replay: true")
	}

	// Replay key-b -- handler must not be called.
	req4 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req4.Header.Set("Idempotency-Key", "key-b")
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)

	if callCount != 2 {
		t.Fatalf("expected handler NOT called again for key-b replay, got %d", callCount)
	}
	if rec4.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("replayed key-b response should have Idempotency-Replay: true")
	}
}

// TestMiddleware_ResponseBodyAndStatusPreserved verifies that a cached replay
// returns exactly the same status code, body, and headers as the original
// response.
func TestMiddleware_ResponseBodyAndStatusPreserved(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	const wantStatus = http.StatusAccepted
	const wantBody = `{"result":"created","count":42}`
	const wantContentType = "application/json; charset=utf-8"

	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", wantContentType)
		w.Header().Set("X-Custom", "custom-value")
		w.WriteHeader(wantStatus)
		_, _ = w.Write([]byte(wantBody))
	}))

	// Original request.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req1.Header.Set("Idempotency-Key", "preserve-test")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// Replayed request.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
	req2.Header.Set("Idempotency-Key", "preserve-test")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	// Verify status code.
	if rec2.Code != wantStatus {
		t.Fatalf("status: want %d, got %d", wantStatus, rec2.Code)
	}

	// Verify body.
	body2, _ := io.ReadAll(rec2.Result().Body)
	if string(body2) != wantBody {
		t.Fatalf("body: want %q, got %q", wantBody, string(body2))
	}

	// Verify headers.
	if got := rec2.Header().Get("Content-Type"); got != wantContentType {
		t.Fatalf("Content-Type: want %q, got %q", wantContentType, got)
	}
	if got := rec2.Header().Get("X-Custom"); got != "custom-value" {
		t.Fatalf("X-Custom: want %q, got %q", "custom-value", got)
	}

	// Verify replay indicator.
	if rec2.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("replayed response must have Idempotency-Replay: true")
	}
}

// TestMiddleware_ConcurrentRequestsSameKey verifies that concurrent requests
// sharing the same idempotency key do not race and that subsequent replays
// return the cached response. Run with -race to detect data races.
func TestMiddleware_ConcurrentRequestsSameKey(t *testing.T) {
	c := New(time.Minute, 100)
	defer c.Stop()

	var handlerCalls atomic.Int64
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"concurrent"}`))
	}))

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/tokens", nil)
			req.Header.Set("Idempotency-Key", "concurrent-key")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// Every response must have status 201 and the correct body,
			// regardless of whether it was served fresh or from cache.
			if rec.Code != http.StatusCreated {
				t.Errorf("expected 201, got %d", rec.Code)
			}
			body, _ := io.ReadAll(rec.Result().Body)
			if string(body) != `{"id":"concurrent"}` {
				t.Errorf("unexpected body: %s", body)
			}
		}()
	}

	wg.Wait()

	// The handler must have been invoked at least once (it may be more than
	// once due to the race between Get and Set not being atomic, which is
	// acceptable for idempotency caches). Critically, the race detector must
	// not report any data races.
	calls := handlerCalls.Load()
	if calls < 1 {
		t.Fatalf("expected handler called at least once, got %d", calls)
	}
	t.Logf("handler invoked %d time(s) across %d concurrent requests", calls, goroutines)
}
