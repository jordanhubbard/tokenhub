package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

type Dependencies struct {
	Engine  *router.Engine
	Vault   *vault.Vault
	Metrics *metrics.Registry
	Store   store.Store
	Health  *health.Tracker
}

func MountRoutes(r chi.Router, d Dependencies) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Get("/admin", func(w http.ResponseWriter, _ *http.Request) {
		// Placeholder “UI”
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokenhub": "admin-ui-stub",
			"vault_locked": d.Vault.IsLocked(),
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Post("/chat", ChatHandler(d))
		r.Post("/plan", PlanHandler(d))
	})

	r.Route("/admin/v1", func(r chi.Router) {
		r.Post("/vault/unlock", VaultUnlockHandler(d))
		r.Post("/providers", ProvidersUpsertHandler(d))
		r.Post("/models", ModelsUpsertHandler(d))
		r.Get("/health", HealthStatsHandler(d))
	})

	r.Handle("/metrics", d.Metrics.Handler())
}
