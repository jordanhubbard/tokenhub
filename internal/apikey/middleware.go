package apikey

import (
	"context"
	"encoding/json"
	"log/slog"
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
// Returns 401 for missing/invalid keys, 403 for insufficient scopes,
// and 429 if the API key has exceeded its monthly budget.
// The budgetChecker parameter is optional; pass nil to skip budget enforcement.
func AuthMiddleware(mgr *Manager, budgetChecker *BudgetChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := r.Header.Get("X-Real-IP")
			if clientIP == "" {
				clientIP = r.RemoteAddr
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				slog.Warn("api key auth: missing token", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
				http.Error(w, "authorization required", http.StatusUnauthorized)
				return
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				slog.Warn("api key auth: invalid format", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
				http.Error(w, "invalid authorization format", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")

			if !strings.HasPrefix(token, keyPrefix) {
				slog.Warn("api key auth: invalid prefix", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
				http.Error(w, "invalid api key format", http.StatusUnauthorized)
				return
			}

			rec, err := mgr.Validate(r.Context(), token)
			if err != nil {
				slog.Warn("api key auth: validation failed", slog.String("ip", clientIP), slog.String("path", r.URL.Path), slog.String("error", err.Error()))
				http.Error(w, "invalid api key", http.StatusUnauthorized)
				return
			}

			// Check scope for this endpoint.
			if !CheckScope(rec, r.URL.Path) {
				slog.Warn("api key auth: insufficient scope", slog.String("ip", clientIP), slog.String("key_id", rec.ID), slog.String("path", r.URL.Path))
				http.Error(w, "insufficient scope", http.StatusForbidden)
				return
			}

			// Check monthly budget.
			if budgetChecker != nil {
				if err := budgetChecker.CheckBudget(r.Context(), rec); err != nil {
					if budgetErr, ok := err.(*BudgetExceededError); ok {
						slog.Warn("api key auth: budget exceeded",
							slog.String("ip", clientIP),
							slog.String("key_id", rec.ID),
							slog.Float64("budget_usd", budgetErr.BudgetUSD),
							slog.Float64("spent_usd", budgetErr.SpentUSD))
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusTooManyRequests)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"error":      "monthly budget exceeded",
							"budget_usd": budgetErr.BudgetUSD,
							"spent_usd":  budgetErr.SpentUSD,
						})
						return
					}
					// Non-budget error — log but don't block the request.
					slog.Warn("api key auth: budget check error",
						slog.String("key_id", rec.ID),
						slog.String("error", err.Error()))
				}
			}

			ctx := context.WithValue(r.Context(), apiKeyContextKey, rec)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
