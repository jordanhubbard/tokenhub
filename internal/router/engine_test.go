package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// mockSender is a configurable mock that implements Sender.
type mockSender struct {
	id        string
	responses map[string]mockResponse // keyed by model
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
	m.calls = append(m.calls, mockCall{Model: model, Req: req})
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
	got := estimateTokens(req)
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
	got := estimateTokens(req)
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
	got := extractContent(resp)
	if got != "hello from openai" {
		t.Errorf("extractContent() = %q, want %q", got, "hello from openai")
	}
}

func TestExtractContentAnthropic(t *testing.T) {
	resp, _ := json.Marshal(map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": "hello from anthropic"},
		},
	})
	got := extractContent(resp)
	if got != "hello from anthropic" {
		t.Errorf("extractContent() = %q, want %q", got, "hello from anthropic")
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
