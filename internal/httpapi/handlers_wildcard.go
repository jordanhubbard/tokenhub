package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

type wildcardModelsResponse struct {
	Models           []string `json:"models"`
	Configured       bool     `json:"configured"`
	ConfiguredModels []string `json:"configured_models,omitempty"`
	Defaults         []string `json:"defaults"`
}

func wildcardModelsSnapshot(engine *router.Engine) wildcardModelsResponse {
	configuredModels, configured := engine.ConfiguredWildcardModelIDs()
	return wildcardModelsResponse{
		Models:           engine.WildcardModelIDs(),
		Configured:       configured,
		ConfiguredModels: configuredModels,
		Defaults:         router.DefaultWildcardRoundRobinModelIDs(),
	}
}

// WildcardModelsGetHandler handles GET /admin/v1/wildcard-models.
func WildcardModelsGetHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wildcardModelsSnapshot(d.Engine))
	}
}

// WildcardModelsSetHandler handles PUT /admin/v1/wildcard-models.
//
// The body may be JSON or YAML, either as {"models":["gpt-5.5"]} or as a
// top-level string list. An empty list clears the override and restores built-in
// defaults.
func WildcardModelsSetHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonError(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		modelIDs, err := router.ParseWildcardModelIDs(body)
		if err != nil {
			jsonError(w, "invalid wildcard model config: "+err.Error(), http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.SaveWildcardModelIDs(r.Context(), modelIDs); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := d.Engine.SetWildcardModelIDs(modelIDs); err != nil {
			jsonError(w, "invalid wildcard model config: "+err.Error(), http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			detail, _ := json.Marshal(map[string]any{"models": modelIDs})
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "wildcard-models.update",
				Resource:  "wildcard-models",
				Detail:    string(detail),
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(wildcardModelsSnapshot(d.Engine))
	}
}
