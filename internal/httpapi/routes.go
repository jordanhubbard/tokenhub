package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	tokenhub "github.com/jordanhubbard/tokenhub"
	"go.temporal.io/sdk/client"

	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/circuitbreaker"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/idempotency"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/ratelimit"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

type Dependencies struct {
	Engine   *router.Engine
	Vault    *vault.Vault
	Metrics  *metrics.Registry
	Store    store.Store
	Health   *health.Tracker
	EventBus *events.Bus
	Stats    *stats.Collector
	TSDB     *tsdb.Store

	// API key management (nil if not configured).
	APIKeyMgr     *apikey.Manager
	BudgetChecker *apikey.BudgetChecker

	// Admin endpoint authentication token (empty = no auth).
	AdminToken string

	// Idempotency cache (nil = idempotency disabled).
	IdempotencyCache *idempotency.Cache

	// Temporal workflow client (nil when Temporal is disabled).
	TemporalClient    client.Client
	TemporalTaskQueue string

	// Circuit breaker for Temporal dispatch (nil when Temporal is disabled).
	CircuitBreaker *circuitbreaker.Breaker

	// Rate limiter for expensive API endpoints (nil = no rate limiting).
	RateLimiter *ratelimit.Limiter
}

// maxRequestBodySize is the maximum allowed request body for POST/PUT/PATCH endpoints (10 MB).
const maxRequestBodySize = 10 << 20

// bodySizeLimit is a middleware that wraps the request body with
// http.MaxBytesReader to enforce a maximum request body size.
func bodySizeLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func MountRoutes(r chi.Router, d Dependencies) {
	// Redirect root to admin dashboard.
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		// Verify the system can actually route requests.
		modelCount := len(d.Engine.ListModels())
		adapterCount := len(d.Engine.ListAdapterIDs())
		if adapterCount == 0 || modelCount == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "unhealthy",
				"adapters": adapterCount,
				"models":   modelCount,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"adapters": adapterCount,
			"models":   modelCount,
		})
	})

	// Serve the embedded admin UI at /admin (with or without trailing slash).
	sub, _ := fs.Sub(tokenhub.WebFS, "web")

	// Compute a content hash from the embedded index.html for cache-busting
	// asset URLs. This changes on every rebuild so browsers always get fresh JS.
	assetVersion := "0"
	if indexBytes, err := fs.ReadFile(sub, "index.html"); err == nil {
		h := sha256.Sum256(indexBytes)
		assetVersion = hex.EncodeToString(h[:8])
	}
	// Inject version query param into script src attributes.
	cachedHTML := ""
	if raw, err := fs.ReadFile(sub, "index.html"); err == nil {
		cachedHTML = strings.ReplaceAll(string(raw),
			"/_assets/cytoscape.min.js", "/_assets/cytoscape.min.js?v="+assetVersion)
		cachedHTML = strings.ReplaceAll(cachedHTML,
			"/_assets/d3.min.js", "/_assets/d3.min.js?v="+assetVersion)
	}

	serveAdmin := func(w http.ResponseWriter, r *http.Request) {
		if cachedHTML == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tokenhub":     "admin",
				"vault_locked": d.Vault.IsLocked(),
			})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Header().Set("ETag", `"`+assetVersion+`"`)
		_, _ = w.Write([]byte(cachedHTML))
	}
	r.Get("/admin", serveAdmin)
	r.Get("/admin/", serveAdmin)

	// Static assets served under /_assets/ to avoid conflicts with /admin/v1.
	// Assets are immutable per release; cache for 1 year with version-busted URLs.
	r.Handle("/_assets/*", http.StripPrefix("/_assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})))

	// JSON API for programmatic access.
	r.Get("/admin/api/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokenhub":          "admin",
			"vault_locked":      d.Vault.IsLocked(),
			"vault_initialized": d.Vault.Salt() != nil,
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(bodySizeLimit(maxRequestBodySize))
		// Apply rate limiting only to expensive API endpoints, not healthz/metrics/admin.
		if d.RateLimiter != nil {
			r.Use(d.RateLimiter.Middleware)
		}
		// Apply idempotency middleware before auth so cached responses are replayed early.
		if d.IdempotencyCache != nil {
			r.Use(idempotency.Middleware(d.IdempotencyCache))
		}
		// Apply API key auth middleware if key manager is configured.
		if d.APIKeyMgr != nil {
			r.Use(apikey.AuthMiddleware(d.APIKeyMgr, d.BudgetChecker))
		}
		r.Post("/chat", ChatHandler(d))
		r.Post("/chat/completions", ChatCompletionsHandler(d))
		r.Post("/plan", PlanHandler(d))
	})

	r.Route("/admin/v1", func(r chi.Router) {
		r.Use(bodySizeLimit(maxRequestBodySize))
		// Protect admin endpoints when an admin token is configured.
		if d.AdminToken != "" {
			r.Use(adminAuthMiddleware(d.AdminToken))
		}

		// API key management endpoints.
		r.Post("/apikeys", APIKeysCreateHandler(d))
		r.Get("/apikeys", APIKeysListHandler(d))
		r.Post("/apikeys/{id}/rotate", APIKeysRotateHandler(d))
		r.Patch("/apikeys/{id}", APIKeysPatchHandler(d))
		r.Delete("/apikeys/{id}", APIKeysDeleteHandler(d))

		// Workflow visibility endpoints.
		r.Get("/workflows", WorkflowsListHandler(d))
		r.Get("/workflows/{id}", WorkflowDescribeHandler(d))
		r.Get("/workflows/{id}/history", WorkflowHistoryHandler(d))

		r.Post("/vault/unlock", VaultUnlockHandler(d))
		r.Post("/vault/lock", VaultLockHandler(d))
		r.Post("/vault/rotate", VaultRotateHandler(d))
		r.Post("/providers", ProvidersUpsertHandler(d))
		r.Get("/providers", ProvidersListHandler(d))
		r.Delete("/providers/{id}", ProvidersDeleteHandler(d))
		r.Post("/models", ModelsUpsertHandler(d))
		r.Get("/models", ModelsListHandler(d))
		r.Patch("/models/{id}", ModelsPatchHandler(d))
		r.Delete("/models/{id}", ModelsDeleteHandler(d))
		r.Get("/routing-config", RoutingConfigGetHandler(d))
		r.Put("/routing-config", RoutingConfigSetHandler(d))
		r.Get("/health", HealthStatsHandler(d))
		r.Get("/stats", StatsHandler(d))
		r.Get("/logs", RequestLogsHandler(d))
		r.Get("/audit", AuditLogsHandler(d))
		r.Get("/rewards", RewardsHandler(d))
		r.Get("/engine/models", EngineModelsHandler(d))
		r.Get("/providers/{id}/discover", ProviderDiscoverHandler(d))
		r.Post("/routing/simulate", RoutingSimulateHandler(d))
		r.Get("/tsdb/query", TSDBQueryHandler(d.TSDB))
		r.Get("/tsdb/metrics", TSDBMetricsHandler(d.TSDB))
		r.Post("/tsdb/prune", TSDBPruneHandler(d.TSDB))
		r.Put("/tsdb/retention", TSDBRetentionHandler(d.TSDB))
		if d.EventBus != nil {
			r.Get("/events", SSEHandler(d.EventBus))
		}
	})

	r.Handle("/metrics", d.Metrics.Handler())

	// Serve built documentation from docs/book/ if available.
	// Build with: make docs (requires mdbook)
	mountDocs(r)
}

func mountDocs(r chi.Router) {
	// Look for docs/book/ in known locations:
	// - docs/book/ relative to working directory (development)
	// - /docs/book/ absolute path (Docker container)
	candidates := []string{
		filepath.Join("docs", "book"),
		"/docs/book",
	}
	for _, docRoot := range candidates {
		if info, err := os.Stat(docRoot); err == nil && info.IsDir() {
			docsFS := http.FileServer(http.Dir(docRoot))
			r.Handle("/docs/*", http.StripPrefix("/docs/", docsFS))
			r.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
			})
			return
		}
	}
}

// adminAuthMiddleware checks for a valid Bearer token on admin endpoints.
func adminAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := r.Header.Get("X-Real-IP")
			if clientIP == "" {
				clientIP = r.RemoteAddr
			}

			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				slog.Warn("admin auth: missing token", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
				http.Error(w, "missing admin token", http.StatusUnauthorized)
				return
			}
			provided := strings.TrimPrefix(auth, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				slog.Warn("admin auth: invalid token", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
				http.Error(w, "invalid admin token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// readSeeker combines io.ReadSeeker for http.ServeContent.
type readSeeker interface {
	Read(p []byte) (n int, err error)
	Seek(offset int64, whence int) (int64, error)
}
