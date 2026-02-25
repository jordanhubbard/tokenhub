package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// DoRequest sends a POST request with a JSON payload and returns the response
// body bytes. It handles JSON marshaling, header setting (Content-Type plus any
// caller-supplied headers), request-ID forwarding, error responses (StatusError
// with Retry-After parsing), and body reading.
func DoRequest(ctx context.Context, client *http.Client, url string, payload any, headers map[string]string) ([]byte, error) {
	// Start a child span if the global tracer is active (OTel enabled).
	ctx, span := otel.Tracer("tokenhub.providers").Start(ctx, "provider.request",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.url", url)),
	)
	defer span.End()

	jsonData, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal failed")
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create request failed")
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Forward request ID for tracing.
	if reqID := GetRequestID(ctx); reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
	// Propagate W3C trace context (traceparent/tracestate) to the provider.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request failed")
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read response failed")
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		se := &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
		se.ParseRetryAfter(resp.Header.Get("Retry-After"))
		span.RecordError(se)
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil, se
	}

	span.SetStatus(codes.Ok, "")
	return body, nil
}

// DoStreamRequest sends a POST request with a JSON payload and returns the raw
// response body for streaming consumption. The caller is responsible for closing
// the returned ReadCloser. It handles JSON marshaling, header setting, request-ID
// forwarding, and error responses (StatusError with Retry-After parsing).
func DoStreamRequest(ctx context.Context, client *http.Client, url string, payload any, headers map[string]string) (io.ReadCloser, error) {
	// Start a child span if the global tracer is active (OTel enabled).
	// Note: the span is NOT ended here because the stream body is read
	// asynchronously by the caller. We record errors inline but rely on
	// the caller closing the body (and the context) for span lifecycle.
	ctx, span := otel.Tracer("tokenhub.providers").Start(ctx, "provider.stream",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.url", url)),
	)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal failed")
		span.End()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create request failed")
		span.End()
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Forward request ID for tracing.
	if reqID := GetRequestID(ctx); reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
	// Propagate W3C trace context (traceparent/tracestate) to the provider.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request failed")
		span.End()
		return nil, fmt.Errorf("request failed: %w", err)
	}

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			span.RecordError(fmt.Errorf("read error response: %w", err))
			span.SetStatus(codes.Error, "read error response failed")
			span.End()
			return nil, fmt.Errorf("failed to read error response: %w", err)
		}
		se := &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
		se.ParseRetryAfter(resp.Header.Get("Retry-After"))
		span.RecordError(se)
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		span.End()
		return nil, se
	}

	// Wrap the response body so the span ends when the caller closes it.
	span.SetStatus(codes.Ok, "")
	return &spanCloser{ReadCloser: resp.Body, span: span}, nil
}

// spanCloser wraps an io.ReadCloser and ends the associated OTel span on Close.
type spanCloser struct {
	io.ReadCloser
	span trace.Span
}

func (sc *spanCloser) Close() error {
	err := sc.ReadCloser.Close()
	sc.span.End()
	return err
}
