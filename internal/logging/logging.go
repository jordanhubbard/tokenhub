package logging

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// sensitiveHeaders are HTTP headers that must never appear in logs.
var sensitiveHeaders = map[string]bool{
	"authorization":   true,
	"x-api-key":       true,
	"proxy-authorization": true,
	"cookie":          true,
	"set-cookie":      true,
}

// globalLevel is the dynamic level variable used by the JSON handler.
// It allows runtime log-level changes via SetLevel without recreating the logger.
var globalLevel = new(slog.LevelVar)

// Setup initializes the global slog logger with the given level.
// The returned logger uses a redacting handler that strips sensitive data.
func Setup(level string) *slog.Logger {
	SetLevel(level)

	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: globalLevel})
	logger := slog.New(&RedactingHandler{base: base})
	slog.SetDefault(logger)
	return logger
}

// SetLevel changes the global log level dynamically at runtime.
// Valid values are "debug", "warn", "error"; anything else defaults to "info".
func SetLevel(level string) {
	switch level {
	case "debug":
		globalLevel.Set(slog.LevelDebug)
	case "warn":
		globalLevel.Set(slog.LevelWarn)
	case "error":
		globalLevel.Set(slog.LevelError)
	default:
		globalLevel.Set(slog.LevelInfo)
	}
}

// RedactingHandler wraps an slog.Handler to redact sensitive attribute values.
type RedactingHandler struct {
	base slog.Handler
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	redacted := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		redacted.AddAttrs(redactAttr(a))
		return true
	})
	return h.base.Handle(ctx, redacted)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var redacted []slog.Attr
	for _, a := range attrs {
		redacted = append(redacted, redactAttr(a))
	}
	return &RedactingHandler{base: h.base.WithAttrs(redacted)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{base: h.base.WithGroup(name)}
}

// redactAttr redacts known-sensitive keys in log attributes.
func redactAttr(a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)

	// Redact auth headers.
	if sensitiveHeaders[key] {
		return slog.String(a.Key, "[REDACTED]")
	}

	// Redact anything that looks like request body content.
	if key == "body" || key == "request_body" || key == "req_body" {
		return slog.String(a.Key, "[REDACTED]")
	}

	// Redact API keys / tokens in values.
	if strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") {
		return slog.String(a.Key, "[REDACTED]")
	}

	return a
}

// RequestLogger returns chi middleware that logs HTTP requests using slog.
// Request bodies and auth headers are never logged.
func RequestLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			reqID := r.Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = middleware.GetReqID(r.Context())
			}

			next.ServeHTTP(ww, r)

			logger.Info("http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", reqID),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}
