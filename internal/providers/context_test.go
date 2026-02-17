package providers

import (
	"context"
	"testing"
)

func TestWithRequestID_and_GetRequestID(t *testing.T) {
	const id = "req-abc-123"
	ctx := WithRequestID(context.Background(), id)

	got := GetRequestID(ctx)
	if got != id {
		t.Errorf("GetRequestID() = %q, want %q", got, id)
	}
}

func TestGetRequestID_missing(t *testing.T) {
	got := GetRequestID(context.Background())
	if got != "" {
		t.Errorf("GetRequestID() on bare context = %q, want empty string", got)
	}
}

func TestGetRequestID_empty_string(t *testing.T) {
	ctx := WithRequestID(context.Background(), "")

	got := GetRequestID(ctx)
	if got != "" {
		t.Errorf("GetRequestID() = %q, want empty string", got)
	}
}

func TestWithRequestID_overwrites(t *testing.T) {
	ctx := WithRequestID(context.Background(), "first")
	ctx = WithRequestID(ctx, "second")

	got := GetRequestID(ctx)
	if got != "second" {
		t.Errorf("GetRequestID() = %q, want %q", got, "second")
	}
}
