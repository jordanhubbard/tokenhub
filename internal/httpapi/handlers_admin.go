package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func ProvidersUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Placeholder. Intended to store provider config in DB.
		_ = json.NewEncoder(w).Encode(map[string]any{"todo": "providers upsert"})
	}
}

func ModelsUpsertHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m router.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		d.Engine.RegisterModel(m)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}
