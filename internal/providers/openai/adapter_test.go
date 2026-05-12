package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

func TestSendSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer auth, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected json content type")
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "Hello!"}},
			},
		})
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	resp, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "Hello!" {
		t.Errorf("unexpected response content")
	}
}

// TestSendPreservesToolCallShape asserts that an assistant tool_calls turn
// followed by a tool tool_call_id turn survives the hop to the upstream
// provider. Stripping these fields was producing
// "litellm.BadRequestError: Azure_aiException - 'tool_call_id'" on every
// streaming chat that contained a tool history.
func TestSendPreservesToolCallShape(t *testing.T) {
	var captured map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	toolCallsRaw := json.RawMessage(`[{"id":"call_abc","type":"function","function":{"name":"do_thing","arguments":"{}"}}]`)
	_, err := a.Send(context.Background(), "gpt-x", router.Request{
		Messages: []router.Message{
			{Role: "user", Content: "kick off"},
			{Role: "assistant", Content: "", ToolCalls: toolCallsRaw},
			{Role: "tool", Content: "result text", ToolCallID: "call_abc", Name: "do_thing"},
			{Role: "assistant", Content: "done"},
		},
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	msgs, ok := captured["messages"].([]any)
	if !ok || len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %v", captured["messages"])
	}

	assistantToolCall, _ := msgs[1].(map[string]any)
	if assistantToolCall["tool_calls"] == nil {
		t.Errorf("assistant turn lost tool_calls; got %v", assistantToolCall)
	}

	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "call_abc" {
		t.Errorf("tool message lost tool_call_id; got %v", toolMsg)
	}
	if toolMsg["name"] != "do_thing" {
		t.Errorf("tool message lost name; got %v", toolMsg)
	}
}

func TestSendStreamPreservesToolCallShape(t *testing.T) {
	var captured map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	body, err := a.SendStream(context.Background(), "gpt-x", router.Request{
		Messages: []router.Message{
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]`)},
			{Role: "tool", Content: "v", ToolCallID: "call_1"},
		},
	})
	if err != nil {
		t.Fatalf("SendStream failed: %v", err)
	}
	defer func() { _ = body.Close() }()
	_, _ = io.ReadAll(body)

	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %v", captured["messages"])
	}
	tool, _ := msgs[1].(map[string]any)
	if tool["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id lost on streaming path; got %v", tool)
	}
}

func TestSendStreamDoesNotUseNonStreamingTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL, WithTimeout(10*time.Millisecond))
	body, err := a.SendStream(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("SendStream failed before body read: %v", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("stream read should not inherit 10ms client timeout: %v", err)
	}
	if string(data) != "data: first\n\ndata: second\n\n" {
		t.Fatalf("stream data = %q", data)
	}
}

func TestSendRateLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrRateLimited {
		t.Errorf("expected ErrRateLimited, got %s", classified.Class)
	}
}

func TestSendServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrTransient {
		t.Errorf("expected ErrTransient, got %s", classified.Class)
	}
}

func TestSendContextLengthExceeded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 4096 tokens","code":"context_length_exceeded"}}`))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrContextOverflow {
		t.Errorf("expected ErrContextOverflow, got %s", classified.Class)
	}
}

func TestSendUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer ts.Close()

	a := New("openai", "bad-key", ts.URL)
	_, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrFatal {
		t.Errorf("expected ErrFatal, got %s", classified.Class)
	}
}

func TestClassifyNonStatusError(t *testing.T) {
	a := New("openai", "key", "http://localhost")
	// Network-level errors (timeout, connection refused) must be transient so
	// the engine retries with backoff rather than abandoning the provider.
	classified := a.ClassifyError(context.DeadlineExceeded)
	if classified.Class != router.ErrTransient {
		t.Errorf("expected ErrTransient for network error, got %s", classified.Class)
	}
}

func TestClassifyBudgetExceeded(t *testing.T) {
	a := New("openai", "key", "http://localhost")
	err := &providers.StatusError{StatusCode: 400, Body: `{"error":{"type":"budget_exceeded","message":"Budget has been exceeded!"}}`}
	classified := a.ClassifyError(err)
	if classified.Class != router.ErrBudgetExceeded {
		t.Errorf("expected ErrBudgetExceeded for budget_exceeded, got %s", classified.Class)
	}
}

func TestSendPayload(t *testing.T) {
	var receivedPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	a := New("openai", "key", ts.URL)
	_, _ = a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hello"},
		},
	})

	if receivedPayload["model"] != "gpt-4" {
		t.Errorf("expected model gpt-4, got %v", receivedPayload["model"])
	}
}

func TestRetryAfterHeader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer ts.Close()

	a := New("openai", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "gpt-4", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrRateLimited {
		t.Errorf("expected ErrRateLimited, got %s", classified.Class)
	}
	if classified.RetryAfter != 30 {
		t.Errorf("expected RetryAfter=30, got %d", classified.RetryAfter)
	}
}

func TestSendPayload_PreservesToolCalls(t *testing.T) {
	var receivedPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	// Construct a router.Request from an OpenAI-style tool-chat history
	// (assistant emits tool_calls, then a tool role replies with tool_call_id).
	body := []byte(`{
		"messages": [
			{"role":"user","content":"weather in SF?"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_abc","content":"72F sunny"}
		]
	}`)
	var rr router.Request
	if err := json.Unmarshal(body, &rr); err != nil {
		t.Fatalf("decode router.Request: %v", err)
	}

	a := New("openai", "key", ts.URL)
	if _, err := a.Send(context.Background(), "gpt-4", rr); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs, ok := receivedPayload["messages"].([]any)
	if !ok {
		t.Fatalf("messages should be an array, got %T", receivedPayload["messages"])
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Assistant message must keep tool_calls intact.
	assistant, ok := msgs[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] type: %T", msgs[1])
	}
	if assistant["role"] != "assistant" {
		t.Errorf("assistant.role = %v", assistant["role"])
	}
	toolCalls, ok := assistant["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Fatalf("assistant message missing tool_calls; got %v", assistant)
	}
	tc0, _ := toolCalls[0].(map[string]any)
	if tc0["id"] != "call_abc" {
		t.Errorf("tool_calls[0].id = %v", tc0["id"])
	}
	fn, _ := tc0["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool_calls[0].function.name = %v", fn["name"])
	}
	if fn["arguments"] != `{"city":"SF"}` {
		t.Errorf("tool_calls[0].function.arguments = %v", fn["arguments"])
	}

	// Tool reply must keep tool_call_id intact.
	toolMsg, ok := msgs[2].(map[string]any)
	if !ok {
		t.Fatalf("messages[2] type: %T", msgs[2])
	}
	if toolMsg["role"] != "tool" {
		t.Errorf("tool.role = %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_abc" {
		t.Errorf("tool.tool_call_id = %v", toolMsg["tool_call_id"])
	}
	if toolMsg["content"] != "72F sunny" {
		t.Errorf("tool.content = %v", toolMsg["content"])
	}
}

// Keep providers import used.
var _ = providers.StatusError{}
