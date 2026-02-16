package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/router"
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

	MountRoutes(r, Dependencies{Engine: eng, Vault: v, Metrics: m})
	ts := httptest.NewServer(r)
	return ts, eng, v
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

	resp, err := http.Get(ts.URL + "/admin")
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
	if result["tokenhub"] != "admin-ui-stub" {
		t.Errorf("expected admin-ui-stub, got %v", result["tokenhub"])
	}
}
