package idempotency

import (
	"bytes"
	"net/http"
)

// Middleware returns an HTTP middleware that provides request idempotency.
// When a request carries an Idempotency-Key header whose value has been seen
// before (and the cached entry has not expired), the cached response is
// replayed with an additional Idempotency-Replay: true header.
// Requests without the header pass through unchanged.
func Middleware(cache *Cache) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

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
