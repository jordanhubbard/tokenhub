package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// DoRequest tests
// ---------------------------------------------------------------------------

func TestDoRequest_success(t *testing.T) {
	want := map[string]string{"message": "hello"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json Content-Type, got %s", ct)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer ts.Close()

	body, err := DoRequest(context.Background(), ts.Client(), ts.URL, map[string]string{"key": "val"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if got["message"] != "hello" {
		t.Errorf("got message=%q, want %q", got["message"], "hello")
	}
}

func TestDoRequest_custom_headers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer tok")
		}
		if got := r.Header.Get("X-Custom"); got != "value" {
			t.Errorf("X-Custom header = %q, want %q", got, "value")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	headers := map[string]string{
		"Authorization": "Bearer tok",
		"X-Custom":      "value",
	}
	_, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRequest_server_error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"something broke"}`))
	}))
	defer ts.Close()

	_, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusInternalServerError)
	}
	if !strings.Contains(se.Body, "something broke") {
		t.Errorf("Body = %q, want it to contain %q", se.Body, "something broke")
	}
}

func TestDoRequest_rate_limit_with_retry_after(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer ts.Close()

	_, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusTooManyRequests)
	}
	if se.RetryAfterSecs != 42 {
		t.Errorf("RetryAfterSecs = %d, want 42", se.RetryAfterSecs)
	}
}

func TestDoRequest_request_id_forwarding(t *testing.T) {
	const wantID = "req-trace-999"
	var gotID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	ctx := WithRequestID(context.Background(), wantID)
	_, err := DoRequest(ctx, ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotID != wantID {
		t.Errorf("X-Request-ID = %q, want %q", gotID, wantID)
	}
}

func TestDoRequest_no_request_id_when_missing(t *testing.T) {
	var gotID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	_, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotID != "" {
		t.Errorf("X-Request-ID should be absent, got %q", gotID)
	}
}

func TestDoRequest_timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	_, err := DoRequest(context.Background(), client, ts.URL, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error = %q, expected it to contain %q", err.Error(), "request failed")
	}
}

func TestDoRequest_non_json_response(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("plain text body"))
	}))
	defer ts.Close()

	body, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// DoRequest returns raw bytes regardless of Content-Type; caller parses.
	if string(body) != "plain text body" {
		t.Errorf("body = %q, want %q", string(body), "plain text body")
	}
}

func TestDoRequest_marshal_error(t *testing.T) {
	// Channels cannot be marshaled to JSON.
	_, err := DoRequest(context.Background(), http.DefaultClient, "http://localhost", make(chan int), nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("error = %q, expected it to contain %q", err.Error(), "marshal")
	}
}

func TestDoRequest_invalid_url(t *testing.T) {
	_, err := DoRequest(context.Background(), http.DefaultClient, "://bad", struct{}{}, nil)
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
}

func TestDoRequest_payload_sent_correctly(t *testing.T) {
	type payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}

	var received payload
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	sent := payload{
		Model: "test-model",
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{Role: "user", Content: "hello"},
		},
	}
	_, err := DoRequest(context.Background(), ts.Client(), ts.URL, sent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Model != "test-model" {
		t.Errorf("received model = %q, want %q", received.Model, "test-model")
	}
	if len(received.Messages) != 1 || received.Messages[0].Content != "hello" {
		t.Errorf("received messages = %+v, unexpected", received.Messages)
	}
}

// ---------------------------------------------------------------------------
// DoStreamRequest tests
// ---------------------------------------------------------------------------

func TestDoStreamRequest_success(t *testing.T) {
	const want = `data: {"chunk":"1"}` + "\n" + `data: {"chunk":"2"}` + "\n"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(want))
	}))
	defer ts.Close()

	rc, err := DoStreamRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if string(got) != want {
		t.Errorf("stream body = %q, want %q", string(got), want)
	}
}

func TestDoStreamRequest_server_error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer ts.Close()

	_, err := DoStreamRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected error for 502 response")
	}

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusBadGateway)
	}
	if se.RetryAfterSecs != 10 {
		t.Errorf("RetryAfterSecs = %d, want 10", se.RetryAfterSecs)
	}
	if !strings.Contains(se.Body, "bad gateway") {
		t.Errorf("Body = %q, want it to contain %q", se.Body, "bad gateway")
	}
}

func TestDoStreamRequest_request_id_forwarding(t *testing.T) {
	const wantID = "stream-req-42"
	var gotID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	ctx := WithRequestID(context.Background(), wantID)
	rc, err := DoStreamRequest(ctx, ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = rc.Close()

	if gotID != wantID {
		t.Errorf("X-Request-ID = %q, want %q", gotID, wantID)
	}
}

func TestDoStreamRequest_timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	_, err := DoStreamRequest(context.Background(), client, ts.URL, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error = %q, expected it to contain %q", err.Error(), "request failed")
	}
}

func TestDoStreamRequest_marshal_error(t *testing.T) {
	_, err := DoStreamRequest(context.Background(), http.DefaultClient, "http://localhost", make(chan int), nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("error = %q, expected it to contain %q", err.Error(), "marshal")
	}
}

func TestDoStreamRequest_close_ends_span(t *testing.T) {
	// Verify that calling Close on the returned ReadCloser does not panic and
	// can be called exactly once without error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stream data"))
	}))
	defer ts.Close()

	rc, err := DoStreamRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read all first, then close.
	data, _ := io.ReadAll(rc)
	if string(data) != "stream data" {
		t.Errorf("data = %q, want %q", string(data), "stream data")
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}

func TestDoStreamRequest_custom_headers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer stream-tok" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer stream-tok")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	headers := map[string]string{"Authorization": "Bearer stream-tok"}
	rc, err := DoStreamRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = rc.Close()
}

// ---------------------------------------------------------------------------
// StatusError tests
// ---------------------------------------------------------------------------

func TestStatusError_Error(t *testing.T) {
	se := &StatusError{StatusCode: 503, Body: "service unavailable"}
	got := se.Error()
	if !strings.Contains(got, "503") {
		t.Errorf("Error() = %q, want it to contain status code 503", got)
	}
	if !strings.Contains(got, "service unavailable") {
		t.Errorf("Error() = %q, want it to contain body text", got)
	}
}

func TestParseRetryAfter_seconds(t *testing.T) {
	se := &StatusError{}
	se.ParseRetryAfter("60")
	if se.RetryAfterSecs != 60 {
		t.Errorf("RetryAfterSecs = %d, want 60", se.RetryAfterSecs)
	}
}

func TestParseRetryAfter_empty(t *testing.T) {
	se := &StatusError{}
	se.ParseRetryAfter("")
	if se.RetryAfterSecs != 0 {
		t.Errorf("RetryAfterSecs = %d, want 0", se.RetryAfterSecs)
	}
}

func TestParseRetryAfter_invalid(t *testing.T) {
	se := &StatusError{}
	se.ParseRetryAfter("not-a-number")
	if se.RetryAfterSecs != 0 {
		t.Errorf("RetryAfterSecs = %d, want 0 for invalid value", se.RetryAfterSecs)
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety
// ---------------------------------------------------------------------------

func TestDoRequest_concurrent(t *testing.T) {
	var count atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := DoRequest(context.Background(), ts.Client(), ts.URL, struct{}{}, nil)
			errs <- err
		}()
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}
	if got := count.Load(); got != n {
		t.Errorf("server received %d requests, want %d", got, n)
	}
}
