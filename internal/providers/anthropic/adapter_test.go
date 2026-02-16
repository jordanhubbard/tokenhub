package anthropic

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
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header")
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected /v1/messages, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Claude!"},
			},
			"model": "claude-opus",
			"role":  "assistant",
		})
	}))
	defer ts.Close()

	a := New("anthropic", "test-key", ts.URL)
	resp, err := a.Send(context.Background(), "claude-opus", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(parsed.Content) == 0 || parsed.Content[0].Text != "Hello from Claude!" {
		t.Errorf("unexpected response content")
	}
}

func TestSendRateLimit429(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer ts.Close()

	a := New("anthropic", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "claude-opus", router.Request{
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

func TestSendRateLimit529(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
	}))
	defer ts.Close()

	a := New("anthropic", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "claude-opus", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	classified := a.ClassifyError(err)
	if classified.Class != router.ErrRateLimited {
		t.Errorf("expected ErrRateLimited for 529, got %s", classified.Class)
	}
}

func TestSendPromptTooLong(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"prompt_too_long: prompt is too long"}}`))
	}))
	defer ts.Close()

	a := New("anthropic", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "claude-opus", router.Request{
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

func TestSendServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer ts.Close()

	a := New("anthropic", "test-key", ts.URL)
	_, err := a.Send(context.Background(), "claude-opus", router.Request{
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

func TestSendPayloadIncludesMaxTokens(t *testing.T) {
	var payload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer ts.Close()

	a := New("anthropic", "key", ts.URL)
	_, _ = a.Send(context.Background(), "claude-opus", router.Request{
		Messages: []router.Message{{Role: "user", Content: "hi"}},
	})

	if payload["max_tokens"] != float64(4096) {
		t.Errorf("expected max_tokens=4096, got %v", payload["max_tokens"])
	}
}
