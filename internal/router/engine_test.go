package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// mockSender is a configurable mock that implements Sender.
type mockSender struct {
	id        string
	responses map[string]mockResponse // keyed by model
	mu        sync.Mutex
	calls     []mockCall
}

type mockResponse struct {
	data json.RawMessage
	err  error
}

type mockCall struct {
	Model string
	Req   Request
}

func newMockSender(id string) *mockSender {
	return &mockSender{id: id, responses: make(map[string]mockResponse)}
}

func (m *mockSender) ID() string { return m.id }

func (m *mockSender) Send(ctx context.Context, model string, req Request) (ProviderResponse, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Model: model, Req: req})
	m.mu.Unlock()
	if r, ok := m.responses[model]; ok {
		return r.data, r.err
	}
	// Default: success with OpenAI-style response
	return json.RawMessage(`{"choices":[{"message":{"content":"mock response"}}]}`), nil
}

func (m *mockSender) ClassifyError(err error) *ClassifiedError {
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	return &ClassifiedError{Err: err, Class: ErrFatal}
}

func (m *mockSender) setResponse(model string, data json.RawMessage, err error) {
	m.responses[model] = mockResponse{data: data, err: err}
}

func (m *mockSender) setError(model string, class ErrorClass) {
	m.responses[model] = mockResponse{
		err: &ClassifiedError{Err: fmt.Errorf("%s error", class), Class: class},
	}
}

func makeRequest(content string) Request {
	return Request{
		Messages: []Message{{Role: "user", Content: content}},
	}
}

func oaiResponse(content string) json.RawMessage {
	r, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	})
	return r
}

func TestEstimateTokens(t *testing.T) {
	req := makeRequest("hello world test message")
	got := EstimateTokens(req)
	// "hello world test message" = 24 chars, 24/4 = 6
	if got != 6 {
		t.Errorf("estimateTokens() = %d, want 6", got)
	}
}

func TestEstimateTokensExplicit(t *testing.T) {
	req := Request{
		Messages:             []Message{{Role: "user", Content: "hello"}},
		EstimatedInputTokens: 100,
	}
	got := EstimateTokens(req)
	if got != 100 {
		t.Errorf("estimateTokens() = %d, want 100 (explicit)", got)
	}
}

func TestModelRegistration(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("test-provider")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{
		ID: "test-model", ProviderID: "test-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "test-model" {
		t.Errorf("expected model test-model, got %s", dec.ModelID)
	}
}

func TestSelectionByWeight(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{ID: "low", ProviderID: "p1", Weight: 1, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "high", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "mid", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "high" {
		t.Errorf("expected highest weight model 'high', got %s", dec.ModelID)
	}
}

func TestBudgetConstraint(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	// Expensive model: 1.0 per 1k input => even small request will be expensive
	eng.RegisterModel(Model{
		ID: "expensive", ProviderID: "p1", Weight: 10,
		MaxContextTokens: 4096, InputPer1K: 1.0, OutputPer1K: 1.0, Enabled: true,
	})
	// Cheap model
	eng.RegisterModel(Model{
		ID: "cheap", ProviderID: "p1", Weight: 3,
		MaxContextTokens: 4096, InputPer1K: 0.0001, OutputPer1K: 0.0001, Enabled: true,
	})

	// Budget of $0.001 should exclude the expensive model
	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{MaxBudgetUSD: 0.001})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "cheap" {
		t.Errorf("expected cheap model under budget, got %s", dec.ModelID)
	}
}

func TestContextSizeConstraint(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{
		ID: "small", ProviderID: "p1", Weight: 10,
		MaxContextTokens: 10, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "large", ProviderID: "p1", Weight: 5,
		MaxContextTokens: 100000, Enabled: true,
	})

	// Request with lots of content should skip the small model
	bigContent := make([]byte, 200)
	for i := range bigContent {
		bigContent[i] = 'a'
	}
	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest(string(bigContent)), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "large" {
		t.Errorf("expected large context model, got %s", dec.ModelID)
	}
}

func TestMinWeightConstraint(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{ID: "low", ProviderID: "p1", Weight: 2, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "high", ProviderID: "p1", Weight: 8, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{MinWeight: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "high" {
		t.Errorf("expected high weight model, got %s", dec.ModelID)
	}
}

func TestNoEligibleModels(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	_, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err == nil {
		t.Fatal("expected error for no eligible models")
	}
	if err.Error() != "no eligible models registered" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEscalationContextOverflow(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{ID: "small", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "large", ProviderID: "p1", Weight: 5, MaxContextTokens: 200000, Enabled: true})

	// small model returns context_overflow
	mock.setError("small", ErrContextOverflow)
	// large model succeeds
	mock.setResponse("large", oaiResponse("escalated answer"), nil)

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "escalated-context-overflow" {
		t.Errorf("expected escalated-context-overflow reason, got %s", dec.Reason)
	}
	if dec.ModelID != "large" {
		t.Errorf("expected large model after escalation, got %s", dec.ModelID)
	}
}

func TestEscalationRateLimited(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock1 := newMockSender("p1")
	mock2 := newMockSender("p2")
	eng.RegisterAdapter(mock1)
	eng.RegisterAdapter(mock2)

	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "m2", ProviderID: "p2", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	mock1.setError("m1", ErrRateLimited)
	mock2.setResponse("m2", oaiResponse("from p2"), nil)

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ProviderID != "p2" {
		t.Errorf("expected fallback to p2, got %s", dec.ProviderID)
	}
}

func TestEscalationTransient(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	retrier := &retryMock{callCount: 0}
	eng.RegisterAdapter(retrier)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "retry-provider", Weight: 10, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retrier.callCount != 2 {
		t.Errorf("expected 2 calls (initial + retry), got %d", retrier.callCount)
	}
	if dec.Reason != "retried-transient" {
		t.Errorf("expected retried-transient reason, got %s", dec.Reason)
	}
}

// retryMock returns transient error on first call, success on second.
type retryMock struct {
	callCount int
}

func (r *retryMock) ID() string { return "retry-provider" }

func (r *retryMock) Send(ctx context.Context, model string, req Request) (ProviderResponse, error) {
	r.callCount++
	if r.callCount == 1 {
		return nil, &ClassifiedError{Err: errors.New("server error"), Class: ErrTransient}
	}
	return json.RawMessage(`{"choices":[{"message":{"content":"retry success"}}]}`), nil
}

func (r *retryMock) ClassifyError(err error) *ClassifiedError {
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	return &ClassifiedError{Err: err, Class: ErrFatal}
}

func TestFatalError(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})
	mock.setError("m1", ErrFatal)

	_, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err == nil {
		t.Fatal("expected error after fatal")
	}
	if err.Error() != "all providers failed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOrchestrateSingleRoute(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.Orchestrate(context.Background(), makeRequest("hi"), OrchestrationDirective{Mode: "planning"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "m1" {
		t.Errorf("expected m1, got %s", dec.ModelID)
	}
}

func TestOrchestrateAdversarial(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})

	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("build a plan"), OrchestrationDirective{
		Mode:       "adversarial",
		Iterations: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "adversarial-orchestration" {
		t.Errorf("expected adversarial-orchestration reason, got %s", dec.Reason)
	}
	// Response should contain initial_plan, critique, refined_plan
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	for _, key := range []string{"initial_plan", "critique", "refined_plan"} {
		if _, ok := result[key]; !ok {
			t.Errorf("expected key %s in adversarial response", key)
		}
	}
}

func TestExtractContentOpenAI(t *testing.T) {
	resp := oaiResponse("hello from openai")
	got := ExtractContent(resp)
	if got != "hello from openai" {
		t.Errorf("ExtractContent() = %q, want %q", got, "hello from openai")
	}
}

func TestExtractContentAnthropic(t *testing.T) {
	resp, _ := json.Marshal(map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": "hello from anthropic"},
		},
	})
	got := ExtractContent(resp)
	if got != "hello from anthropic" {
		t.Errorf("ExtractContent() = %q, want %q", got, "hello from anthropic")
	}
}

func TestEstimateCostUSD(t *testing.T) {
	cost := estimateCostUSD(1000, 500, 0.01, 0.03)
	// (1000/1000)*0.01 + (500/1000)*0.03 = 0.01 + 0.015 = 0.025
	if cost < 0.024 || cost > 0.026 {
		t.Errorf("estimateCostUSD() = %f, want ~0.025", cost)
	}
}

func TestDefaultPolicyValues(t *testing.T) {
	eng := NewEngine(EngineConfig{
		DefaultMode:         "cheap",
		DefaultMaxBudgetUSD: 0.10,
	})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "m1" {
		t.Errorf("expected m1, got %s", dec.ModelID)
	}
}

func TestModelWithoutAdapterSkipped(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	// This model has no adapter registered
	eng.RegisterModel(Model{ID: "orphan", ProviderID: "missing-provider", Weight: 10, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "ok", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "ok" {
		t.Errorf("expected model 'ok' (has adapter), got %s", dec.ModelID)
	}
}

func TestCheapModePrefersCheapModel(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	// Expensive, high-weight model.
	eng.RegisterModel(Model{
		ID: "expensive", ProviderID: "p1", Weight: 10,
		MaxContextTokens: 4096, InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true,
	})
	// Cheap, low-weight model.
	eng.RegisterModel(Model{
		ID: "cheapo", ProviderID: "p1", Weight: 3,
		MaxContextTokens: 4096, InputPer1K: 0.0005, OutputPer1K: 0.0015, Enabled: true,
	})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{Mode: "cheap"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "cheapo" {
		t.Errorf("cheap mode: expected cheapo model, got %s", dec.ModelID)
	}
}

func TestHighConfidenceModePrefersBestModel(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{
		ID: "strong", ProviderID: "p1", Weight: 10,
		MaxContextTokens: 4096, InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "weak", ProviderID: "p1", Weight: 2,
		MaxContextTokens: 4096, InputPer1K: 0.0005, OutputPer1K: 0.0015, Enabled: true,
	})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{Mode: "high_confidence"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "strong" {
		t.Errorf("high_confidence mode: expected strong model, got %s", dec.ModelID)
	}
}

func TestScoreModels(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	models := []Model{
		{ID: "expensive", Weight: 10, InputPer1K: 0.1, OutputPer1K: 0.3},
		{ID: "cheapo", Weight: 3, InputPer1K: 0.001, OutputPer1K: 0.003},
	}

	scores := eng.scoreModels(models, 100, "cheap")
	// In cheap mode, cost is heavily weighted. Cheapo should have lower score.
	if scores["cheapo"] >= scores["expensive"] {
		t.Errorf("cheap mode: cheapo score (%.4f) should be lower than expensive (%.4f)",
			scores["cheapo"], scores["expensive"])
	}

	scores2 := eng.scoreModels(models, 100, "high_confidence")
	// In high_confidence mode, weight is heavily weighted. Expensive (weight=10) should win.
	if scores2["expensive"] >= scores2["cheapo"] {
		t.Errorf("high_confidence mode: expensive score (%.4f) should be lower than cheapo (%.4f)",
			scores2["expensive"], scores2["cheapo"])
	}
}

func TestContextHeadroom(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	// Model with 100 context tokens.
	eng.RegisterModel(Model{ID: "tight", ProviderID: "p1", Weight: 10, MaxContextTokens: 100, Enabled: true})
	eng.RegisterModel(Model{ID: "roomy", ProviderID: "p1", Weight: 5, MaxContextTokens: 200000, Enabled: true})

	// Request that's 90 tokens: with 15% headroom (103.5) it exceeds 100 context limit.
	req := Request{
		Messages:             []Message{{Role: "user", Content: "hi"}},
		EstimatedInputTokens: 90,
	}
	dec, _, err := eng.RouteAndSend(context.Background(), req, Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "roomy" {
		t.Errorf("expected roomy model due to headroom, got %s", dec.ModelID)
	}
}

func TestDisabledModelSkipped(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)

	eng.RegisterModel(Model{ID: "disabled", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: false})
	eng.RegisterModel(Model{ID: "enabled", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "enabled" {
		t.Errorf("expected 'enabled' model, got %s", dec.ModelID)
	}
}

func TestVoteOrchestration(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	// Two different providers to ensure diverse voting.
	mock1 := newMockSender("p1")
	mock1.responses["m1"] = mockResponse{
		data: json.RawMessage(`{"choices":[{"message":{"content":"Response from model 1"}}]}`),
	}
	mock2 := newMockSender("p2")
	mock2.responses["m2"] = mockResponse{
		data: json.RawMessage(`{"choices":[{"message":{"content":"Response from model 2"}}]}`),
	}
	// Judge response: picks response 1.
	mock1.responses["m-judge"] = mockResponse{
		data: json.RawMessage(`{"choices":[{"message":{"content":"1"}}]}`),
	}

	eng.RegisterAdapter(mock1)
	eng.RegisterAdapter(mock2)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "m2", ProviderID: "p2", Weight: 5, MaxContextTokens: 4096, Enabled: true})
	eng.RegisterModel(Model{ID: "m-judge", ProviderID: "p1", Weight: 10, MaxContextTokens: 4096, Enabled: true})

	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("test prompt"), OrchestrationDirective{
		Mode:       "vote",
		Iterations: 2, // 2 voters
	})
	if err != nil {
		t.Fatalf("vote failed: %v", err)
	}
	if dec.Reason != "vote-orchestration" {
		t.Errorf("expected vote-orchestration reason, got %s", dec.Reason)
	}

	// Parse composite response.
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	responses, ok := result["responses"].([]any)
	if !ok {
		t.Fatal("expected 'responses' array in vote result")
	}
	if len(responses) < 2 {
		t.Errorf("expected at least 2 voter responses, got %d", len(responses))
	}
}

func TestVoteOrchestrationSingleVoter(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true})

	dec, _, err := eng.Orchestrate(context.Background(), makeRequest("test"), OrchestrationDirective{
		Mode:       "vote",
		Iterations: 5, // more voters than models
	})
	if err != nil {
		t.Fatalf("vote failed: %v", err)
	}
	if dec.Reason != "vote-single-response" {
		t.Errorf("expected vote-single-response reason, got %s", dec.Reason)
	}
}

func TestUpdateDefaults(t *testing.T) {
	eng := NewEngine(EngineConfig{DefaultMode: "normal", DefaultMaxBudgetUSD: 0.05, DefaultMaxLatencyMs: 20000})
	eng.UpdateDefaults("cheap", 0.01, 5000)

	// Verify via routing behavior: register a model and observe defaults.
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{ID: "m1", ProviderID: "p1", Weight: 5, MaxContextTokens: 4096, Enabled: true, InputPer1K: 0.001, OutputPer1K: 0.002})

	// With empty policy, should use updated defaults.
	_, _, err := eng.RouteAndSend(context.Background(), makeRequest("hi"), Policy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdversarialExplicitModels(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	// Set up two providers: one for primary, one for review.
	primaryMock := newMockSender("primary-provider")
	reviewMock := newMockSender("review-provider")
	fallbackMock := newMockSender("fallback-provider")
	eng.RegisterAdapter(primaryMock)
	eng.RegisterAdapter(reviewMock)
	eng.RegisterAdapter(fallbackMock)

	eng.RegisterModel(Model{
		ID: "primary-model", ProviderID: "primary-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "review-model", ProviderID: "review-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "fallback-model", ProviderID: "fallback-provider",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	// Configure specific responses for the explicit models.
	primaryMock.setResponse("primary-model", oaiResponse("explicit plan"), nil)
	reviewMock.setResponse("review-model", oaiResponse("explicit critique"), nil)

	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("build something"), OrchestrationDirective{
		Mode:           "adversarial",
		Iterations:     1,
		PrimaryModelID: "primary-model",
		ReviewModelID:  "review-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "adversarial-orchestration" {
		t.Errorf("expected adversarial-orchestration reason, got %s", dec.Reason)
	}

	// Verify the response contains the explicit model outputs.
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if plan, ok := result["initial_plan"].(string); !ok || plan != "explicit plan" {
		t.Errorf("expected initial_plan='explicit plan', got %v", result["initial_plan"])
	}
	if critique, ok := result["critique"].(string); !ok || critique != "explicit critique" {
		t.Errorf("expected critique='explicit critique', got %v", result["critique"])
	}

	// Verify primary-model was called for plan and refine (2 calls), not the fallback.
	primaryMock.mu.Lock()
	primaryCalls := len(primaryMock.calls)
	primaryMock.mu.Unlock()
	if primaryCalls != 2 {
		t.Errorf("expected primary-model to be called 2 times (plan + refine), got %d", primaryCalls)
	}

	// Verify review-model was called for critique (1 call).
	reviewMock.mu.Lock()
	reviewCalls := len(reviewMock.calls)
	reviewMock.mu.Unlock()
	if reviewCalls != 1 {
		t.Errorf("expected review-model to be called 1 time (critique), got %d", reviewCalls)
	}

	// Verify fallback-model was NOT called (explicit models succeeded).
	fallbackMock.mu.Lock()
	fallbackCalls := len(fallbackMock.calls)
	fallbackMock.mu.Unlock()
	if fallbackCalls != 0 {
		t.Errorf("expected fallback-model to not be called, got %d calls", fallbackCalls)
	}
}

func TestAdversarialExplicitModelFallback(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	// Primary model will fail; fallback should be used.
	failMock := newMockSender("fail-provider")
	fallbackMock := newMockSender("fallback-provider")
	eng.RegisterAdapter(failMock)
	eng.RegisterAdapter(fallbackMock)

	eng.RegisterModel(Model{
		ID: "fail-model", ProviderID: "fail-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "fallback-model", ProviderID: "fallback-provider",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	// fail-model returns an error.
	failMock.setResponse("fail-model", nil, fmt.Errorf("model unavailable"))
	// fallback-model succeeds.
	fallbackMock.setResponse("fallback-model", oaiResponse("fallback response"), nil)

	dec, _, err := eng.Orchestrate(context.Background(), makeRequest("build something"), OrchestrationDirective{
		Mode:           "adversarial",
		Iterations:     1,
		PrimaryModelID: "fail-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "adversarial-orchestration" {
		t.Errorf("expected adversarial-orchestration reason, got %s", dec.Reason)
	}

	// The fallback model should have been called (via RouteAndSend) since the explicit model failed.
	fallbackMock.mu.Lock()
	fallbackCalls := len(fallbackMock.calls)
	fallbackMock.mu.Unlock()
	if fallbackCalls == 0 {
		t.Error("expected fallback-model to be called after explicit model failure")
	}
}

func TestVoteExplicitJudge(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	// Set up voter models and an explicit judge model.
	voterMock := newMockSender("voter-provider")
	judgeMock := newMockSender("judge-provider")
	eng.RegisterAdapter(voterMock)
	eng.RegisterAdapter(judgeMock)

	eng.RegisterModel(Model{
		ID: "voter-1", ProviderID: "voter-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "voter-2", ProviderID: "voter-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "explicit-judge", ProviderID: "judge-provider",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	// Configure voter responses.
	voterMock.setResponse("voter-1", oaiResponse("Answer from voter 1"), nil)
	voterMock.setResponse("voter-2", oaiResponse("Answer from voter 2"), nil)
	// Judge picks response 2.
	judgeMock.setResponse("explicit-judge", oaiResponse("2"), nil)

	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("test prompt"), OrchestrationDirective{
		Mode:          "vote",
		Iterations:    2,
		ReviewModelID: "explicit-judge",
	})
	if err != nil {
		t.Fatalf("vote failed: %v", err)
	}
	if dec.Reason != "vote-orchestration" {
		t.Errorf("expected vote-orchestration reason, got %s", dec.Reason)
	}

	// Parse composite response and check that the judge field is the explicit judge.
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	judge, ok := result["judge"].(string)
	if !ok {
		t.Fatal("expected 'judge' field in vote result")
	}
	if judge != "explicit-judge" {
		t.Errorf("expected judge='explicit-judge', got %s", judge)
	}

	// Verify the explicit judge model was called.
	judgeMock.mu.Lock()
	judgeCalls := len(judgeMock.calls)
	judgeMock.mu.Unlock()
	if judgeCalls != 1 {
		t.Errorf("expected explicit-judge to be called 1 time, got %d", judgeCalls)
	}
}

func TestRefineBasic(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{
		ID: "m1", ProviderID: "p1",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	// Set up a response for the model.
	mock.setResponse("m1", oaiResponse("initial response"), nil)

	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("explain Go interfaces"), OrchestrationDirective{
		Mode:       "refine",
		Iterations: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "refine-orchestration" {
		t.Errorf("expected refine-orchestration reason, got %s", dec.Reason)
	}

	// Verify the mock was called 1 (initial) + 3 (iterations) = 4 times.
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 4 {
		t.Errorf("expected 4 calls (1 initial + 3 iterations), got %d", callCount)
	}

	// Verify composite response structure.
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if _, ok := result["refined_response"]; !ok {
		t.Error("expected 'refined_response' key in refine response")
	}
	if _, ok := result["iterations"]; !ok {
		t.Error("expected 'iterations' key in refine response")
	}
	if model, ok := result["model"].(string); !ok || model != "m1" {
		t.Errorf("expected model='m1', got %v", result["model"])
	}
}

func TestRefineExplicitModel(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	primaryMock := newMockSender("primary-provider")
	fallbackMock := newMockSender("fallback-provider")
	eng.RegisterAdapter(primaryMock)
	eng.RegisterAdapter(fallbackMock)

	eng.RegisterModel(Model{
		ID: "primary-model", ProviderID: "primary-provider",
		Weight: 5, MaxContextTokens: 4096, Enabled: true,
	})
	eng.RegisterModel(Model{
		ID: "fallback-model", ProviderID: "fallback-provider",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	primaryMock.setResponse("primary-model", oaiResponse("primary response"), nil)
	fallbackMock.setResponse("fallback-model", oaiResponse("fallback response"), nil)

	dec, _, err := eng.Orchestrate(context.Background(), makeRequest("test"), OrchestrationDirective{
		Mode:           "refine",
		Iterations:     2,
		PrimaryModelID: "primary-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.ModelID != "primary-model" {
		t.Errorf("expected primary-model, got %s", dec.ModelID)
	}
	if dec.Reason != "refine-orchestration" {
		t.Errorf("expected refine-orchestration reason, got %s", dec.Reason)
	}

	// Verify primary-model was called for all phases: 1 initial + 2 refinement = 3.
	primaryMock.mu.Lock()
	primaryCalls := len(primaryMock.calls)
	primaryMock.mu.Unlock()
	if primaryCalls != 3 {
		t.Errorf("expected primary-model to be called 3 times (1 initial + 2 refinement), got %d", primaryCalls)
	}

	// Verify fallback-model was NOT called.
	fallbackMock.mu.Lock()
	fallbackCalls := len(fallbackMock.calls)
	fallbackMock.mu.Unlock()
	if fallbackCalls != 0 {
		t.Errorf("expected fallback-model to not be called, got %d calls", fallbackCalls)
	}
}

func TestRefineDefaultIterations(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	mock := newMockSender("p1")
	eng.RegisterAdapter(mock)
	eng.RegisterModel(Model{
		ID: "m1", ProviderID: "p1",
		Weight: 10, MaxContextTokens: 4096, Enabled: true,
	})

	mock.setResponse("m1", oaiResponse("response"), nil)

	// Iterations=0 should default to 2.
	dec, resp, err := eng.Orchestrate(context.Background(), makeRequest("test"), OrchestrationDirective{
		Mode:       "refine",
		Iterations: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Reason != "refine-orchestration" {
		t.Errorf("expected refine-orchestration reason, got %s", dec.Reason)
	}

	// Verify the mock was called 1 (initial) + 2 (default iterations) = 3 times.
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 default iterations), got %d", callCount)
	}

	// Verify the iterations field in the response is 2 (the default).
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	iterFloat, ok := result["iterations"].(float64)
	if !ok {
		t.Fatal("expected 'iterations' to be a number in refine response")
	}
	if int(iterFloat) != 2 {
		t.Errorf("expected iterations=2 (default), got %d", int(iterFloat))
	}
}
