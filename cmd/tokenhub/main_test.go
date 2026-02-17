package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHealthCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	// Extract ":<port>" from the test server URL so runHealthCheck hits it
	// via http://localhost:<port>/healthz.
	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	err := runHealthCheck(port)
	require.NoError(t, err)
}

func TestRunHealthCheck_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	err := runHealthCheck(port)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check returned status 503")
}

func TestRunHealthCheck_ConnectionError(t *testing.T) {
	// Use a port that is almost certainly not listening.
	err := runHealthCheck(":19") // chargen port, unlikely to be in use
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check request failed")
}

func TestVersionIsSet(t *testing.T) {
	// The version variable defaults to "dev" when not overridden by ldflags.
	assert.NotEmpty(t, version)
	assert.Equal(t, "dev", version)
}
