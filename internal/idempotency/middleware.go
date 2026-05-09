package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/apikey"
)

// Middleware returns an HTTP middleware that provides request idempotency.
// When a request carries an Idempotency-Key header whose value has been seen
// before (and the cached entry has not expired), the cached response is
// replayed with an additional Idempotency-Replay: true header.
// Requests without the header pass through unchanged.
func Middleware(cache *Cache) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := r.Header.Get("Idempotency-Key")
			if rawKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))

			key := scopedKey(r, rawKey, body)

			// Return cached response if available.
			if e, ok := cache.Get(key); ok {
				for k, v := range e.Headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Idempotency-Replay", "true")
				w.WriteHeader(e.StatusCode)
				_, _ = w.Write(e.Response)
				return
			}

			// Capture the response so we can cache it.
			rec := &responseRecorder{
				ResponseWriter: w,
				body:           &bytes.Buffer{},
				statusCode:     http.StatusOK,
			}
			next.ServeHTTP(rec, r)

			if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
				return
			}
			// Only cache responses with status < 500. Server errors (5xx)
			// are transient and should not be cached so the client can
			// retry the same idempotency key successfully.
			if rec.statusCode >= 500 {
				return
			}

			// Cache the captured response.
			hdrs := make(map[string]string)
			for k, v := range rec.Header() {
				if len(v) > 0 {
					hdrs[k] = v[0]
				}
			}
			cache.Set(key, rec.body.Bytes(), rec.statusCode, hdrs)
		})
	}
}

func scopedKey(r *http.Request, rawKey string, body []byte) string {
	principal := "anonymous"
	if rec := apikey.FromContext(r.Context()); rec != nil && rec.ID != "" {
		principal = "apikey:" + rec.ID
	}
	bodyHash := sha256.Sum256(body)
	return strings.Join([]string{
		principal,
		r.Method,
		r.URL.EscapedPath(),
		r.URL.RawQuery,
		rawKey,
		hex.EncodeToString(bodyHash[:]),
	}, "\x00")
}

// responseRecorder wraps an http.ResponseWriter to capture the response body
// and status code while still writing to the original writer.
type responseRecorder struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	written    bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.written {
		r.statusCode = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
