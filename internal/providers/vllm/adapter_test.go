package vllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

func TestSendSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// vLLM should NOT send Authorization header
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header for vLLM, got %s", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "Hello from vLLM!"}},
			},
		})
	}))
	defer ts.Close()

	a := New("vllm", ts.URL)
	resp, err := a.Send(context.Background(), "local-model", router.Request{
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
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "Hello from vLLM!" {
		t.Errorf("unexpected response content")
	}
}

func TestSendRateLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer ts.Close()

	a := New("vllm", ts.URL)
	_, err := a.Send(context.Background(), "local-model", router.Request{
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
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer ts.Close()

	a := New("vllm", ts.URL)
	_, err := a.Send(context.Background(), "local-model", router.Request{
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

func TestSendPayload(t *testing.T) {
	var payload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	a := New("vllm", ts.URL)
	_, _ = a.Send(context.Background(), "my-local-model", router.Request{
		Messages: []router.Message{
			{Role: "user", Content: "Hello"},
		},
	})

	if payload["model"] != "my-local-model" {
		t.Errorf("expected model my-local-model, got %v", payload["model"])
	}
}

func TestClassifyNonStatusError(t *testing.T) {
	a := New("vllm", "http://localhost")
	classified := a.ClassifyError(context.DeadlineExceeded)
	if classified.Class != router.ErrFatal {
		t.Errorf("expected ErrFatal for non-StatusError, got %s", classified.Class)
	}
}
