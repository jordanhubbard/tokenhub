package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestRunHealthCheck_InvalidJSON(t *testing.T) {
	// Server returns 200 OK but with invalid JSON body.
	// runHealthCheck only checks the status code, so this should still succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not valid json at all {{{"))
	}))
	defer srv.Close()

	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	err := runHealthCheck(port)
	require.NoError(t, err, "health check should succeed even with invalid JSON body")
}

func TestRunHealthCheck_SlowServer(t *testing.T) {
	// Server deliberately delays the response beyond a reasonable timeout.
	// Since runHealthCheck uses the default http.Client with no explicit timeout,
	// we verify the request eventually completes (or fails if the server is truly unreachable).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	// The default http client has no timeout, so a slow-but-responding server
	// should still succeed.
	err := runHealthCheck(port)
	require.NoError(t, err, "slow server should still succeed when it eventually responds")
}

func TestRunHealthCheck_SlowServer_Closed(t *testing.T) {
	// Start a server that delays, then close it before the health check runs.
	// This tests the behavior when a server is too slow and then goes away.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	// Close the server immediately so the health check will fail to connect.
	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]
	srv.Close()

	err := runHealthCheck(port)
	require.Error(t, err, "health check should fail when server is closed")
	assert.Contains(t, err.Error(), "health check request failed")
}

func TestVersionIsSet(t *testing.T) {
	// The version variable defaults to "dev" when not overridden by ldflags.
	assert.NotEmpty(t, version)
	assert.Equal(t, "dev", version)
}

func TestVersionDefault(t *testing.T) {
	// Verify the version variable has the expected default value.
	// When built without ldflags, version should be "dev".
	assert.Equal(t, "dev", version, "version should default to 'dev' when not set via ldflags")
	assert.NotEqual(t, "", version, "version should never be empty")
}

func TestRunHealthCheck_EmptyBody(t *testing.T) {
	// Server returns 200 OK with an empty body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	err := runHealthCheck(port)
	require.NoError(t, err, "health check should succeed with empty body as long as status is 200")
}

func TestRunHealthCheck_PlainTextBody(t *testing.T) {
	// Server returns 200 OK with a plain text body (not JSON).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer srv.Close()

	parts := strings.TrimPrefix(srv.URL, "http://")
	colonIdx := strings.LastIndex(parts, ":")
	port := parts[colonIdx:]

	err := runHealthCheck(port)
	require.NoError(t, err, "health check should succeed with plain text body")
}

func TestRunHealthCheck_VariousErrorCodes(t *testing.T) {
	codes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	}

	for _, code := range codes {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()

			parts := strings.TrimPrefix(srv.URL, "http://")
			colonIdx := strings.LastIndex(parts, ":")
			port := parts[colonIdx:]

			err := runHealthCheck(port)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "health check returned status")
		})
	}
}
