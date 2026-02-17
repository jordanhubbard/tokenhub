// Package tracing provides opt-in OpenTelemetry trace propagation for TokenHub.
//
// When enabled via TOKENHUB_OTEL_ENABLED=true, it sets up an OTLP HTTP exporter,
// a TracerProvider, and W3C TraceContext + Baggage propagation. When disabled,
// all functions are no-ops with zero overhead.
package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds the OTel tracing configuration. When Enabled is false, Setup
// returns a no-op shutdown and all middleware/transport wrappers pass through.
type Config struct {
	Enabled     bool
	Endpoint    string // OTLP HTTP endpoint, e.g. "localhost:4318"
	ServiceName string // resource service name, e.g. "tokenhub"
}

// Setup initialises the OpenTelemetry TracerProvider with an OTLP HTTP exporter.
// It sets the global TextMapPropagator to W3C TraceContext + Baggage so that
// trace context is automatically propagated on outgoing HTTP calls.
//
// The returned shutdown function must be called (typically in a defer or
// server Close) to flush pending spans and release resources.
//
// When cfg.Enabled is false, Setup returns a no-op shutdown and nil error.
func Setup(cfg Config) (shutdown func(context.Context) error, err error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	ctx := context.Background()

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.Endpoint),
		otlptracehttp.WithInsecure(), // typical for local collectors
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Middleware returns an HTTP middleware that instruments incoming requests with
// OTel tracing. When OTel is not enabled (no global TracerProvider set), the
// otelhttp middleware effectively becomes a no-op.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "tokenhub.request")
	}
}

// HTTPTransport wraps a base http.RoundTripper with OTel instrumentation so
// that outgoing HTTP calls propagate the W3C traceparent/tracestate headers.
// If base is nil, http.DefaultTransport is used.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}
