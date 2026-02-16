package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

func VaultLockHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Vault.IsLocked() {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "already_locked": true})
			return
		}
		d.Vault.Lock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func VaultUnlockHandler(d Dependencies) http.HandlerFunc {
	type unlockReq struct {
		AdminPassword string `json:"admin_password"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req unlockReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := d.Vault.Unlock([]byte(req.AdminPassword)); err != nil {
			http.Error(w, "unlock failed", http.StatusUnauthorized)
			return
		}
		// Persist vault salt and encrypted data to the store.
		if d.Store != nil {
			salt := d.Vault.Salt()
			data := d.Vault.Export()
			if salt != nil {
				_ = d.Store.SaveVaultBlob(r.Context(), salt, data)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func ProvidersUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p store.ProviderRecord
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if p.ID == "" {
			http.Error(w, "provider id required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.UpsertProvider(r.Context(), p); err != nil {
				http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func ProvidersListHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		providers, err := d.Store.ListProviders(r.Context())
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(providers)
	}
}

func ProvidersDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "provider id required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.DeleteProvider(r.Context(), id); err != nil {
				http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func ModelsUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m router.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// Register in the runtime engine.
		d.Engine.RegisterModel(m)
		// Persist to store.
		if d.Store != nil {
			_ = d.Store.UpsertModel(r.Context(), store.ModelRecord{
				ID:               m.ID,
				ProviderID:       m.ProviderID,
				Weight:           m.Weight,
				MaxContextTokens: m.MaxContextTokens,
				InputPer1K:       m.InputPer1K,
				OutputPer1K:      m.OutputPer1K,
				Enabled:          m.Enabled,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func ModelsListHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		models, err := d.Store.ListModels(r.Context())
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(models)
	}
}

func ModelsDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "model id required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.DeleteModel(r.Context(), id); err != nil {
				http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// ModelsPatchHandler handles PATCH /admin/v1/models/{id} for partial updates (e.g. weight).
func ModelsPatchHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "model id required", http.StatusBadRequest)
			return
		}

		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Load the existing model from store.
		if d.Store == nil {
			http.Error(w, "no store configured", http.StatusInternalServerError)
			return
		}
		existing, err := d.Store.GetModel(r.Context(), id)
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if existing == nil {
			http.Error(w, "model not found", http.StatusNotFound)
			return
		}

		// Apply partial updates.
		if v, ok := patch["weight"]; ok {
			if f, ok := v.(float64); ok {
				existing.Weight = int(f)
			}
		}
		if v, ok := patch["enabled"]; ok {
			if b, ok := v.(bool); ok {
				existing.Enabled = b
			}
		}
		if v, ok := patch["input_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				existing.InputPer1K = f
			}
		}
		if v, ok := patch["output_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				existing.OutputPer1K = f
			}
		}

		// Update store and engine.
		if err := d.Store.UpsertModel(r.Context(), *existing); err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		d.Engine.RegisterModel(router.Model{
			ID:               existing.ID,
			ProviderID:       existing.ProviderID,
			Weight:           existing.Weight,
			MaxContextTokens: existing.MaxContextTokens,
			InputPer1K:       existing.InputPer1K,
			OutputPer1K:      existing.OutputPer1K,
			Enabled:          existing.Enabled,
		})

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "model": existing})
	}
}

// RoutingConfigGetHandler returns the current routing policy defaults.
func RoutingConfigGetHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		cfg, err := d.Store.LoadRoutingConfig(r.Context())
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(cfg)
	}
}

// RoutingConfigSetHandler persists updated routing policy defaults and applies them to the engine.
func RoutingConfigSetHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cfg store.RoutingConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.SaveRoutingConfig(r.Context(), cfg); err != nil {
				http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Apply to engine.
		d.Engine.UpdateDefaults(cfg.DefaultMode, cfg.DefaultMaxBudgetUSD, cfg.DefaultMaxLatencyMs)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func HealthStatsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Health == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": d.Health.AllStats()})
	}
}
