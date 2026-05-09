package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

// perAdapterSender returns a response that echoes its own adapter ID in the
// content field, so we can tell which variant served the request.
type perAdapterSender struct {
	id string
}

func (s *perAdapterSender) ID() string { return s.id }
func (s *perAdapterSender) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) {
	body := fmt.Sprintf(`{"choices":[{"message":{"content":"from-%s"}}]}`, s.id)
	return router.ProviderResponse(body), nil
}
func (s *perAdapterSender) ClassifyError(err error) *router.ClassifiedError {
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

// setupAliasTestServer wires MountRoutes with a real engine+resolver and two
// registered models ("variant-a", "variant-b"), so tests can observe blind A/B
// rewriting end-to-end through the OpenAI-compatible endpoint.
func setupAliasTestServer(t *testing.T) (*httptest.Server, *router.Engine, store.Store, string) {
	t.Helper()

	r := chi.NewRouter()
	eng := router.NewEngine(router.EngineConfig{DefaultMode: "normal"})

	eng.RegisterAdapter(&perAdapterSender{id: "provider-a"})
	eng.RegisterAdapter(&perAdapterSender{id: "provider-b"})
	eng.RegisterModel(router.Model{
		ID: "variant-a", ProviderID: "provider-a",
		Weight: 5, MaxContextTokens: 8192, Enabled: true,
	})
	eng.RegisterModel(router.Model{
		ID: "variant-b", ProviderID: "provider-b",
		Weight: 5, MaxContextTokens: 8192, Enabled: true,
	})

	eng.SetAliasResolver(router.NewAliasResolver())

	v, _ := vault.New(true)
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
	t.Cleanup(func() { _ = db.Close() })
	ts, _ := tsdb.New(db.DB())
	keyMgr := apikey.NewManager(db)
	plaintext, _, err := keyMgr.Generate(context.Background(), "integration", `["chat"]`, 0, nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	MountRoutes(r, Dependencies{
		Engine:    eng,
		Vault:     v,
		Metrics:   m,
		Store:     db,
		EventBus:  bus,
		Stats:     sc,
		TSDB:      ts,
		APIKeyMgr: keyMgr,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, eng, db, plaintext
}

func postCompletion(t *testing.T, url, apiKey, model string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

// TestAliasIntegration_BlindABSplit verifies the end-to-end blind A/B flow:
// client asks for "experiment", the resolver rewrites to "variant-a" or
// "variant-b" with equal probability, the caller only sees a response content
// string telling them which one served, and an X-Alias-From response header
// reports the original alias so operators can correlate.
func TestAliasIntegration_BlindABSplit(t *testing.T) {
	srv, eng, _, apiKey := setupAliasTestServer(t)

	// Install the 50/50 alias.
	if err := eng.AliasResolver().Set(router.Alias{
		Name: "experiment",
		Variants: []router.AliasVariant{
			{ModelID: "variant-a", Weight: 50},
			{ModelID: "variant-b", Weight: 50},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	seen := map[string]int{}
	// Enough iterations to almost certainly hit both variants under a fair
	// coin; the probability that all 40 coin flips land on the same side is
	// 2 * (1/2)^40 ≈ 2e-12.
	const iterations = 40
	for i := 0; i < iterations; i++ {
		resp := postCompletion(t, srv.URL, apiKey, "experiment")
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("iter %d: status %d: %s", i, resp.StatusCode, string(body))
		}
		if alias := resp.Header.Get("X-Alias-From"); alias != "experiment" {
			t.Fatalf("iter %d: missing X-Alias-From header, got %q", i, alias)
		}
		negotiated := resp.Header.Get("X-Negotiated-Model")
		if negotiated != "variant-a" && negotiated != "variant-b" {
			t.Fatalf("iter %d: unexpected X-Negotiated-Model %q", i, negotiated)
		}

		// Sanity: response body content echoes the adapter that served it.
		var body completionsResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = resp.Body.Close()
		if body.Model != negotiated {
			t.Fatalf("response model %q should match X-Negotiated-Model %q", body.Model, negotiated)
		}
		seen[negotiated]++
	}

	if seen["variant-a"] == 0 || seen["variant-b"] == 0 {
		t.Fatalf("expected traffic on both variants over %d iterations, got %v", iterations, seen)
	}
}

// TestAliasIntegration_UnknownAliasPassesThrough verifies that when no alias
// is registered for a requested model name, routing falls through to the
// normal model-hint path unchanged. A missing alias must never block traffic.
func TestAliasIntegration_UnknownAliasPassesThrough(t *testing.T) {
	srv, _, _, apiKey := setupAliasTestServer(t)

	// No alias registered. Direct model-name request should route normally.
	resp := postCompletion(t, srv.URL, apiKey, "variant-a")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, string(b))
	}
	if got := resp.Header.Get("X-Alias-From"); got != "" {
		t.Fatalf("unknown alias should not set X-Alias-From; got %q", got)
	}
	if got := resp.Header.Get("X-Negotiated-Model"); got != "variant-a" {
		t.Fatalf("expected direct route to variant-a, got %q", got)
	}
}

// TestAliasIntegration_DisabledAliasPassesThrough verifies a disabled alias
// is a no-op — useful for pausing an experiment without deleting config.
func TestAliasIntegration_DisabledAliasPassesThrough(t *testing.T) {
	srv, eng, _, apiKey := setupAliasTestServer(t)

	if err := eng.AliasResolver().Set(router.Alias{
		Name: "variant-a", // use a real model name as alias name
		Variants: []router.AliasVariant{
			{ModelID: "variant-b", Weight: 1},
		},
		Enabled: false, // paused
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Client asks for "variant-a" — if the alias were active it would rewrite
	// to variant-b; because it's disabled we get variant-a.
	resp := postCompletion(t, srv.URL, apiKey, "variant-a")
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Negotiated-Model") != "variant-a" {
		t.Fatalf("disabled alias should not rewrite; got model %q",
			resp.Header.Get("X-Negotiated-Model"))
	}
	if got := resp.Header.Get("X-Alias-From"); got != "" {
		t.Fatalf("disabled alias should not set X-Alias-From; got %q", got)
	}
}

func TestAliasIntegration_WildcardAliasSelectsConfiguredBackend(t *testing.T) {
	srv, eng, _, apiKey := setupAliasTestServer(t)

	if err := eng.AliasResolver().Set(router.Alias{
		Name: router.WildcardModelHint,
		Variants: []router.AliasVariant{
			{ModelID: "variant-b", Weight: 1},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	resp := postCompletion(t, srv.URL, apiKey, router.WildcardModelHint)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, string(b))
	}
	if got := resp.Header.Get("X-Alias-From"); got != router.WildcardModelHint {
		t.Fatalf("expected wildcard alias header, got %q", got)
	}
	if got := resp.Header.Get("X-Negotiated-Model"); got != "variant-b" {
		t.Fatalf("expected wildcard alias to select variant-b, got %q", got)
	}
}

// TestAliasIntegration_RequestLogRecordsAlias verifies that request_logs
// captures AliasFrom so A/B analysis can group variants by experiment.
// This is the single most important bit of the whole feature — without it
// you can run the split but can't read the results.
func TestAliasIntegration_RequestLogRecordsAlias(t *testing.T) {
	srv, eng, db, apiKey := setupAliasTestServer(t)

	if err := eng.AliasResolver().Set(router.Alias{
		Name: "experiment",
		Variants: []router.AliasVariant{
			{ModelID: "variant-a", Weight: 1},
			{ModelID: "variant-b", Weight: 1},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Send a handful of requests through the alias.
	for i := 0; i < 5; i++ {
		resp := postCompletion(t, srv.URL, apiKey, "experiment")
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	// Baseline: one direct call that should NOT be tagged.
	resp := postCompletion(t, srv.URL, apiKey, "variant-a")
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	logs, err := db.ListRequestLogs(context.Background(), 100, 0)
	if err != nil {
		t.Fatalf("ListRequestLogs: %v", err)
	}

	var tagged, untagged int
	for _, l := range logs {
		switch l.AliasFrom {
		case "experiment":
			tagged++
		case "":
			untagged++
		default:
			t.Fatalf("unexpected alias_from %q on log row", l.AliasFrom)
		}
	}
	if tagged != 5 {
		t.Fatalf("expected 5 rows with alias_from=experiment, got %d (all logs: %+v)", tagged, logs)
	}
	if untagged != 1 {
		t.Fatalf("expected 1 direct (untagged) row, got %d", untagged)
	}
}

// TestAliasIntegration_StickyByAPIKey verifies per-caller sticky assignment
// end-to-end: a single API key always lands on the same variant across many
// HTTP requests (different request IDs), which is what you want when the
// two variants behave differently enough that mid-session flipping would
// confuse the caller.
func TestAliasIntegration_StickyByAPIKey(t *testing.T) {
	srv, eng, _, apiKey := setupAliasTestServer(t)

	if err := eng.AliasResolver().Set(router.Alias{
		Name: "experiment",
		Variants: []router.AliasVariant{
			{ModelID: "variant-a", Weight: 50},
			{ModelID: "variant-b", Weight: 50},
		},
		Enabled:  true,
		StickyBy: router.StickyByAPIKey,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Fire many requests with the same api_key — must all land on one variant.
	var pinned string
	const iterations = 25
	for i := 0; i < iterations; i++ {
		resp := postCompletion(t, srv.URL, apiKey, "experiment")
		got := resp.Header.Get("X-Negotiated-Model")
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if got != "variant-a" && got != "variant-b" {
			t.Fatalf("iter %d: unexpected model %q", i, got)
		}
		if i == 0 {
			pinned = got
		}
		if got != pinned {
			t.Fatalf("api_key stickiness broken: iter %d got %q, expected stable %q (all %d requests should pin to one variant)", i, got, pinned, iterations)
		}
	}
}

// TestAliasIntegration_StablePerRequestID verifies that within a single HTTP
// request (i.e. same middleware-assigned request ID) the alias resolves
// consistently — so retries and fallback paths within one request don't
// straddle two variants.
func TestAliasIntegration_StablePerRequestID(t *testing.T) {
	// Use the resolver directly rather than the HTTP layer because the test
	// harness doesn't expose a way to inject a specific request ID.
	r := router.NewAliasResolver()
	_ = r.Set(router.Alias{
		Name: "exp",
		Variants: []router.AliasVariant{
			{ModelID: "variant-a", Weight: 50},
			{ModelID: "variant-b", Weight: 50},
		},
		Enabled: true,
	})

	// Collect how each unique request ID maps; within an ID the mapping must
	// be stable, but across IDs the mapping should vary.
	perReq := make(map[string]string)
	for i := 0; i < 200; i++ {
		reqID := fmt.Sprintf("req-%d", i)
		got, _ := r.Resolve("exp", reqID)
		// Re-resolve 5 more times — must match.
		for j := 0; j < 5; j++ {
			again, _ := r.Resolve("exp", reqID)
			if again != got {
				t.Fatalf("reqID %q resolved inconsistently: first=%q, retry=%q", reqID, got, again)
			}
		}
		perReq[reqID] = got
	}
	// Across 200 distinct request IDs we expect both variants to appear.
	counts := map[string]int{}
	for _, v := range perReq {
		counts[v]++
	}
	if counts["variant-a"] == 0 || counts["variant-b"] == 0 {
		t.Fatalf("expected both variants across 200 IDs, got %v", counts)
	}
}
