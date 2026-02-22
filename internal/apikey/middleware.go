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

// KeyRateLimiter is the subset of the ratelimit.Limiter interface used by
// AuthMiddleware for per-API-key rate limiting.
type KeyRateLimiter interface {
	AllowCustom(key string, rate, burst int) bool
}

// FromContext returns the API key record attached to the request context.
func FromContext(ctx context.Context) *store.APIKeyRecord {
	if v, ok := ctx.Value(apiKeyContextKey).(*store.APIKeyRecord); ok {
		return v
	}
	return nil
}

// AuthMiddleware validates Bearer tokens on incoming requests.
// Returns 401 for missing/invalid keys, 403 for insufficient scopes,
// and 429 if the API key has exceeded its monthly budget or per-key rate limit.
// budgetChecker and keyLimiter are optional; pass nil to skip.
// globalRPS is the fallback RPS used when a key's RateLimitRPS is 0; pass 0
// to skip per-key rate limiting entirely.
func AuthMiddleware(mgr *Manager, budgetChecker *BudgetChecker, keyLimiter KeyRateLimiter, globalRPS int) func(http.Handler) http.Handler {
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

			// Per-API-key rate limiting (independent of per-IP limit).
			// rec.RateLimitRPS == -1 means unlimited; 0 means use globalRPS.
			if keyLimiter != nil && globalRPS > 0 {
				rps := rec.RateLimitRPS
				if rps == 0 {
					rps = globalRPS
				}
				if rps > 0 && !keyLimiter.AllowCustom("apikey:"+rec.ID, rps, rps*2) {
					slog.Warn("api key auth: rate limit exceeded",
						slog.String("ip", clientIP),
						slog.String("key_id", rec.ID),
						slog.Int("rps", rps))
					w.Header().Set("Retry-After", "1")
					http.Error(w, "api key rate limit exceeded", http.StatusTooManyRequests)
					return
				}
			}

			ctx := context.WithValue(r.Context(), apiKeyContextKey, rec)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
