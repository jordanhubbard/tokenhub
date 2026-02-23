package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/providers/anthropic"
	"github.com/jordanhubbard/tokenhub/internal/providers/openai"
	"github.com/jordanhubbard/tokenhub/internal/providers/vllm"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

// registerProviderAdapter constructs and registers a runtime adapter for the
// given provider record. It resolves the API key from the vault when the
// credential store is "vault". This is called on every upsert/patch so that
// changes take effect immediately without a restart and so that repeated
// registrations from bootstrap scripts are a no-op.
func registerProviderAdapter(d Dependencies, p store.ProviderRecord, apiKeyOverride string) {
	if p.BaseURL == "" {
		return
	}
	base := normalizeBaseURL(p.BaseURL)
	timeout := d.ProviderTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	keyFunc := makeKeyFunc(d, p, apiKeyOverride)

	var adapter router.Sender
	switch p.Type {
	case "anthropic":
		adapter = anthropic.New(p.ID, "", base, anthropic.WithTimeout(timeout), anthropic.WithKeyFunc(keyFunc))
	case "vllm":
		vllmOpts := []vllm.Option{vllm.WithTimeout(timeout), vllm.WithKeyFunc(keyFunc)}
		adapter = vllm.New(p.ID, base, vllmOpts...)
	case "openai", "":
		adapter = openai.New(p.ID, "", base, openai.WithTimeout(timeout), openai.WithKeyFunc(keyFunc))
	default:
		slog.Warn("unknown provider type, adapter not registered", slog.String("provider", p.ID), slog.String("type", p.Type))
		return
	}
	d.Engine.RegisterAdapter(adapter)
	slog.Info("registered provider adapter", slog.String("provider", p.ID), slog.String("type", p.Type), slog.String("base_url", base))
}

// normalizeBaseURL strips trailing slashes and common path suffixes like "/v1"
// so the adapter can unconditionally append "/v1/..." without duplication.
func normalizeBaseURL(raw string) string {
	u := strings.TrimRight(raw, "/")
	for _, suffix := range []string{"/v1", "/v2"} {
		u = strings.TrimSuffix(u, suffix)
	}
	return strings.TrimRight(u, "/")
}

// checkBaseURLReachable attempts to resolve the hostname in baseURL and returns
// an error with a clear message if DNS lookup fails. This catches misconfigured
// or unreachable providers at registration time rather than silently storing
// them and discovering the problem later in health probe logs.
func checkBaseURLReachable(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("base_url has no hostname: %q", baseURL)
	}
	// Skip DNS check for loopback/IP addresses — those don't need lookup.
	if net.ParseIP(host) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("cannot resolve hostname %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("hostname %q resolved to no addresses", host)
	}
	return nil
}

// makeKeyFunc returns a closure that resolves the API key on every call.
// This ensures vault rotations, lock/unlock, and re-stored keys take effect
// immediately without re-registering the adapter.
func makeKeyFunc(d Dependencies, p store.ProviderRecord, apiKeyOverride string) func() string {
	if apiKeyOverride != "" {
		return func() string { return apiKeyOverride }
	}
	if p.CredStore == "vault" && d.Vault != nil {
		vaultKey := "provider:" + p.ID + ":api_key"
		return func() string {
			if d.Vault.IsLocked() {
				return ""
			}
			key, _ := d.Vault.Get(vaultKey)
			return key
		}
	}
	return func() string { return "" }
}

// addProbeTargetIfProbeable checks if the adapter just registered for providerID
// implements health.Probeable and, if so, adds it to the dynamic health prober.
func addProbeTargetIfProbeable(d Dependencies, providerID string) {
	if d.Prober == nil {
		return
	}
	adapter := d.Engine.GetAdapter(providerID)
	if adapter == nil {
		return
	}
	if p, ok := adapter.(health.Probeable); ok {
		d.Prober.AddTarget(p)
	}
}

// AdminSessionHandler creates a short-lived session cookie so that the SSE
// EventSource (which cannot set request headers) can authenticate without
// embedding the admin token in the URL query string.
// The caller must already be authenticated via adminAuthMiddleware (Bearer token).
func AdminSessionHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The middleware already validated the caller's token, so we can
		// safely issue a session cookie using the current admin token.
		http.SetCookie(w, &http.Cookie{
			Name:     "th_admin_session",
			Value:    d.AdminToken.Get(),
			Path:     "/admin",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			// Secure is not forced here because tokenhub can run over plain HTTP
			// in dev environments; production deployments should use HTTPS via a
			// reverse proxy which will upgrade the cookie to Secure automatically.
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// AdminTokenRotateHandler rotates the admin token. If a "token" field is
// provided in the request body, it replaces the current token with that
// value; otherwise a new random token is generated. The new token is
// persisted to the data directory and returned in the response.
//
// POST /admin/v1/admin-token/rotate
func AdminTokenRotateHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AdminToken == nil {
			jsonError(w, "admin token management not available", http.StatusServiceUnavailable)
			return
		}

		var req struct {
			Token string `json:"token"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		var newToken string
		var err error
		if req.Token != "" {
			if len(req.Token) < 16 {
				jsonError(w, "token must be at least 16 characters", http.StatusBadRequest)
				return
			}
			d.AdminToken.Replace(req.Token, slog.Default())
			newToken = req.Token
		} else {
			newToken, err = d.AdminToken.Rotate(slog.Default())
			if err != nil {
				jsonError(w, "rotate failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		if d.EventBus != nil {
			d.EventBus.Publish(events.Event{
				Type:   events.EventHealthChange,
				Reason: "admin token rotated",
			})
		}
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "admin_token.rotate",
				Resource:  "admin",
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    true,
			"token": newToken,
		})
	}
}

func VaultLockHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Vault.IsLocked() {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "already_locked": true})
			return
		}
		d.Vault.Lock()
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := d.Vault.Unlock([]byte(req.AdminPassword)); err != nil {
			jsonError(w, "unlock failed", http.StatusUnauthorized)
			return
		}
		// Persist vault salt and encrypted data to the store.
		if d.Store != nil {
			salt := d.Vault.Salt()
			data := d.Vault.Export()
			if salt != nil {
				d.warnOnErr("save_vault", d.Store.SaveVaultBlob(r.Context(), salt, data))
			}
		}
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.OldPassword == "" || req.NewPassword == "" {
			jsonError(w, "old_password and new_password required", http.StatusBadRequest)
			return
		}

		if err := d.Vault.RotatePassword([]byte(req.OldPassword), []byte(req.NewPassword)); err != nil {
			// Distinguish validation errors from internal errors.
			switch {
			case errors.Is(err, vault.ErrVaultLocked),
				errors.Is(err, vault.ErrVaultNotEnabled),
				errors.Is(err, vault.ErrNewPasswordTooShort):
				jsonError(w, err.Error(), http.StatusBadRequest)
			default:
				jsonError(w, "rotation failed: "+err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// Persist the new vault blob to the store.
		if d.Store != nil {
			salt := d.Vault.Salt()
			data := d.Vault.Export()
			if salt != nil {
				if err := d.Store.SaveVaultBlob(r.Context(), salt, data); err != nil {
					jsonError(w, "failed to persist vault: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}

		// Log audit entry.
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
		req.Enabled = true
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			jsonError(w, "provider id required", http.StatusBadRequest)
			return
		}
		req.BaseURL = normalizeBaseURL(req.BaseURL)

		// Fail fast if the container cannot resolve the provider hostname.
		if req.BaseURL != "" {
			if err := checkBaseURLReachable(req.BaseURL); err != nil {
				jsonError(w, "base_url not reachable: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		// Store API key in vault if provided.
		if req.APIKey != "" && d.Vault != nil && !d.Vault.IsLocked() {
			if err := d.Vault.Set("provider:"+req.ID+":api_key", req.APIKey); err != nil {
				jsonError(w, "vault error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			req.CredStore = "vault"
			// Persist vault data.
			if d.Store != nil {
				salt := d.Vault.Salt()
				data := d.Vault.Export()
				if salt != nil {
					d.warnOnErr("save_vault", d.Store.SaveVaultBlob(r.Context(), salt, data))
				}
			}
		}

		if d.Store != nil {
			if err := d.Store.UpsertProvider(r.Context(), req.ProviderRecord); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		registerProviderAdapter(d, req.ProviderRecord, req.APIKey)
		addProbeTargetIfProbeable(d, req.ID)
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if providers == nil {
			providers = []store.ProviderRecord{}
		}
		total := len(providers)
		limit, offset := parsePagination(r)
		providers = paginateSlice(providers, offset, limit)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":  providers,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

func ProvidersDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			jsonError(w, "provider id required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.DeleteProvider(r.Context(), id); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		d.Engine.UnregisterAdapter(id)
		if d.Prober != nil {
			d.Prober.RemoveTarget(id)
		}
		if d.EventBus != nil {
			d.EventBus.Publish(events.Event{
				Type:       events.EventHealthChange,
				ProviderID: id,
				OldState:   "unknown",
				NewState:   "removed",
				Reason:     "provider deleted",
			})
		}
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		// Validate required fields.
		if m.ID == "" {
			jsonError(w, "model id required", http.StatusBadRequest)
			return
		}
		if m.ProviderID == "" {
			jsonError(w, "provider_id required", http.StatusBadRequest)
			return
		}
		if m.Weight < 0 || m.Weight > 10 {
			jsonError(w, "weight must be between 0 and 10", http.StatusBadRequest)
			return
		}
		if m.InputPer1K < 0 {
			jsonError(w, "input_per_1k must be >= 0", http.StatusBadRequest)
			return
		}
		if m.OutputPer1K < 0 {
			jsonError(w, "output_per_1k must be >= 0", http.StatusBadRequest)
			return
		}

		// Register in the runtime engine.
		d.Engine.RegisterModel(m)
		// Persist to store.
		if d.Store != nil {
			rec := store.ModelRecord{
				ID:               m.ID,
				ProviderID:       m.ProviderID,
				Weight:           m.Weight,
				MaxContextTokens: m.MaxContextTokens,
				InputPer1K:       m.InputPer1K,
				OutputPer1K:      m.OutputPer1K,
				Enabled:          m.Enabled,
				PricingSource:    m.PricingSource,
			}
			if rec.PricingSource == "" {
				rec.PricingSource = "manual"
			}
			d.warnOnErr("upsert_model", d.Store.UpsertModel(r.Context(), rec))
		}
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if models == nil {
			models = []store.ModelRecord{}
		}
		total := len(models)
		limit, offset := parsePagination(r)
		models = paginateSlice(models, offset, limit)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":  models,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// wildcardID extracts and URL-decodes the Chi wildcard parameter, stripping
// the leading slash that Chi includes with "*" routes.
func wildcardID(r *http.Request) string {
	id := chi.URLParam(r, "*")
	return strings.TrimPrefix(id, "/")
}

func ModelsDeleteHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := wildcardID(r)
		if id == "" {
			jsonError(w, "model id required", http.StatusBadRequest)
			return
		}
		if d.Store != nil {
			if err := d.Store.DeleteModel(r.Context(), id); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		d.Engine.UnregisterModel(id)
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "model.delete",
				Resource:  id,
				RequestID: middleware.GetReqID(r.Context()),
			}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// ModelsPatchHandler handles PATCH /admin/v1/models/* for partial updates (e.g. weight).
func ModelsPatchHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := wildcardID(r)
		if id == "" {
			jsonError(w, "model id required", http.StatusBadRequest)
			return
		}

		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		// Load the existing model from store.
		if d.Store == nil {
			jsonError(w, "no store configured", http.StatusInternalServerError)
			return
		}
		existing, err := d.Store.GetModel(r.Context(), id)
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if existing == nil {
			// Try to find in engine (runtime-only model) and seed a store record.
			for _, m := range d.Engine.ListModels() {
				if m.ID == id {
					existing = &store.ModelRecord{
						ID: m.ID, ProviderID: m.ProviderID, Weight: m.Weight,
						MaxContextTokens: m.MaxContextTokens, InputPer1K: m.InputPer1K,
						OutputPer1K: m.OutputPer1K, Enabled: m.Enabled,
						PricingSource: m.PricingSource,
					}
					break
				}
			}
			if existing == nil {
				jsonError(w, "model not found", http.StatusNotFound)
				return
			}
		}

		// Validate patch values before applying.
		if v, ok := patch["weight"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 || f > 10 {
					jsonError(w, "weight must be between 0 and 10", http.StatusBadRequest)
					return
				}
			}
		}
		if v, ok := patch["input_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 {
					jsonError(w, "input_per_1k must be >= 0", http.StatusBadRequest)
					return
				}
			}
		}
		if v, ok := patch["output_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				if f < 0 {
					jsonError(w, "output_per_1k must be >= 0", http.StatusBadRequest)
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
		pricingPatched := false
		if v, ok := patch["input_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				existing.InputPer1K = f
				pricingPatched = true
			}
		}
		if v, ok := patch["output_per_1k"]; ok {
			if f, ok := v.(float64); ok {
				existing.OutputPer1K = f
				pricingPatched = true
			}
		}
		if v, ok := patch["max_context_tokens"]; ok {
			if f, ok := v.(float64); ok {
				existing.MaxContextTokens = int(f)
			}
		}
		// When the user explicitly sets pricing, mark source as "manual" so
		// the auto-refresh won't silently overwrite their values.
		if pricingPatched {
			existing.PricingSource = "manual"
		}

		// Update store and engine.
		if err := d.Store.UpsertModel(r.Context(), *existing); err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
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
			PricingSource:    existing.PricingSource,
		})
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
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
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		// Validate routing config fields.
		switch cfg.DefaultMode {
		case "", "cheap", "normal", "high_confidence", "planning", "adversarial", "thompson":
			// valid
		default:
			jsonError(w, "unknown default_mode", http.StatusBadRequest)
			return
		}
		if cfg.DefaultMaxBudgetUSD < 0 || cfg.DefaultMaxBudgetUSD > 100 {
			jsonError(w, "default_max_budget_usd must be between 0 and 100", http.StatusBadRequest)
			return
		}
		if cfg.DefaultMaxLatencyMs < 0 || cfg.DefaultMaxLatencyMs > 300000 {
			jsonError(w, "default_max_latency_ms must be between 0 and 300000", http.StatusBadRequest)
			return
		}

		if d.Store != nil {
			if err := d.Store.SaveRoutingConfig(r.Context(), cfg); err != nil {
				jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Apply to engine.
		d.Engine.UpdateDefaults(cfg.DefaultMode, cfg.DefaultMaxBudgetUSD, cfg.DefaultMaxLatencyMs)
		if d.Store != nil {
			d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"logs": logs})
	}
}

// EngineModelsHandler lists models from the runtime engine (not DB).
func EngineModelsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models := d.Engine.ListModels()
		total := len(models)
		limit, offset := parsePagination(r)
		models = paginateSlice(models, offset, limit)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models":       models,
			"total":        total,
			"limit":        limit,
			"offset":       offset,
			"adapters":     d.Engine.ListAdapterIDs(),
			"adapter_info": d.Engine.ListAdapterInfo(),
		})
	}
}

// ProvidersPatchHandler handles PATCH /admin/v1/providers/{id} for partial updates.
func ProvidersPatchHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			jsonError(w, "provider id required", http.StatusBadRequest)
			return
		}
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		if d.Store == nil {
			jsonError(w, "no store configured", http.StatusInternalServerError)
			return
		}
		providers, err := d.Store.ListProviders(r.Context())
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var existing *store.ProviderRecord
		for i := range providers {
			if providers[i].ID == id {
				existing = &providers[i]
				break
			}
		}
		if existing == nil {
			// Provider not in store; create from patch for runtime providers being edited.
			existing = &store.ProviderRecord{ID: id, Enabled: true}
		}
		if v, ok := patch["type"]; ok {
			if s, ok := v.(string); ok {
				existing.Type = s
			}
		}
		if v, ok := patch["base_url"]; ok {
			if s, ok := v.(string); ok {
				normalized := normalizeBaseURL(s)
				if normalized != "" {
					if err := checkBaseURLReachable(normalized); err != nil {
						jsonError(w, "base_url not reachable: "+err.Error(), http.StatusBadRequest)
						return
					}
				}
				existing.BaseURL = normalized
			}
		}
		if v, ok := patch["enabled"]; ok {
			if b, ok := v.(bool); ok {
				existing.Enabled = b
			}
		}
		if v, ok := patch["cred_store"]; ok {
			if s, ok := v.(string); ok {
				existing.CredStore = s
			}
		}
		// Handle API key update.
		if v, ok := patch["api_key"]; ok {
			if s, ok := v.(string); ok && s != "" && d.Vault != nil && !d.Vault.IsLocked() {
				if err := d.Vault.Set("provider:"+id+":api_key", s); err != nil {
					jsonError(w, "vault error: "+err.Error(), http.StatusInternalServerError)
					return
				}
				existing.CredStore = "vault"
				if d.Store != nil {
					salt := d.Vault.Salt()
					data := d.Vault.Export()
					if salt != nil {
						d.warnOnErr("save_vault", d.Store.SaveVaultBlob(r.Context(), salt, data))
					}
				}
			}
		}
		if err := d.Store.UpsertProvider(r.Context(), *existing); err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Extract the explicit API key from the patch (if any) for adapter registration.
		var apiKeyOverride string
		if v, ok := patch["api_key"]; ok {
			if s, ok := v.(string); ok {
				apiKeyOverride = s
			}
		}
		registerProviderAdapter(d, *existing, apiKeyOverride)
		addProbeTargetIfProbeable(d, id)
		d.warnOnErr("audit", d.Store.LogAudit(r.Context(), store.AuditEntry{
			Timestamp: time.Now().UTC(),
			Action:    "provider.patch",
			Resource:  id,
			RequestID: middleware.GetReqID(r.Context()),
		}))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "provider": existing})
	}
}

func parseIntParam(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// parsePagination extracts limit and offset from query parameters.
// Default limit=1000, maximum limit=1000. Use explicit pagination for larger sets.
func parsePagination(r *http.Request) (limit, offset int) {
	limit = 1000
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseIntParam(v); err == nil && n > 0 {
			limit = n
			if limit > 1000 {
				limit = 1000
			}
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := parseIntParam(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// paginateSlice applies offset and limit to a slice.
func paginateSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
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
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
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
