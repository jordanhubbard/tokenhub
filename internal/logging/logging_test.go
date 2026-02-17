package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactingHandlerRedactsAuthHeaders(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("authorization", "Bearer sk-secret"),
		slog.String("x-api-key", "my-key"),
		slog.String("method", "POST"),
	)

	output := buf.String()
	if strings.Contains(output, "sk-secret") {
		t.Error("authorization header value should be redacted")
	}
	if strings.Contains(output, "my-key") {
		t.Error("x-api-key value should be redacted")
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Error("expected [REDACTED] placeholder")
	}
	if !strings.Contains(output, "POST") {
		t.Error("non-sensitive values should be preserved")
	}
}

func TestRedactingHandlerRedactsBody(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test", slog.String("body", `{"messages":[{"role":"user","content":"secret stuff"}]}`))

	output := buf.String()
	if strings.Contains(output, "secret stuff") {
		t.Error("request body should be redacted")
	}
}

func TestRedactingHandlerRedactsKeys(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("api_key", "sk-12345"),
		slog.String("password", "hunter2"),
		slog.String("secret_token", "abc"),
	)

	output := buf.String()
	if strings.Contains(output, "sk-12345") {
		t.Error("api_key value should be redacted")
	}
	if strings.Contains(output, "hunter2") {
		t.Error("password value should be redacted")
	}
	if strings.Contains(output, "abc") {
		t.Error("secret_token value should be redacted")
	}
}

func TestRedactingHandlerPreservesNonSensitive(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("path", "/v1/chat"),
		slog.Int("status", 200),
	)

	output := buf.String()
	if !strings.Contains(output, "/v1/chat") {
		t.Error("path should be preserved")
	}
	if !strings.Contains(output, "200") {
		t.Error("status should be preserved")
	}
}

func TestRedactingHandlerEnabled(t *testing.T) {
	base := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	handler := &RedactingHandler{base: base}

	if handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should not be enabled when level is warn")
	}
	if !handler.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("warn should be enabled")
	}
}

func TestSetupReturnsLogger(t *testing.T) {
	logger := Setup("info")
	if logger == nil {
		t.Error("expected non-nil logger")
	}
}

// --- Redacting handler: additional sensitive attribute name tests ---

func TestRedactingHandler_TokenAttribute(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("token", "eyJhbGciOiJIUzI1NiJ9.payload.signature"),
		slog.String("access_token", "at-abc123"),
		slog.String("refresh_token", "rt-xyz789"),
	)

	output := buf.String()
	if strings.Contains(output, "eyJhbGciOiJIUzI1NiJ9") {
		t.Error("token value should be redacted")
	}
	if strings.Contains(output, "at-abc123") {
		t.Error("access_token value should be redacted")
	}
	if strings.Contains(output, "rt-xyz789") {
		t.Error("refresh_token value should be redacted")
	}
}

func TestRedactingHandler_ProxyAuthorizationAndCookies(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("proxy-authorization", "Basic dXNlcjpwYXNz"),
		slog.String("cookie", "session_id=abc123; csrf=xyz"),
		slog.String("set-cookie", "session_id=new456; HttpOnly"),
	)

	output := buf.String()
	if strings.Contains(output, "dXNlcjpwYXNz") {
		t.Error("proxy-authorization value should be redacted")
	}
	if strings.Contains(output, "abc123") {
		t.Error("cookie value should be redacted")
	}
	if strings.Contains(output, "new456") {
		t.Error("set-cookie value should be redacted")
	}
	// All three should be replaced with [REDACTED]
	if count := strings.Count(output, "[REDACTED]"); count < 3 {
		t.Errorf("expected at least 3 [REDACTED] placeholders, got %d", count)
	}
}

func TestRedactingHandler_RequestBodyVariants(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("request_body", "sensitive request data"),
		slog.String("req_body", "more sensitive data"),
	)

	output := buf.String()
	if strings.Contains(output, "sensitive request data") {
		t.Error("request_body value should be redacted")
	}
	if strings.Contains(output, "more sensitive data") {
		t.Error("req_body value should be redacted")
	}
}

func TestRedactingHandler_SecretAndPasswordVariants(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	logger.Info("test",
		slog.String("client_secret", "cs-secret-value"),
		slog.String("db_password", "p@ssw0rd!"),
		slog.String("api_key_id", "key-id-value"),
	)

	output := buf.String()
	if strings.Contains(output, "cs-secret-value") {
		t.Error("client_secret value should be redacted")
	}
	if strings.Contains(output, "p@ssw0rd!") {
		t.Error("db_password value should be redacted")
	}
	if strings.Contains(output, "key-id-value") {
		t.Error("api_key_id value should be redacted")
	}
}

// --- Edge case: very long attribute values ---

func TestRedactingHandler_VeryLongAttributeValue(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	// A very long non-sensitive value should be preserved as-is.
	longValue := strings.Repeat("a", 10000)
	logger.Info("test", slog.String("description", longValue))

	output := buf.String()
	if !strings.Contains(output, longValue) {
		t.Error("long non-sensitive value should be preserved")
	}
}

func TestRedactingHandler_VeryLongSensitiveValue(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	// A very long sensitive value should be redacted, not leak partially.
	longSecret := strings.Repeat("s", 10000)
	logger.Info("test", slog.String("api_key", longSecret))

	output := buf.String()
	if strings.Contains(output, longSecret) {
		t.Error("long sensitive value should be redacted")
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Error("expected [REDACTED] placeholder for long sensitive value")
	}
}

// --- WithAttrs and WithGroup ---

func TestRedactingHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}

	// WithAttrs should also redact sensitive attributes.
	childHandler := handler.WithAttrs([]slog.Attr{
		slog.String("authorization", "Bearer leaked-token"),
		slog.String("method", "GET"),
	})
	logger := slog.New(childHandler)
	logger.Info("request")

	output := buf.String()
	if strings.Contains(output, "leaked-token") {
		t.Error("authorization in WithAttrs should be redacted")
	}
	if !strings.Contains(output, "GET") {
		t.Error("non-sensitive WithAttrs value should be preserved")
	}
}

func TestRedactingHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}

	groupHandler := handler.WithGroup("request")
	logger := slog.New(groupHandler)
	logger.Info("test", slog.String("path", "/api/v1"))

	output := buf.String()
	if !strings.Contains(output, "request") {
		t.Error("group name should appear in output")
	}
	if !strings.Contains(output, "/api/v1") {
		t.Error("attribute within group should be preserved")
	}
}

// --- SetLevel tests ---

func TestSetLevel_AllLevels(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},           // default
		{"unknown", slog.LevelInfo},    // default for unrecognized
		{"DEBUG", slog.LevelInfo},      // case-sensitive, so defaults to info
		{"WARN", slog.LevelInfo},       // case-sensitive, so defaults to info
	}

	for _, tc := range tests {
		t.Run("level_"+tc.input, func(t *testing.T) {
			SetLevel(tc.input)
			if globalLevel.Level() != tc.expected {
				t.Errorf("SetLevel(%q): got %v, want %v", tc.input, globalLevel.Level(), tc.expected)
			}
		})
	}
}

func TestSetLevel_DynamicChange(t *testing.T) {
	// Verify that changing the level dynamically actually takes effect.
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: globalLevel})
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	// Set to error level: debug messages should be suppressed.
	SetLevel("error")
	logger.Debug("should-not-appear")
	if strings.Contains(buf.String(), "should-not-appear") {
		t.Error("debug message should not appear at error level")
	}

	// Change to debug level: debug messages should now appear.
	buf.Reset()
	SetLevel("debug")
	logger.Debug("should-appear")
	if !strings.Contains(buf.String(), "should-appear") {
		t.Error("debug message should appear at debug level")
	}
}

// --- RequestLogger middleware tests ---

func TestRequestLogger_LogsRequestFields(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	// Create a simple handler that returns 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mw := RequestLogger(logger)
	server := httptest.NewServer(mw(inner))
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	output := buf.String()

	// Parse the JSON log line to verify specific fields.
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Verify the log message.
	if msg, ok := logEntry["msg"].(string); !ok || msg != "http_request" {
		t.Errorf("expected msg 'http_request', got %v", logEntry["msg"])
	}

	// Verify method.
	if method, ok := logEntry["method"].(string); !ok || method != "GET" {
		t.Errorf("expected method 'GET', got %v", logEntry["method"])
	}

	// Verify path.
	if path, ok := logEntry["path"].(string); !ok || path != "/v1/chat/completions" {
		t.Errorf("expected path '/v1/chat/completions', got %v", logEntry["path"])
	}

	// Verify status code (JSON numbers are float64).
	if status, ok := logEntry["status"].(float64); !ok || int(status) != 200 {
		t.Errorf("expected status 200, got %v", logEntry["status"])
	}

	// Verify duration exists (it should be a string like "1.234ms").
	if _, ok := logEntry["duration"]; !ok {
		t.Error("expected duration field in log output")
	}
}

func TestRequestLogger_LogsPostMethod(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	mw := RequestLogger(logger)
	server := httptest.NewServer(mw(inner))
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/tokens", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse log output: %v\nOutput: %s", err, output)
	}

	if method, ok := logEntry["method"].(string); !ok || method != "POST" {
		t.Errorf("expected method 'POST', got %v", logEntry["method"])
	}
	if path, ok := logEntry["path"].(string); !ok || path != "/api/tokens" {
		t.Errorf("expected path '/api/tokens', got %v", logEntry["path"])
	}
	if status, ok := logEntry["status"].(float64); !ok || int(status) != 201 {
		t.Errorf("expected status 201, got %v", logEntry["status"])
	}
}

func TestRequestLogger_LogsErrorStatus(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mw := RequestLogger(logger)
	server := httptest.NewServer(mw(inner))
	defer server.Close()

	resp, err := http.Get(server.URL + "/fail")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse log output: %v\nOutput: %s", err, output)
	}

	if status, ok := logEntry["status"].(float64); !ok || int(status) != 500 {
		t.Errorf("expected status 500, got %v", logEntry["status"])
	}
}

func TestRequestLogger_IncludesRequestID(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	handler := &RedactingHandler{base: base}
	logger := slog.New(handler)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := RequestLogger(logger)
	server := httptest.NewServer(mw(inner))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/test", nil)
	req.Header.Set("X-Request-ID", "req-test-12345")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse log output: %v\nOutput: %s", err, output)
	}

	if reqID, ok := logEntry["request_id"].(string); !ok || reqID != "req-test-12345" {
		t.Errorf("expected request_id 'req-test-12345', got %v", logEntry["request_id"])
	}
}
