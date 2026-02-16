package httpapi

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	tokenhub "github.com/jordanhubbard/tokenhub"
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
}

func MountRoutes(r chi.Router, d Dependencies) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
		r.Post("/chat", ChatHandler(d))
		r.Post("/plan", PlanHandler(d))
	})

	r.Route("/admin/v1", func(r chi.Router) {
		r.Post("/vault/unlock", VaultUnlockHandler(d))
		r.Post("/vault/lock", VaultLockHandler(d))
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
		r.Get("/tsdb/query", TSDBQueryHandler(d.TSDB))
		r.Get("/tsdb/metrics", TSDBMetricsHandler(d.TSDB))
		if d.EventBus != nil {
			r.Get("/events", SSEHandler(d.EventBus))
		}
	})

	r.Handle("/metrics", d.Metrics.Handler())
}

// readSeeker combines io.ReadSeeker for http.ServeContent.
type readSeeker interface {
	Read(p []byte) (n int, err error)
	Seek(offset int64, whence int) (int64, error)
}
