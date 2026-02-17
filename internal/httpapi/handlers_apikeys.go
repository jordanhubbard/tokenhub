package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

// APIKeysCreateHandler handles POST /admin/v1/apikeys — creates a new API key.
func APIKeysCreateHandler(d Dependencies) http.HandlerFunc {
	type createReq struct {
		Name             string  `json:"name"`
		Scopes           string  `json:"scopes"`             // JSON array, e.g. '["chat","plan"]'
		RotationDays     int     `json:"rotation_days"`
		ExpiresIn        *string `json:"expires_in"`          // duration string, e.g. "720h"
		MonthlyBudgetUSD float64 `json:"monthly_budget_usd"` // 0 = unlimited
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if d.APIKeyMgr == nil {
			jsonError(w, "api key management not configured", http.StatusServiceUnavailable)
			return
		}

		var req createReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		if req.Scopes == "" {
			req.Scopes = `["chat","plan"]`
		}

		var expiresAt *time.Time
		if req.ExpiresIn != nil && *req.ExpiresIn != "" {
			dur, err := time.ParseDuration(*req.ExpiresIn)
			if err != nil {
				jsonError(w, "invalid expires_in duration", http.StatusBadRequest)
				return
			}
			t := time.Now().UTC().Add(dur)
			expiresAt = &t
		}

		plaintext, rec, err := d.APIKeyMgr.Generate(r.Context(), req.Name, req.Scopes, req.RotationDays, expiresAt)
		if err != nil {
			jsonError(w, "failed to create key: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Set monthly budget if specified.
		if req.MonthlyBudgetUSD > 0 {
			rec.MonthlyBudgetUSD = req.MonthlyBudgetUSD
			if err := d.Store.UpdateAPIKey(r.Context(), *rec); err != nil {
				jsonError(w, "failed to set budget: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "apikey.create",
				Resource:  rec.ID,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"key":     plaintext,
			"id":      rec.ID,
			"prefix":  rec.KeyPrefix,
			"name":    rec.Name,
			"scopes":  rec.Scopes,
			"warning": "This is the only time the full key will be shown. Store it securely.",
		})
	}
}

// APIKeysListHandler handles GET /admin/v1/apikeys — lists all API keys (no plaintext).
func APIKeysListHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.APIKeyMgr == nil {
			jsonError(w, "api key management not configured", http.StatusServiceUnavailable)
			return
		}

		if d.Store == nil {
			jsonError(w, "store not configured", http.StatusServiceUnavailable)
			return
		}

		keys, err := d.Store.ListAPIKeys(r.Context())
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// KeyHash is already excluded via json:"-" tag.
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}
}

// APIKeysRotateHandler handles POST /admin/v1/apikeys/{id}/rotate — rotates a key.
func APIKeysRotateHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.APIKeyMgr == nil {
			jsonError(w, "api key management not configured", http.StatusServiceUnavailable)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			jsonError(w, "key id required", http.StatusBadRequest)
			return
		}

		plaintext, err := d.APIKeyMgr.Rotate(r.Context(), id)
		if err != nil {
			jsonError(w, "rotate failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "apikey.rotate",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"key":     plaintext,
			"warning": "This is the only time the new key will be shown. Store it securely.",
		})
	}
}

// APIKeysPatchHandler handles PATCH /admin/v1/apikeys/{id} — update name/scopes/enabled.
func APIKeysPatchHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.APIKeyMgr == nil {
			jsonError(w, "api key management not configured", http.StatusServiceUnavailable)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			jsonError(w, "key id required", http.StatusBadRequest)
			return
		}

		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		rec, err := d.Store.GetAPIKey(r.Context(), id)
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if rec == nil {
			jsonError(w, "api key not found", http.StatusNotFound)
			return
		}

		if v, ok := patch["name"]; ok {
			s, ok := v.(string)
			if !ok || s == "" {
				jsonError(w, "name must be a non-empty string", http.StatusBadRequest)
				return
			}
			rec.Name = s
		}
		if v, ok := patch["scopes"]; ok {
			s, ok := v.(string)
			if !ok {
				jsonError(w, "scopes must be a JSON array string", http.StatusBadRequest)
				return
			}
			var arr []string
			if err := json.Unmarshal([]byte(s), &arr); err != nil {
				jsonError(w, "scopes must be a valid JSON array", http.StatusBadRequest)
				return
			}
			rec.Scopes = s
		}
		if v, ok := patch["enabled"]; ok {
			b, ok := v.(bool)
			if !ok {
				jsonError(w, "enabled must be a boolean", http.StatusBadRequest)
				return
			}
			rec.Enabled = b
		}
		if v, ok := patch["rotation_days"]; ok {
			f, ok := v.(float64)
			if !ok || f < 0 {
				jsonError(w, "rotation_days must be a non-negative number", http.StatusBadRequest)
				return
			}
			rec.RotationDays = int(f)
		}
		if v, ok := patch["monthly_budget_usd"]; ok {
			f, ok := v.(float64)
			if !ok || f < 0 {
				jsonError(w, "monthly_budget_usd must be a non-negative number", http.StatusBadRequest)
				return
			}
			rec.MonthlyBudgetUSD = f
		}

		if err := d.Store.UpdateAPIKey(r.Context(), *rec); err != nil {
			jsonError(w, "update failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "apikey.update",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// APIKeysDeleteHandler handles DELETE /admin/v1/apikeys/{id} — revoke a key.
func APIKeysDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.APIKeyMgr == nil {
			jsonError(w, "api key management not configured", http.StatusServiceUnavailable)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			jsonError(w, "key id required", http.StatusBadRequest)
			return
		}

		if err := d.Store.DeleteAPIKey(r.Context(), id); err != nil {
			jsonError(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if d.Store != nil {
			warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "apikey.revoke",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// Ensure apikey import is used (for CheckScope and Manager types).
var _ = apikey.CheckScope
