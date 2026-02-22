package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// testAPIKey is the plaintext key generated during test setup.
var testAPIKey string

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
	t.Cleanup(func() { _ = db.Close() })

	// Set up TSDB.
	ts, err := tsdb.New(db.DB())
	if err != nil {
		t.Fatalf("failed to create TSDB: %v", err)
	}

	keyMgr := apikey.NewManager(db)

	// Create a test API key for authenticating /v1 requests.
	plaintext, _, err := keyMgr.Generate(context.Background(), "test-api-key", `["chat","plan"]`, 0, nil)
	if err != nil {
		t.Fatalf("failed to generate test API key: %v", err)
	}
	testAPIKey = plaintext

	MountRoutes(r, Dependencies{Engine: eng, Vault: v, Metrics: m, EventBus: bus, Stats: sc, Store: db, TSDB: ts, APIKeyMgr: keyMgr})
	srv := httptest.NewServer(r)
	return srv, eng, v
}

// authPost sends a POST with the test API key bearer token.
func authPost(url, contentType string, body *bytes.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	return http.DefaultClient.Do(req)
}


func TestHealthz(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	// healthz returns 503 when no adapters/models are registered.
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with no adapters, got %d", resp.StatusCode)
	}

	// Register an adapter and model so healthz passes.
	eng.RegisterAdapter(&mockSender{id: "test-provider", resp: json.RawMessage(`{}`)})
	eng.RegisterModel(router.Model{
		ID: "test-model", ProviderID: "test-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})

	resp2, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with adapters, got %d", resp2.StatusCode)
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

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Now verify the model is usable via chat
	chatBody, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})
	chatResp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(chatBody))
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	defer func() { _ = chatResp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && ct != "text/html; charset=utf-8" && ct != "text/html" {
		t.Errorf("unexpected Content-Type: %s", ct)
	}
}

func TestAdminAPIInfoEndpoint(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/info")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	_ = resp.Body.Close()
	if v.IsLocked() {
		t.Error("vault should be unlocked after unlock")
	}

	// Lock.
	resp, err = http.Post(ts.URL+"/admin/v1/vault/lock", "application/json", nil)
	if err != nil {
		t.Fatalf("lock failed: %v", err)
	}
	_ = resp.Body.Close()
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
	defer func() { _ = resp.Body.Close() }()
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

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	_ = resp.Body.Close()

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
	defer func() { _ = resp.Body.Close() }()

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
	_ = resp.Body.Close()
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
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// List metrics.
	resp, err = http.Get(ts.URL + "/admin/v1/tsdb/metrics")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
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

	// Create in-memory store and TSDB for this standalone test.
	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tsd, err := tsdb.New(db.DB())
	if err != nil {
		t.Fatalf("failed to create TSDB: %v", err)
	}

	keyMgr := apikey.NewManager(db)
	plaintext, _, err := keyMgr.Generate(context.Background(), "events-test-key", `["chat","plan"]`, 0, nil)
	if err != nil {
		t.Fatalf("failed to generate API key: %v", err)
	}

	MountRoutes(r, Dependencies{Engine: eng, Vault: v, Metrics: m, EventBus: bus, Stats: sc, Store: db, TSDB: tsd, APIKeyMgr: keyMgr})
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

	// Use local auth key for this standalone test.
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

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

// --- Input validation tests ---

func TestChatEmptyMessages(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{},
		},
	})

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty messages, got %d", resp.StatusCode)
	}
}

func TestChatNilMessages(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Send a request with no messages field at all.
	body, _ := json.Marshal(map[string]any{
		"request": map[string]any{},
	})

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for nil messages, got %d", resp.StatusCode)
	}
}

func TestChatPolicyOutOfRange(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name   string
		policy *PolicyHint
	}{
		{"negative budget", &PolicyHint{MaxBudgetUSD: -1}},
		{"budget too high", &PolicyHint{MaxBudgetUSD: 200}},
		{"negative latency", &PolicyHint{MaxLatencyMs: -1}},
		{"latency too high", &PolicyHint{MaxLatencyMs: 500000}},
		{"negative weight", &PolicyHint{MinWeight: -1}},
		{"weight too high", &PolicyHint{MinWeight: 11}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(ChatRequest{
				Policy: tc.policy,
				Request: router.Request{
					Messages: []router.Message{{Role: "user", Content: "hi"}},
				},
			})

			resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

func TestChatValidPolicyStillWorks(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(ChatRequest{
		Policy: &PolicyHint{MaxBudgetUSD: 50.0, MaxLatencyMs: 5000, MinWeight: 3},
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hi"}},
		},
	})

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid policy, got %d", resp.StatusCode)
	}
}

func TestPlanEmptyMessages(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(PlanRequest{
		Request: router.Request{
			Messages: []router.Message{},
		},
		Orchestration: router.OrchestrationDirective{Mode: "planning"},
	})

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty messages, got %d", resp.StatusCode)
	}
}

func TestPlanInvalidIterations(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name       string
		iterations int
	}{
		{"negative iterations", -1},
		{"iterations too high", 11},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(PlanRequest{
				Request: router.Request{
					Messages: []router.Message{{Role: "user", Content: "plan"}},
				},
				Orchestration: router.OrchestrationDirective{
					Mode:       "planning",
					Iterations: tc.iterations,
				},
			})

			resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

func TestPlanInvalidMode(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(PlanRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "plan"}},
		},
		Orchestration: router.OrchestrationDirective{
			Mode: "invalid_mode",
		},
	})

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid mode, got %d", resp.StatusCode)
	}
}

func TestModelsUpsertValidation(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name  string
		model router.Model
	}{
		{"empty id", router.Model{ID: "", ProviderID: "p1", Weight: 5}},
		{"empty provider", router.Model{ID: "m1", ProviderID: "", Weight: 5}},
		{"weight too high", router.Model{ID: "m1", ProviderID: "p1", Weight: 11}},
		{"negative weight", router.Model{ID: "m1", ProviderID: "p1", Weight: -1}},
		{"negative input cost", router.Model{ID: "m1", ProviderID: "p1", Weight: 5, InputPer1K: -0.5}},
		{"negative output cost", router.Model{ID: "m1", ProviderID: "p1", Weight: 5, OutputPer1K: -0.5}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.model)
			resp, err := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

func TestModelsUpsertValidModel(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)

	model := router.Model{
		ID: "valid-model", ProviderID: "p1",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
		InputPer1K: 0.01, OutputPer1K: 0.03,
	}
	body, _ := json.Marshal(model)
	resp, err := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid model, got %d", resp.StatusCode)
	}
}

func TestModelsPatchValidation(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	// Persist the model in the store so PATCH can find it.
	model := router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true}
	body, _ := json.Marshal(model)
	resp, _ := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()

	tests := []struct {
		name  string
		patch map[string]any
	}{
		{"weight too high", map[string]any{"weight": 11.0}},
		{"negative weight", map[string]any{"weight": -1.0}},
		{"negative input cost", map[string]any{"input_per_1k": -0.5}},
		{"negative output cost", map[string]any{"output_per_1k": -0.5}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patchBody, _ := json.Marshal(tc.patch)
			req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/models/m1", bytes.NewReader(patchBody))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

func TestRoutingConfigSetValidation(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name string
		cfg  map[string]any
	}{
		{"unknown mode", map[string]any{"default_mode": "unknown_mode"}},
		{"negative budget", map[string]any{"default_max_budget_usd": -1.0}},
		{"budget too high", map[string]any{"default_max_budget_usd": 200.0}},
		{"negative latency", map[string]any{"default_max_latency_ms": -1}},
		{"latency too high", map[string]any{"default_max_latency_ms": 500000}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.cfg)
			req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/routing-config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

func TestRoutingConfigSetValid(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	cfg := map[string]any{
		"default_mode":           "cheap",
		"default_max_budget_usd": 10.0,
		"default_max_latency_ms": 5000,
	}
	body, _ := json.Marshal(cfg)
	req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/routing-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid config, got %d", resp.StatusCode)
	}
}

// --- Additional coverage tests ---

func TestVaultLockHandler(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock vault first so Lock actually transitions state.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, _ := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()
	if v.IsLocked() {
		t.Fatal("vault should be unlocked before lock test")
	}

	// Lock the vault.
	resp, err := http.Post(ts.URL+"/admin/v1/vault/lock", "application/json", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	if !v.IsLocked() {
		t.Error("vault should be locked")
	}
}

func TestProvidersListHandler(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/providers")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatal("expected items array in response")
	}
	if len(items) != 0 {
		t.Errorf("expected empty items, got %d", len(items))
	}
	if result["total"] != float64(0) {
		t.Errorf("expected total=0, got %v", result["total"])
	}
}

func TestProvidersDeleteHandler(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock vault and create a provider first.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, _ := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()
	if v.IsLocked() {
		t.Fatal("vault should be unlocked")
	}

	provBody, _ := json.Marshal(map[string]any{
		"id":      "del-provider",
		"type":    "openai",
		"enabled": true,
	})
	resp, _ = http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(provBody))
	_ = resp.Body.Close()

	// Delete the provider.
	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/v1/providers/del-provider", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

func TestModelsListHandler(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatal("expected items array in response")
	}
	if len(items) != 0 {
		t.Errorf("expected empty items, got %d", len(items))
	}
	if result["total"] != float64(0) {
		t.Errorf("expected total=0, got %v", result["total"])
	}
}

func TestModelsDeleteHandler(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	// Register adapter and upsert a model so there's something to delete.
	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)

	model := router.Model{ID: "delete-me", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true}
	body, _ := json.Marshal(model)
	resp, _ := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()

	// Delete the model.
	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/v1/models/delete-me", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

func TestRoutingConfigGetHandler(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/routing-config")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	// Default config should decode without error; presence of the object is sufficient.
}

func TestHealthStatsHandlerFields(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if _, ok := result["providers"]; !ok {
		t.Error("expected 'providers' key in health response")
	}
}

func TestStatsHandlerFields(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/stats")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

func TestAuditLogsHandler(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/audit?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if _, ok := result["logs"]; !ok {
		t.Error("expected 'logs' key in audit response")
	}
}

func TestRewardsHandler(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/rewards?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if _, ok := result["rewards"]; !ok {
		t.Error("expected 'rewards' key in rewards response")
	}
}

func TestModelsListPagination(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)

	// Create 5 models via the admin API.
	for i := range 5 {
		model := router.Model{
			ID: fmt.Sprintf("model-%d", i), ProviderID: "p1",
			Weight: 5, MaxContextTokens: 4096, Enabled: true,
		}
		body, _ := json.Marshal(model)
		resp, _ := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
		_ = resp.Body.Close()
	}

	// Fetch with limit=2, offset=1.
	resp, err := http.Get(ts.URL + "/admin/v1/models?limit=2&offset=1")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	items, ok := result["items"].([]any)
	if !ok {
		t.Fatal("expected items array")
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
	if result["total"] != float64(5) {
		t.Errorf("expected total=5, got %v", result["total"])
	}
	if result["limit"] != float64(2) {
		t.Errorf("expected limit=2, got %v", result["limit"])
	}
	if result["offset"] != float64(1) {
		t.Errorf("expected offset=1, got %v", result["offset"])
	}

	// Without pagination params, should return all items.
	resp2, err := http.Get(ts.URL + "/admin/v1/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	var result2 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&result2)
	items2, _ := result2["items"].([]any)
	if len(items2) != 5 {
		t.Errorf("expected all 5 items without pagination, got %d", len(items2))
	}
}

func TestAPIKeysListPagination(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// The test setup already creates 1 API key. Fetch with limit and offset.
	resp, err := http.Get(ts.URL + "/admin/v1/apikeys?limit=10&offset=0")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if _, ok := result["keys"]; !ok {
		t.Fatal("expected keys in response")
	}
	if _, ok := result["total"]; !ok {
		t.Fatal("expected total in response")
	}
	if _, ok := result["limit"]; !ok {
		t.Fatal("expected limit in response")
	}
	if _, ok := result["offset"]; !ok {
		t.Fatal("expected offset in response")
	}
}
