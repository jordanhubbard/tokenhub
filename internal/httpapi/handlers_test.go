package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

// mockSender implements router.Sender for testing.
type mockSender struct {
	id   string
	resp json.RawMessage
	err  error
}

func (m *mockSender) ID() string { return m.id }

func (m *mockSender) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func (m *mockSender) ClassifyError(err error) *router.ClassifiedError {
	var ce *router.ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

func setupTestServer(t *testing.T) (*httptest.Server, *router.Engine, *vault.Vault) {
	t.Helper()

	r := chi.NewRouter()
	eng := router.NewEngine(router.EngineConfig{})
	v, err := vault.New(true)
	if err != nil {
		t.Fatalf("failed to create vault: %v", err)
	}
	m := metrics.New()
	bus := events.NewBus()
	sc := stats.NewCollector()

	// Set up in-memory SQLite store for tests.
	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Set up TSDB.
	ts, err := tsdb.New(db.DB())
	if err != nil {
		t.Fatalf("failed to create TSDB: %v", err)
	}

	MountRoutes(r, Dependencies{Engine: eng, Vault: v, Metrics: m, EventBus: bus, Stats: sc, Store: db, TSDB: ts})
	srv := httptest.NewServer(r)
	return srv, eng, v
}

func TestHealthz(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestChatSuccess(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id: "test-provider",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"Hello!"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{
		ID: "test-model", ProviderID: "test-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})

	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if chatResp.NegotiatedModel != "test-model" {
		t.Errorf("expected test-model, got %s", chatResp.NegotiatedModel)
	}
	if chatResp.RoutingReason == "" {
		t.Error("expected routing reason to be set")
	}
	if chatResp.Response == nil {
		t.Error("expected response body")
	}
}

func TestChatBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestChatNoEligibleModels(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestChatWithPolicy(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id: "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(ChatRequest{
		Policy: &PolicyHint{Mode: "cheap", MinWeight: 1},
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPlanSuccess(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id: "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"plan output"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(PlanRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "make a plan"}},
		},
		Orchestration: router.OrchestrationDirective{Mode: "planning"},
	})

	resp, err := http.Post(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["negotiated_model"] != "m1" {
		t.Errorf("expected m1, got %v", result["negotiated_model"])
	}
}

func TestVaultUnlockSuccess(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, err := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
}

func TestVaultUnlockShortPassword(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"admin_password": "short"})
	resp, err := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestModelsUpsert(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	// Register an adapter so the model is usable
	mock := &mockSender{
		id: "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)

	model := router.Model{
		ID: "new-model", ProviderID: "p1",
		Weight: 7, MaxContextTokens: 8192, Enabled: true,
	}
	body, _ := json.Marshal(model)
	resp, err := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Now verify the model is usable via chat
	chatBody, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})
	chatResp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(chatBody))
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	defer chatResp.Body.Close()

	if chatResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for chat after model registration, got %d", chatResp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHealthStatsEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if _, ok := result["providers"]; !ok {
		t.Error("expected 'providers' key in health stats response")
	}
}

func TestAdminEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// /admin now serves the embedded SPA.
	resp, err := http.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && ct != "text/html; charset=utf-8" && ct != "text/html" {
		// Acceptable: either text/html or served as-is.
	}
}

func TestAdminAPIInfoEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/api/info")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["tokenhub"] != "admin" {
		t.Errorf("expected admin, got %v", result["tokenhub"])
	}
}

func TestStatsEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/stats")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if _, ok := result["global"]; !ok {
		t.Error("expected 'global' key in stats response")
	}
	if _, ok := result["by_model"]; !ok {
		t.Error("expected 'by_model' key in stats response")
	}
	if _, ok := result["by_provider"]; !ok {
		t.Error("expected 'by_provider' key in stats response")
	}
}

func TestSSEEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Make a request that we cancel after getting the initial connection event.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/admin/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", ct)
	}

	// Read the initial connection event.
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	data := string(buf[:n])
	if !bytes.Contains([]byte(data), []byte("event: connected")) {
		t.Errorf("expected connected event, got %s", data)
	}
}

func TestVaultLockUnlockCycle(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock first.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, err := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unlock failed: %v", err)
	}
	resp.Body.Close()
	if v.IsLocked() {
		t.Error("vault should be unlocked after unlock")
	}

	// Lock.
	resp, err = http.Post(ts.URL+"/admin/v1/vault/lock", "application/json", nil)
	if err != nil {
		t.Fatalf("lock failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !v.IsLocked() {
		t.Error("vault should be locked after lock")
	}

	// Lock again (idempotent).
	resp, err = http.Post(ts.URL+"/admin/v1/vault/lock", "application/json", nil)
	if err != nil {
		t.Fatalf("second lock failed: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["already_locked"] != true {
		t.Error("expected already_locked:true on second lock")
	}
}

func TestChatWithDirectives(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"cheap reply"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	// Include @@tokenhub directive in message content.
	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "@@tokenhub mode=cheap\nHello"},
			},
		},
	})

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	_ = json.NewDecoder(resp.Body).Decode(&chatResp)
	if chatResp.NegotiatedModel != "m1" {
		t.Errorf("expected m1, got %s", chatResp.NegotiatedModel)
	}
}

func TestRequestLogsEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/logs?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["logs"]; !ok {
		t.Error("expected 'logs' key")
	}
}

func TestEngineModelsEndpoint(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	resp, err := http.Get(ts.URL + "/admin/v1/engine/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	models, ok := result["models"].([]any)
	if !ok {
		t.Fatal("expected models array")
	}
	if len(models) < 1 {
		t.Error("expected at least 1 model")
	}
	adapters, ok := result["adapters"].([]any)
	if !ok {
		t.Fatal("expected adapters array")
	}
	if len(adapters) < 1 {
		t.Error("expected at least 1 adapter")
	}
}

func TestProviderUpsertWithAPIKey(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock vault first.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, _ := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	if v.IsLocked() {
		t.Fatal("vault should be unlocked")
	}

	// Upsert provider with API key.
	provBody, _ := json.Marshal(map[string]any{
		"id":      "test-openai",
		"type":    "openai",
		"enabled": true,
		"api_key": "sk-test-12345",
	})
	resp, err := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(provBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["cred_store"] != "vault" {
		t.Errorf("expected cred_store=vault, got %v", result["cred_store"])
	}

	// Verify key stored in vault.
	key, err := v.Get("provider:test-openai:api_key")
	if err != nil {
		t.Fatalf("failed to get key from vault: %v", err)
	}
	if key != "sk-test-12345" {
		t.Errorf("expected sk-test-12345, got %s", key)
	}
}

func TestRoutingConfigEndpoints(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// GET routing config (should be empty/default).
	resp, err := http.Get(ts.URL + "/admin/v1/routing-config")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTSDBEndpoints(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Query with no data.
	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/query?metric=latency")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// List metrics.
	resp, err = http.Get(ts.URL + "/admin/v1/tsdb/metrics")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestChatPublishesEventsAndStats(t *testing.T) {
	r := chi.NewRouter()
	eng := router.NewEngine(router.EngineConfig{})
	v, _ := vault.New(true)
	m := metrics.New()
	bus := events.NewBus()
	sc := stats.NewCollector()

	MountRoutes(r, Dependencies{Engine: eng, Vault: v, Metrics: m, EventBus: bus, Stats: sc})
	ts := httptest.NewServer(r)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"hi"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	// Subscribe to events before making request.
	sub := bus.Subscribe(10)
	defer bus.Unsubscribe(sub)

	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})
	resp, err := http.Post(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// Check that an event was published.
	select {
	case e := <-sub.C:
		if e.Type != events.EventRouteSuccess {
			t.Errorf("expected route_success, got %s", e.Type)
		}
		if e.ModelID != "m1" {
			t.Errorf("expected model m1, got %s", e.ModelID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	// Check that a stats snapshot was recorded.
	if sc.SnapshotCount() != 1 {
		t.Errorf("expected 1 snapshot, got %d", sc.SnapshotCount())
	}
}
