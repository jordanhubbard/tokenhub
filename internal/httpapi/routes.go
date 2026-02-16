package httpapi

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	tokenhub "github.com/jordanhubbard/tokenhub"
	"go.temporal.io/sdk/client"

	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
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
	APIKeyMgr *apikey.Manager

	// Temporal workflow client (nil when Temporal is disabled).
	TemporalClient    client.Client
	TemporalTaskQueue string
}

func MountRoutes(r chi.Router, d Dependencies) {
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

	// Serve the embedded admin UI at /admin.
	sub, _ := fs.Sub(tokenhub.WebFS, "web")
	r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html for the SPA entry point.
		f, err := sub.Open("index.html")
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tokenhub":     "admin",
				"vault_locked": d.Vault.IsLocked(),
			})
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		stat, _ := f.Stat()
		http.ServeContent(w, r, "index.html", stat.ModTime(), f.(readSeeker))
	})

	// Static assets served under /_assets/ to avoid conflicts with /admin/v1.
	r.Handle("/_assets/*", http.StripPrefix("/_assets/", http.FileServer(http.FS(sub))))

	// JSON API for programmatic access.
	r.Get("/admin/api/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokenhub":     "admin",
			"vault_locked": d.Vault.IsLocked(),
		})
	})

	r.Route("/v1", func(r chi.Router) {
		// Apply API key auth middleware if key manager is configured.
		if d.APIKeyMgr != nil {
			r.Use(apikey.AuthMiddleware(d.APIKeyMgr))
		}
		r.Post("/chat", ChatHandler(d))
		r.Post("/plan", PlanHandler(d))
	})

	r.Route("/admin/v1", func(r chi.Router) {
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

// readSeeker combines io.ReadSeeker for http.ServeContent.
type readSeeker interface {
	Read(p []byte) (n int, err error)
	Seek(offset int64, whence int) (int64, error)
}
