package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "vault.lock",
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
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
				warnOnErr("save_vault", d.Store.SaveVaultBlob(r.Context(), salt, data))
			}
		}
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "vault.unlock",
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func VaultRotateHandler(d Dependencies) http.HandlerFunc {
	type rotateReq struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req rotateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.OldPassword == "" || req.NewPassword == "" {
			http.Error(w, "old_password and new_password required", http.StatusBadRequest)
			return
		}

		if err := d.Vault.RotatePassword([]byte(req.OldPassword), []byte(req.NewPassword)); err != nil {
			// Distinguish validation errors from internal errors.
			switch err.Error() {
			case "vault is locked", "vault is not enabled", "new password too short":
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "rotation failed: "+err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// Persist the new vault blob to the store.
		if d.Store != nil {
			salt := d.Vault.Salt()
			data := d.Vault.Export()
			if salt != nil {
				if err := d.Store.SaveVaultBlob(r.Context(), salt, data); err != nil {
					http.Error(w, "failed to persist vault: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}

		// Log audit entry.
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "vault.rotate",
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// ProviderUpsertRequest extends ProviderRecord with optional credential.
type ProviderUpsertRequest struct {
	store.ProviderRecord
	APIKey string `json:"api_key,omitempty"` // optional; stored in vault if provided
}

func ProvidersUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ProviderUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			http.Error(w, "provider id required", http.StatusBadRequest)
			return
		}

		// Store API key in vault if provided.
		if req.APIKey != "" && d.Vault != nil && !d.Vault.IsLocked() {
			if err := d.Vault.Set("provider:"+req.ID+":api_key", req.APIKey); err != nil {
				http.Error(w, "vault error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			req.CredStore = "vault"
			// Persist vault data.
			if d.Store != nil {
				salt := d.Vault.Salt()
				data := d.Vault.Export()
				if salt != nil {
					warnOnErr("save_vault", d.Store.SaveVaultBlob(r.Context(), salt, data))
				}
			}
		}

		if d.Store != nil {
			if err := d.Store.UpsertProvider(r.Context(), req.ProviderRecord); err != nil {
				http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "provider.upsert",
				Resource:  req.ID,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cred_store": req.CredStore})
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
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "provider.delete",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
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

		// Validate required fields.
		if m.ID == "" {
			http.Error(w, "model id required", http.StatusBadRequest)
			return
		}
		if m.ProviderID == "" {
			http.Error(w, "provider_id required", http.StatusBadRequest)
			return
		}
		if m.Weight < 0 || m.Weight > 10 {
			http.Error(w, "weight must be between 0 and 10", http.StatusBadRequest)
			return
		}
		if m.InputPer1K < 0 {
			http.Error(w, "input_per_1k must be >= 0", http.StatusBadRequest)
			return
		}
		if m.OutputPer1K < 0 {
			http.Error(w, "output_per_1k must be >= 0", http.StatusBadRequest)
			return
		}

		// Register in the runtime engine.
		d.Engine.RegisterModel(m)
		// Persist to store.
		if d.Store != nil {
			warnOnErr("upsert_model", d.Store.UpsertModel(r.Context(), store.ModelRecord{
				ID:               m.ID,
				ProviderID:       m.ProviderID,
				Weight:           m.Weight,
				MaxContextTokens: m.MaxContextTokens,
				InputPer1K:       m.InputPer1K,
				OutputPer1K:      m.OutputPer1K,
				Enabled:          m.Enabled,
			}))
		}
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "model.upsert",
				Resource:  m.ID,
				RequestID: middleware.GetReqID(r.Context()),
			}))
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
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "model.delete",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
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

		// Validate patch values before applying.
		if v, ok := patch["weight"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 || f > 10 {
					http.Error(w, "weight must be between 0 and 10", http.StatusBadRequest)
					return
				}
			}
		}
		if v, ok := patch["input_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 {
					http.Error(w, "input_per_1k must be >= 0", http.StatusBadRequest)
					return
				}
			}
		}
		if v, ok := patch["output_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 {
					http.Error(w, "output_per_1k must be >= 0", http.StatusBadRequest)
					return
				}
			}
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
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "model.patch",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

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

		// Validate routing config fields.
		switch cfg.DefaultMode {
		case "", "cheap", "normal", "high_confidence", "planning", "adversarial":
			// valid
		default:
			http.Error(w, "unknown default_mode", http.StatusBadRequest)
			return
		}
		if cfg.DefaultMaxBudgetUSD < 0 || cfg.DefaultMaxBudgetUSD > 100 {
			http.Error(w, "default_max_budget_usd must be between 0 and 100", http.StatusBadRequest)
			return
		}
		if cfg.DefaultMaxLatencyMs < 0 || cfg.DefaultMaxLatencyMs > 300000 {
			http.Error(w, "default_max_latency_ms must be between 0 and 300000", http.StatusBadRequest)
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
		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "routing-config.update",
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// RequestLogsHandler handles GET /admin/v1/logs?limit=N&offset=N
func RequestLogsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"logs": []any{}})
			return
		}
		limit := 100
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := parseIntParam(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := parseIntParam(v); err == nil && n >= 0 {
				offset = n
			}
		}
		logs, err := d.Store.ListRequestLogs(r.Context(), limit, offset)
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"logs": logs})
	}
}

// EngineModelsHandler lists models from the runtime engine (not DB).
func EngineModelsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models := d.Engine.ListModels()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models":   models,
			"adapters": d.Engine.ListAdapterIDs(),
		})
	}
}

func parseIntParam(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// AuditLogsHandler handles GET /admin/v1/audit?limit=N&offset=N
func AuditLogsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"logs": []any{}})
			return
		}
		limit := 100
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := parseIntParam(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := parseIntParam(v); err == nil && n >= 0 {
				offset = n
			}
		}
		logs, err := d.Store.ListAuditLogs(r.Context(), limit, offset)
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"logs": logs})
	}
}

// RewardsHandler handles GET /admin/v1/rewards?limit=N&offset=N
func RewardsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"rewards": []any{}})
			return
		}
		limit := 100
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := parseIntParam(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := parseIntParam(v); err == nil && n >= 0 {
				offset = n
			}
		}
		rewards, err := d.Store.ListRewards(r.Context(), limit, offset)
		if err != nil {
			http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rewards": rewards})
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
