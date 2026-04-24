package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

// aliasPayload is the JSON body shape for PUT /admin/v1/aliases/{name}.
type aliasPayload struct {
	Variants []router.AliasVariant `json:"variants"`
	Enabled  *bool                 `json:"enabled"`
	// StickyBy: "request" (default) or "api_key". See router.Alias.
	StickyBy string `json:"sticky_by,omitempty"`
}

// recordToRouterAlias converts the persisted record form into the runtime
// resolver form. Defined here (rather than in the router package) so the
// router doesn't take a build-time dependency on store.
func recordToRouterAlias(rec store.ModelAliasRecord) router.Alias {
	variants := make([]router.AliasVariant, 0, len(rec.Variants))
	for _, v := range rec.Variants {
		variants = append(variants, router.AliasVariant{
			ModelID: v.ModelID,
			Weight:  v.Weight,
		})
	}
	return router.Alias{
		Name:     rec.Name,
		Variants: variants,
		Enabled:  rec.Enabled,
		StickyBy: rec.StickyBy,
	}
}

func routerAliasToRecord(a router.Alias) store.ModelAliasRecord {
	variants := make([]store.AliasVariantStore, 0, len(a.Variants))
	for _, v := range a.Variants {
		variants = append(variants, store.AliasVariantStore{
			ModelID: v.ModelID,
			Weight:  v.Weight,
		})
	}
	return store.ModelAliasRecord{
		Name:     a.Name,
		Variants: variants,
		Enabled:  a.Enabled,
		StickyBy: a.StickyBy,
	}
}

// AliasesListHandler handles GET /admin/v1/aliases.
// Returns all registered aliases; read straight from the store so operators
// see the persisted truth even if the resolver hasn't been rehydrated after
// a crash / restart edge case.
func AliasesListHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "total": 0})
			return
		}
		recs, err := d.Store.ListModelAliases(r.Context())
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if recs == nil {
			recs = []store.ModelAliasRecord{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": recs,
			"total": len(recs),
		})
	}
}

// AliasGetHandler handles GET /admin/v1/aliases/{name}.
func AliasGetHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(chi.URLParam(r, "name"))
		if name == "" {
			jsonError(w, "alias name required", http.StatusBadRequest)
			return
		}
		if d.Store == nil {
			jsonError(w, "store not configured", http.StatusServiceUnavailable)
			return
		}
		rec, err := d.Store.GetModelAlias(r.Context(), name)
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if rec == nil {
			jsonError(w, "alias not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(rec)
	}
}

// AliasUpsertHandler handles PUT /admin/v1/aliases/{name}.
// Body: {"variants":[{"model_id":"claude-sonnet-4-6","weight":50},...], "enabled":true}
//
// The request body defaults Enabled to true when the field is omitted so that
// creating a new experiment is a single call; to pause traffic without
// deleting the alias, PUT with "enabled": false.
func AliasUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(chi.URLParam(r, "name"))
		if name == "" {
			jsonError(w, "alias name required", http.StatusBadRequest)
			return
		}
		var body aliasPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		alias := router.Alias{
			Name:     name,
			Variants: body.Variants,
			Enabled:  enabled,
			StickyBy: body.StickyBy,
		}
		if err := alias.Validate(); err != nil {
			jsonError(w, "invalid alias: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Persist first so that a crash between resolver update and store write
		// cannot leave the live resolver ahead of what operators see on disk.
		if d.Store != nil {
			if err := d.Store.UpsertModelAlias(r.Context(), routerAliasToRecord(alias)); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if res := d.Engine.AliasResolver(); res != nil {
			_ = res.Set(alias) // already validated above
		}

		if d.Store != nil {
			detail, _ := json.Marshal(alias)
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "alias.upsert",
				Resource:  name,
				Detail:    string(detail),
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(alias)
	}
}

// AliasDeleteHandler handles DELETE /admin/v1/aliases/{name}.
func AliasDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(chi.URLParam(r, "name"))
		if name == "" {
			jsonError(w, "alias name required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.DeleteModelAlias(r.Context(), name); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if res := d.Engine.AliasResolver(); res != nil {
			res.Delete(name)
		}
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "alias.delete",
				Resource:  name,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}
