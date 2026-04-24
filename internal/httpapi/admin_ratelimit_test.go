package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/ratelimit"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

// setupRateLimitedServer mounts MountRoutes with independent /v1 and /admin/v1
// rate limiters so tests can observe that they bucket traffic separately.
// rps/burst apply to both limiters unless overridden.
func setupRateLimitedServer(t *testing.T, publicRPS, publicBurst, adminRPS, adminBurst int) (*httptest.Server, func()) {
	t.Helper()

	r := chi.NewRouter()
	eng := router.NewEngine(router.EngineConfig{})
	v, err := vault.New(true)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	m := metrics.New()
	bus := events.NewBus()
	sc := stats.NewCollector()

	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ts, err := tsdb.New(db.DB())
	if err != nil {
		t.Fatalf("tsdb: %v", err)
	}
	keyMgr := apikey.NewManager(db)

	publicRL := ratelimit.New(publicRPS, publicBurst, time.Second)
	adminRL := ratelimit.New(adminRPS, adminBurst, time.Second)

	MountRoutes(r, Dependencies{
		Engine:           eng,
		Vault:            v,
		Metrics:          m,
		Store:            db,
		EventBus:         bus,
		Stats:            sc,
		TSDB:             ts,
		APIKeyMgr:        keyMgr,
		RateLimiter:      publicRL,
		AdminRateLimiter: adminRL,
	})
	srv := httptest.NewServer(r)

	cleanup := func() {
		srv.Close()
		publicRL.Stop()
		adminRL.Stop()
		_ = db.Close()
	}
	return srv, cleanup
}

// TestAdminRateLimit_Enforced verifies that /admin/v1 endpoints enforce the
// admin rate-limit bucket. Without this protection, a leaked admin token (or
// a buggy admin UI in a tight loop) could consume unbounded request capacity.
func TestAdminRateLimit_Enforced(t *testing.T) {
	// Admin burst = 2 means the first 2 requests succeed, the 3rd is rejected
	// before it can accrue any new tokens (rps=1, burst=2).
	srv, cleanup := setupRateLimitedServer(t, 1000, 1000, 1, 2)
	defer cleanup()

	// /admin/v1/info does not require auth in the test harness (AdminToken is
	// nil), so 401 is not a confound here. We are purely testing the limiter.
	got := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/admin/v1/info")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		got = append(got, resp.StatusCode)
	}

	// First 2 should succeed (burst), remaining should be 429.
	if got[0] != http.StatusOK || got[1] != http.StatusOK {
		t.Fatalf("first two requests should succeed within burst, got %v", got)
	}
	has429 := false
	for _, s := range got[2:] {
		if s == http.StatusTooManyRequests {
			has429 = true
			break
		}
	}
	if !has429 {
		t.Fatalf("expected at least one 429 after burst exhausted, got %v", got)
	}
}

// TestAdminRateLimit_SeparateFromPublic verifies that the admin limiter is
// an independent bucket. If an attacker floods /admin/v1, public /v1 traffic
// must not be affected — and vice versa.
func TestAdminRateLimit_SeparateFromPublic(t *testing.T) {
	// Exhaust the admin bucket with a tiny rate; public limiter is generous.
	srv, cleanup := setupRateLimitedServer(t, 1000, 1000, 1, 1)
	defer cleanup()

	// Hammer /admin/v1/info until we get a 429 (admin bucket is drained).
	admin429 := false
	for i := 0; i < 10; i++ {
		resp, err := http.Get(srv.URL + "/admin/v1/info")
		if err != nil {
			t.Fatalf("admin req %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			admin429 = true
			break
		}
	}
	if !admin429 {
		t.Fatal("admin bucket should have drained and returned 429")
	}

	// Public /v1/models (unauthenticated, cheap) must still succeed — the
	// admin flood must not consume the public bucket.
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("public req: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("public bucket should NOT be drained by admin traffic; got 429")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /v1/models, got %d", resp.StatusCode)
	}
}

// TestAdminRateLimit_RefillsOverTime verifies that after a 429, subsequent
// requests succeed once an interval has elapsed for the bucket to refill.
// The limiter only refills in whole-interval units, so we wait slightly
// longer than one interval before retrying.
func TestAdminRateLimit_RefillsOverTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive refill test in -short mode")
	}
	// rate=1 req per 1s interval, burst=1 ⇒ after draining, one token refills
	// after ~1 second of elapsed wall time.
	srv, cleanup := setupRateLimitedServer(t, 1000, 1000, 1, 1)
	defer cleanup()

	// First burst succeeds.
	resp, err := http.Get(srv.URL + "/admin/v1/info")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first request should succeed, got %d", resp.StatusCode)
	}

	// Immediate follow-up should 429 (bucket drained).
	resp, _ = http.Get(srv.URL + "/admin/v1/info")
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second immediate request should 429, got %d", resp.StatusCode)
	}

	// Wait for one full interval so at least one token refills.
	time.Sleep(1100 * time.Millisecond)

	resp, err = http.Get(srv.URL + "/admin/v1/info")
	if err != nil {
		t.Fatalf("after refill: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after refill expected 200, got %d", resp.StatusCode)
	}
}

// TestAdminRateLimit_NoLimiter verifies that an explicit nil AdminRateLimiter
// disables admin rate limiting entirely (the documented test-harness behaviour).
// This guards against accidental coupling between the public and admin limiters.
func TestAdminRateLimit_NoLimiter(t *testing.T) {
	r := chi.NewRouter()
	eng := router.NewEngine(router.EngineConfig{})
	v, _ := vault.New(true)
	m := metrics.New()
	bus := events.NewBus()
	sc := stats.NewCollector()
	db, _ := store.NewSQLite(":memory:")
	_ = db.Migrate(context.Background())
	ts, _ := tsdb.New(db.DB())
	defer func() { _ = db.Close() }()

	MountRoutes(r, Dependencies{
		Engine:           eng,
		Vault:            v,
		Metrics:          m,
		Store:            db,
		EventBus:         bus,
		Stats:            sc,
		TSDB:             ts,
		AdminRateLimiter: nil,
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	for i := 0; i < 50; i++ {
		resp, err := http.Get(srv.URL + "/admin/v1/info")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("admin req %d returned 429 with nil AdminRateLimiter", i)
		}
	}
}
