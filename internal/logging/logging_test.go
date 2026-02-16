package logging

import (
	"bytes"
	"context"
	"log/slog"
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
