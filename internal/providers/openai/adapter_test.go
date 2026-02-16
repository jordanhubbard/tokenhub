package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	classified := a.ClassifyError(context.DeadlineExceeded)
	if classified.Class != router.ErrFatal {
		t.Errorf("expected ErrFatal for non-StatusError, got %s", classified.Class)
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

// Ensure unused import is legitimate
var _ = providers.StatusError{}
