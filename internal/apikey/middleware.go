package apikey

import (
	"context"
	"net/http"
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/store"
)

type contextKey string

const apiKeyContextKey contextKey = "apikey"

// FromContext returns the API key record attached to the request context.
func FromContext(ctx context.Context) *store.APIKeyRecord {
	if v, ok := ctx.Value(apiKeyContextKey).(*store.APIKeyRecord); ok {
		return v
	}
	return nil
}

// AuthMiddleware validates Bearer tokens on incoming requests.
// Returns 401 for missing/invalid keys and 403 for insufficient scopes.
func AuthMiddleware(mgr *Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "authorization required", http.StatusUnauthorized)
				return
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "invalid authorization format", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")

			if !strings.HasPrefix(token, keyPrefix) {
				http.Error(w, "invalid api key format", http.StatusUnauthorized)
				return
			}

			rec, err := mgr.Validate(r.Context(), token)
			if err != nil {
				http.Error(w, "invalid api key", http.StatusUnauthorized)
				return
			}

			// Check scope for this endpoint.
			if !CheckScope(rec, r.URL.Path) {
				http.Error(w, "insufficient scope", http.StatusForbidden)
				return
			}

			ctx := context.WithValue(r.Context(), apiKeyContextKey, rec)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
