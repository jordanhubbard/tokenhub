package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// mockStreamer implements both router.Sender and router.StreamSender for testing.
type mockStreamer struct {
	id   string
	resp json.RawMessage
	data string // SSE data returned by SendStream
	err  error
}

func (m *mockStreamer) ID() string { return m.id }

func (m *mockStreamer) Send(_ context.Context, _ string, _ router.Request) (router.ProviderResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func (m *mockStreamer) SendStream(_ context.Context, _ string, _ router.Request) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(strings.NewReader(m.data)), nil
}

func (m *mockStreamer) ClassifyError(err error) *router.ClassifiedError {
	var ce *router.ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

func TestCompletionsSuccess(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{
		ID: "gpt-4", ProviderID: "p1",
		Weight: 5, MaxContextTokens: 8192, Enabled: true,
	})

	body, _ := json.Marshal(CompletionsRequest{
		Model:    "gpt-4",
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var oai completionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oai); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if oai.Object != "chat.completion" {
		t.Errorf("expected object=chat.completion, got %s", oai.Object)
	}
	if !strings.HasPrefix(oai.ID, "chatcmpl-") {
		t.Errorf("expected id to start with chatcmpl-, got %s", oai.ID)
	}
	if oai.Model != "gpt-4" {
		t.Errorf("expected model=gpt-4, got %s", oai.Model)
	}
	if oai.Created == 0 {
		t.Error("expected created timestamp to be set")
	}
	if oai.Choices == nil {
		t.Error("expected choices to be set")
	}
	if oai.Usage == nil {
		t.Fatal("expected usage to be set")
	}
	if oai.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens=15, got %d", oai.Usage.TotalTokens)
	}
}

func TestCompletionsMissingModel(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(CompletionsRequest{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	// Verify OpenAI error format.
	var errResp openaiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %s", errResp.Error.Type)
	}
	if !strings.Contains(errResp.Error.Message, "model") {
		t.Errorf("expected error about model, got: %s", errResp.Error.Message)
	}
}

func TestCompletionsMissingMessages(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(CompletionsRequest{
		Model: "gpt-4",
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	var errResp openaiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %s", errResp.Error.Type)
	}
	if !strings.Contains(errResp.Error.Message, "messages") {
		t.Errorf("expected error about messages, got: %s", errResp.Error.Message)
	}
}

func TestCompletionsWithParameters(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockSender{
		id:   "p1",
		resp: json.RawMessage(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{
		ID: "gpt-4", ProviderID: "p1",
		Weight: 5, MaxContextTokens: 8192, Enabled: true,
	})

	temp := 0.7
	maxTok := 100
	topP := 0.9
	body, _ := json.Marshal(CompletionsRequest{
		Model:       "gpt-4",
		Messages:    []router.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
		TopP:        &topP,
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var oai completionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oai); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if oai.Model != "gpt-4" {
		t.Errorf("expected gpt-4, got %s", oai.Model)
	}
}

func TestCompletionsBadJSON(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	var errResp openaiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %s", errResp.Error.Type)
	}
}

func TestCompletionsNoEligibleModels(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(CompletionsRequest{
		Model:    "nonexistent-model",
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}

	var errResp openaiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Type != "server_error" {
		t.Errorf("expected type=server_error, got %s", errResp.Error.Type)
	}
}

func TestCompletionsStreamHeaders(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	mock := &mockStreamer{
		id:   "p1",
		data: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{
		ID: "gpt-4", ProviderID: "p1",
		Weight: 5, MaxContextTokens: 8192, Enabled: true,
	})

	body, _ := json.Marshal(CompletionsRequest{
		Model:    "gpt-4",
		Messages: []router.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", ct)
	}
	if resp.Header.Get("X-Negotiated-Model") != "gpt-4" {
		t.Errorf("expected X-Negotiated-Model=gpt-4, got %s", resp.Header.Get("X-Negotiated-Model"))
	}
}

func TestCompletionsAnthropicTranslation(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	// Simulate an Anthropic-style response.
	mock := &mockSender{
		id:   "anthropic",
		resp: json.RawMessage(`{"content":[{"type":"text","text":"Hello from Claude"}],"usage":{"input_tokens":10,"output_tokens":20}}`),
	}
	eng.RegisterAdapter(mock)
	eng.RegisterModel(router.Model{
		ID: "claude-3", ProviderID: "anthropic",
		Weight: 5, MaxContextTokens: 100000, Enabled: true,
	})

	body, _ := json.Marshal(CompletionsRequest{
		Model:    "claude-3",
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})

	resp, err := authPost(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var oai completionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oai); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if oai.Model != "claude-3" {
		t.Errorf("expected claude-3, got %s", oai.Model)
	}

	// Verify choices were constructed from Anthropic content.
	var choices []map[string]any
	if err := json.Unmarshal(oai.Choices, &choices); err != nil {
		t.Fatalf("failed to parse choices: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	msg, ok := choices[0]["message"].(map[string]any)
	if !ok {
		t.Fatal("expected message object in choice")
	}
	if msg["content"] != "Hello from Claude" {
		t.Errorf("expected 'Hello from Claude', got %v", msg["content"])
	}

	// Verify usage was translated.
	if oai.Usage == nil {
		t.Fatal("expected usage to be set")
	}
	if oai.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", oai.Usage.PromptTokens)
	}
	if oai.Usage.CompletionTokens != 20 {
		t.Errorf("expected completion_tokens=20, got %d", oai.Usage.CompletionTokens)
	}
	if oai.Usage.TotalTokens != 30 {
		t.Errorf("expected total_tokens=30, got %d", oai.Usage.TotalTokens)
	}
}
