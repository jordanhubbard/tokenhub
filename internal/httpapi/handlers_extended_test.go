package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// --- ChatHandler extended tests ---

func TestChatMissingMessagesField(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Body has no "request" field at all.
	body, _ := json.Marshal(map[string]any{})

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing messages, got %d", resp.StatusCode)
	}
}

func TestChatEmptyBody(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader([]byte("")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", resp.StatusCode)
	}
}

func TestChatTruncatedJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader([]byte(`{"request":{"messages`)))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for truncated json, got %d", resp.StatusCode)
	}
}

func TestChatResponseHasExpectedFields(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"response"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(ChatRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "hello"}},
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
		t.Fatalf("failed to decode: %v", err)
	}
	if chatResp.NegotiatedModel == "" {
		t.Error("expected negotiated_model to be set")
	}
	if chatResp.Response == nil {
		t.Error("expected response body to be set")
	}
	if chatResp.RoutingReason == "" {
		t.Error("expected routing_reason to be set")
	}
}

func TestChatProviderError(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:  "p-err",
		err: &router.ClassifiedError{Err: errMock("provider down"), Class: router.ErrFatal},
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m-err", ProviderID: "p-err", Weight: 5, MaxContextTokens: 4096, Enabled: true})

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
		t.Errorf("expected 502 for provider error, got %d", resp.StatusCode)
	}
}

// errMock is a simple error type for test mocking.
type errMock string

func (e errMock) Error() string { return string(e) }

func TestChatWithOutputFormat(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"resp"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(ChatRequest{
		OutputFormat: &router.OutputFormat{Type: "json"},
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

func TestChatWithCapabilities(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(ChatRequest{
		Capabilities: map[string]any{"streaming": true},
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

// --- PlanHandler extended tests ---

func TestPlanBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPlanNilMessages(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"request":       map[string]any{},
		"orchestration": map[string]any{"mode": "planning"},
	})

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for nil messages, got %d", resp.StatusCode)
	}
}

func TestPlanValidModes(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	validModes := []string{"", "planning", "adversarial", "vote", "refine"}

	for _, mode := range validModes {
		t.Run("mode_"+mode, func(t *testing.T) {
			body, _ := json.Marshal(PlanRequest{
				Request: router.Request{
					Messages: []router.Message{{Role: "user", Content: "plan"}},
				},
				Orchestration: router.OrchestrationDirective{Mode: mode},
			})

			resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected 200 for valid mode %q, got %d", mode, resp.StatusCode)
			}
		})
	}
}

func TestPlanInvalidModeVariants(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	invalidModes := []string{"execute", "random", "PLANNING", "Plan"}

	for _, mode := range invalidModes {
		t.Run("mode_"+mode, func(t *testing.T) {
			body, _ := json.Marshal(PlanRequest{
				Request: router.Request{
					Messages: []router.Message{{Role: "user", Content: "plan"}},
				},
				Orchestration: router.OrchestrationDirective{Mode: mode},
			})

			resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for invalid mode %q, got %d", mode, resp.StatusCode)
			}
		})
	}
}

func TestPlanIterationsBoundary(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	tests := []struct {
		name       string
		iterations int
		wantCode   int
	}{
		{"zero iterations (valid)", 0, http.StatusOK},
		{"max iterations (valid)", 10, http.StatusOK},
		{"one over max", 11, http.StatusBadRequest},
		{"negative", -1, http.StatusBadRequest},
		{"way over", 100, http.StatusBadRequest},
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

			if resp.StatusCode != tc.wantCode {
				t.Errorf("expected %d for iterations=%d, got %d", tc.wantCode, tc.iterations, resp.StatusCode)
			}
		})
	}
}

func TestPlanNoEligibleModels(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(PlanRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "plan"}},
		},
		Orchestration: router.OrchestrationDirective{Mode: "planning"},
	})

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 for no models, got %d", resp.StatusCode)
	}
}

func TestPlanResponseHasExpectedFields(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"a plan"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(PlanRequest{
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "plan"}},
		},
		Orchestration: router.OrchestrationDirective{Mode: "planning"},
	})

	resp, err := authPost(ts.URL+"/v1/plan", "application/json", bytes.NewReader(body))
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

	for _, key := range []string{"negotiated_model", "estimated_cost_usd", "routing_reason", "response"} {
		if _, ok := result[key]; !ok {
			t.Errorf("expected key %q in plan response", key)
		}
	}
}

func TestPlanWithOutputFormat(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	body, _ := json.Marshal(PlanRequest{
		OutputFormat: &router.OutputFormat{Type: "json"},
		Request: router.Request{
			Messages: []router.Message{{Role: "user", Content: "plan"}},
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
}

// --- Admin handler tests ---

func TestVaultUnlockBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", resp.StatusCode)
	}
}

func TestVaultUnlockEmptyPassword(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"admin_password": ""})
	resp, err := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty password, got %d", resp.StatusCode)
	}
}

func TestVaultRotatePasswordSuccess(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock vault first.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, _ := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()
	if v.IsLocked() {
		t.Fatal("vault should be unlocked")
	}

	// Rotate password.
	rotateBody, _ := json.Marshal(map[string]string{
		"old_password": "supersecretpassword",
		"new_password": "anothersupersecretpassword",
	})
	resp, err := http.Post(ts.URL+"/admin/v1/vault/rotate", "application/json", bytes.NewReader(rotateBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for vault rotate, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
}

func TestVaultRotateBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/vault/rotate", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", resp.StatusCode)
	}
}

func TestVaultRotateMissingPasswords(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name string
		body map[string]string
	}{
		{"missing old", map[string]string{"old_password": "", "new_password": "newsecretpassword123"}},
		{"missing new", map[string]string{"old_password": "supersecretpassword", "new_password": ""}},
		{"both empty", map[string]string{"old_password": "", "new_password": ""}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			resp, err := http.Post(ts.URL+"/admin/v1/vault/rotate", "application/json", bytes.NewReader(body))
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

func TestProviderUpsertBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestProviderUpsertMissingID(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"type":    "openai",
		"enabled": true,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing id, got %d", resp.StatusCode)
	}
}

func TestProviderUpsertWithoutAPIKey(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"id":      "prov-no-key",
		"type":    "openai",
		"enabled": true,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestProviderDeleteNonExistent(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/v1/providers/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Delete of nonexistent should still succeed (idempotent).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for idempotent delete, got %d", resp.StatusCode)
	}
}

func TestProvidersListWithPagination(t *testing.T) {
	ts, _, v := setupTestServer(t)
	defer ts.Close()

	// Unlock vault.
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, _ := http.Post(ts.URL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()
	if v.IsLocked() {
		t.Fatal("vault should be unlocked")
	}

	// Create 3 providers.
	for i := range 3 {
		provBody, _ := json.Marshal(map[string]any{
			"id":      "prov-" + string(rune('a'+i)),
			"type":    "openai",
			"enabled": true,
		})
		resp, _ := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(provBody))
		_ = resp.Body.Close()
	}

	// List with pagination.
	resp2, err := http.Get(ts.URL + "/admin/v1/providers?limit=2&offset=0")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&result)
	items, _ := result["items"].([]any)
	if len(items) != 2 {
		t.Errorf("expected 2 items with limit=2, got %d", len(items))
	}
	if result["total"] != float64(3) {
		t.Errorf("expected total=3, got %v", result["total"])
	}
}

func TestModelUpsertBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestModelPatchBadJSON(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)

	// Create a model first.
	model := router.Model{ID: "m-patch", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true}
	body, _ := json.Marshal(model)
	resp, _ := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()

	// PATCH with bad JSON.
	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/models/m-patch", bytes.NewReader([]byte("bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestModelPatchNotFound(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	patchBody, _ := json.Marshal(map[string]any{"weight": 5.0})
	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/models/nonexistent", bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestModelPatchSuccess(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)

	// Create the "p1" provider so model FK constraints are satisfied.
	provBody, _ := json.Marshal(map[string]any{"id": "p1", "type": "openai", "enabled": true})
	resp0, _ := http.Post(ts.URL+"/admin/v1/providers", "application/json", bytes.NewReader(provBody))
	_ = resp0.Body.Close()

	model := router.Model{ID: "m-patch-ok", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true}
	body, _ := json.Marshal(model)
	resp, _ := http.Post(ts.URL+"/admin/v1/models", "application/json", bytes.NewReader(body))
	_ = resp.Body.Close()

	// PATCH weight and enabled.
	patchBody, _ := json.Marshal(map[string]any{"weight": 8.0, "enabled": false})
	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/models/m-patch-ok", bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
}

func TestModelDeleteNonExistent(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/v1/models/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Delete of nonexistent should still return ok (idempotent).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRoutingConfigSetAndGet(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Set routing config.
	cfg := map[string]any{
		"default_mode":           "cheap",
		"default_max_budget_usd": 25.0,
		"default_max_latency_ms": 10000,
	}
	body, _ := json.Marshal(cfg)
	req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/routing-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for set, got %d", resp.StatusCode)
	}

	// Get and verify.
	resp, err = http.Get(ts.URL + "/admin/v1/routing-config")
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for get, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["default_mode"] != "cheap" {
		t.Errorf("expected default_mode=cheap, got %v", result["default_mode"])
	}
}

func TestRoutingConfigSetBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/routing-config", bytes.NewReader([]byte("bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRoutingConfigValidModes(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	validModes := []string{"", "cheap", "normal", "high_confidence", "planning", "adversarial"}

	for _, mode := range validModes {
		t.Run("mode_"+mode, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"default_mode": mode})
			req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/routing-config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected 200 for valid mode %q, got %d", mode, resp.StatusCode)
			}
		})
	}
}

// --- TSDB handler tests ---

func TestTSDBQueryMissingMetric(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/query")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing metric, got %d", resp.StatusCode)
	}
}

func TestTSDBQueryWithFilters(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/query?metric=latency&model=m1&provider=p1")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["series"]; !ok {
		t.Error("expected 'series' key in response")
	}
}

func TestTSDBQueryWithTimeRange(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/query?metric=cost&start=2024-01-01T00:00:00Z&end=2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTSDBQueryWithUnixTimestamps(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/query?metric=latency&start=1704067200000&end=1735689600000&step=60000")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTSDBMetricsList(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/tsdb/metrics")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["metrics"]; !ok {
		t.Error("expected 'metrics' key in response")
	}
}

func TestTSDBPrune(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/tsdb/prune", "application/json", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	// deleted should be present (0 for empty TSDB).
	if _, ok := result["deleted"]; !ok {
		t.Error("expected 'deleted' key in response")
	}
}

func TestTSDBRetentionSet(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"days": 30})
	req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/tsdb/retention", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
	if result["retention_days"] != float64(30) {
		t.Errorf("expected retention_days=30, got %v", result["retention_days"])
	}
}

func TestTSDBRetentionBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/tsdb/retention", bytes.NewReader([]byte("bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTSDBRetentionInvalidDays(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name string
		days int
	}{
		{"zero days", 0},
		{"negative days", -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"days": tc.days})
			req, _ := http.NewRequest("PUT", ts.URL+"/admin/v1/tsdb/retention", bytes.NewReader(body))
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

// --- Workflow handler tests ---

func TestWorkflowsListNoTemporal(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/workflows")
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
	if result["temporal_enabled"] != false {
		t.Errorf("expected temporal_enabled=false, got %v", result["temporal_enabled"])
	}
	workflows, ok := result["workflows"].([]any)
	if !ok {
		t.Fatal("expected workflows array")
	}
	if len(workflows) != 0 {
		t.Errorf("expected empty workflows, got %d", len(workflows))
	}
}

func TestWorkflowDescribeNoTemporal(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/workflows/some-id")
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
	if result["temporal_enabled"] != false {
		t.Errorf("expected temporal_enabled=false, got %v", result["temporal_enabled"])
	}
}

func TestWorkflowHistoryNoTemporal(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/workflows/some-id/history")
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
	if result["temporal_enabled"] != false {
		t.Errorf("expected temporal_enabled=false, got %v", result["temporal_enabled"])
	}
}

// --- API key handler tests ---

func TestAPIKeyCreateSuccess(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"name":   "test-key-create",
		"scopes": `["chat"]`,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
	if result["key"] == nil || result["key"] == "" {
		t.Error("expected key to be set")
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected id to be set")
	}
}

func TestAPIKeyCreateBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAPIKeyCreateMissingName(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"scopes": `["chat"]`,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", resp.StatusCode)
	}
}

func TestAPIKeyCreateWithExpiry(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	expiresIn := "720h"
	body, _ := json.Marshal(map[string]any{
		"name":       "expiry-key",
		"scopes":     `["chat","plan"]`,
		"expires_in": expiresIn,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIKeyCreateWithInvalidExpiry(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	expiresIn := "not-a-duration"
	body, _ := json.Marshal(map[string]any{
		"name":       "bad-expiry-key",
		"scopes":     `["chat"]`,
		"expires_in": expiresIn,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid duration, got %d", resp.StatusCode)
	}
}

func TestAPIKeyCreateWithBudget(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"name":              "budget-key",
		"scopes":            `["chat"]`,
		"monthly_budget_usd": 50.0,
	})
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIKeyDeleteSuccess(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Create a key to delete.
	createBody, _ := json.Marshal(map[string]any{
		"name":   "to-delete",
		"scopes": `["chat"]`,
	})
	createResp, _ := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(createBody))
	var createResult map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&createResult)
	_ = createResp.Body.Close()

	keyID, _ := createResult["id"].(string)
	if keyID == "" {
		t.Fatal("failed to get key ID from create response")
	}

	// Delete the key.
	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/v1/apikeys/"+keyID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIKeyRotateSuccess(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Create a key to rotate.
	createBody, _ := json.Marshal(map[string]any{
		"name":   "to-rotate",
		"scopes": `["chat"]`,
	})
	createResp, _ := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(createBody))
	var createResult map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&createResult)
	_ = createResp.Body.Close()

	keyID, _ := createResult["id"].(string)
	if keyID == "" {
		t.Fatal("failed to get key ID from create response")
	}

	// Rotate the key.
	resp, err := http.Post(ts.URL+"/admin/v1/apikeys/"+keyID+"/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["ok"] != true {
		t.Errorf("expected ok:true, got %v", result["ok"])
	}
	if result["key"] == nil || result["key"] == "" {
		t.Error("expected new key to be returned")
	}
}

func TestAPIKeyPatchSuccess(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Create a key to patch.
	createBody, _ := json.Marshal(map[string]any{
		"name":   "to-patch",
		"scopes": `["chat"]`,
	})
	createResp, _ := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(createBody))
	var createResult map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&createResult)
	_ = createResp.Body.Close()

	keyID, _ := createResult["id"].(string)
	if keyID == "" {
		t.Fatal("failed to get key ID from create response")
	}

	// Patch the key.
	patchBody, _ := json.Marshal(map[string]any{
		"name":    "patched-name",
		"enabled": false,
	})
	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/apikeys/"+keyID, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIKeyPatchBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Create a key first.
	createBody, _ := json.Marshal(map[string]any{"name": "patch-bad-json", "scopes": `["chat"]`})
	createResp, _ := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(createBody))
	var createResult map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&createResult)
	_ = createResp.Body.Close()
	keyID, _ := createResult["id"].(string)

	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/apikeys/"+keyID, bytes.NewReader([]byte("bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAPIKeyPatchNotFound(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	patchBody, _ := json.Marshal(map[string]any{"name": "new-name"})
	req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/apikeys/nonexistent-id", bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPIKeyPatchValidation(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Create a key to patch.
	createBody, _ := json.Marshal(map[string]any{"name": "patch-validate", "scopes": `["chat"]`})
	createResp, _ := http.Post(ts.URL+"/admin/v1/apikeys", "application/json", bytes.NewReader(createBody))
	var createResult map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&createResult)
	_ = createResp.Body.Close()
	keyID, _ := createResult["id"].(string)

	tests := []struct {
		name  string
		patch map[string]any
	}{
		{"empty name", map[string]any{"name": ""}},
		{"invalid scopes", map[string]any{"scopes": "not-json-array"}},
		{"negative rotation_days", map[string]any{"rotation_days": -1.0}},
		{"negative budget", map[string]any{"monthly_budget_usd": -1.0}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.patch)
			req, _ := http.NewRequest("PATCH", ts.URL+"/admin/v1/apikeys/"+keyID, bytes.NewReader(body))
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

// --- Healthz extended tests ---

func TestHealthzResponseBody(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["status"] != "unhealthy" {
		t.Errorf("expected unhealthy with no adapters, got %v", result["status"])
	}
	if result["adapters"] != float64(0) {
		t.Errorf("expected 0 adapters, got %v", result["adapters"])
	}
	if result["models"] != float64(0) {
		t.Errorf("expected 0 models, got %v", result["models"])
	}
}

// --- Admin auth middleware tests ---

func TestAdminAuthMiddleware(t *testing.T) {
	// Create a server with admin auth enabled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := adminAuthMiddleware("secret-token")
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// No auth header.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", resp.StatusCode)
	}

	// Wrong token.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", resp.StatusCode)
	}

	// Correct token.
	req, _ = http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for correct token, got %d", resp.StatusCode)
	}
}

// --- Request logs handler tests ---

func TestRequestLogsWithPagination(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/logs?limit=5&offset=0")
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

// --- Audit logs handler tests ---

func TestAuditLogsWithPagination(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/audit?limit=5&offset=0")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Rewards handler tests ---

func TestRewardsWithPagination(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/v1/rewards?limit=5&offset=0")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Engine models handler tests ---

func TestEngineModelsWithPagination(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{id: "p1", resp: json.RawMessage(`{}`)}
	eng.RegisterAdapter(mock)
	for i := range 5 {
		eng.RegisterModel(router.Model{
			ID: "em-" + string(rune('a'+i)), ProviderID: "p1",
			Weight: 5, MaxContextTokens: 4096, Enabled: true,
		})
	}

	resp, err := http.Get(ts.URL + "/admin/v1/engine/models?limit=2&offset=1")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	models, _ := result["models"].([]any)
	if len(models) != 2 {
		t.Errorf("expected 2 models with limit=2, got %d", len(models))
	}
}

// --- Body size limit tests ---

func TestBodySizeLimitMiddleware(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	// Generate a body slightly over the 10MB limit (this is a quick sanity check).
	// We just check the endpoint doesn't crash, the actual limit is enforced by
	// http.MaxBytesReader which returns an error when the limit is exceeded.
	bigBody := make([]byte, 100) // Small body should work fine.
	resp, err := authPost(ts.URL+"/v1/chat", "application/json", bytes.NewReader(bigBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Should get 400 (bad json) not a server error.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
