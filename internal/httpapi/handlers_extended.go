package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// ProviderDiscoverHandler probes a registered provider's /v1/models endpoint
// and returns the list of available models. Works with any OpenAI-compatible
// API (OpenAI, Anthropic via proxy, vLLM, NVIDIA NIM, Ollama, etc.).
func ProviderDiscoverHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := chi.URLParam(r, "id")
		if providerID == "" {
			jsonError(w, "provider id required", http.StatusBadRequest)
			return
		}

		// Look up provider base URL from the store.
		if d.Store == nil {
			jsonError(w, "no store configured", http.StatusInternalServerError)
			return
		}
		providers, err := d.Store.ListProviders(r.Context())
		if err != nil {
			jsonError(w, "store error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var baseURL string
		for _, p := range providers {
			if p.ID == providerID {
				baseURL = p.BaseURL
				break
			}
		}
		if baseURL == "" {
			jsonError(w, "provider not found or has no base URL", http.StatusNotFound)
			return
		}

		// Probe the provider's /v1/models endpoint.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		modelsURL := baseURL + "/v1/models"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
		if err != nil {
			jsonError(w, "failed to build request: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Try to get the API key from the vault for authenticated requests.
		if d.Vault != nil && !d.Vault.IsLocked() {
			if key, err := d.Vault.Get("provider:" + providerID + ":api_key"); err == nil && key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			jsonError(w, "failed to reach provider: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			jsonError(w, "failed to read response: "+err.Error(), http.StatusBadGateway)
			return
		}

		if resp.StatusCode != http.StatusOK {
			jsonError(w, "provider returned "+resp.Status, resp.StatusCode)
			return
		}

		// Parse the OpenAI-format model list response.
		type modelEntry struct {
			ID      string `json:"id"`
			Object  string `json:"object,omitempty"`
			OwnedBy string `json:"owned_by,omitempty"`
			Created int64  `json:"created,omitempty"`
		}
		type modelsResponse struct {
			Data   []modelEntry `json:"data"`
			Object string       `json:"object,omitempty"`
		}

		var parsed modelsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			// Some providers return a plain array instead of {data: [...]}.
			var arr []modelEntry
			if err2 := json.Unmarshal(body, &arr); err2 != nil {
				jsonError(w, "failed to parse models response", http.StatusBadGateway)
				return
			}
			parsed.Data = arr
		}

		// Return the discovered models along with which are already registered.
		registered := make(map[string]bool)
		for _, m := range d.Engine.ListModels() {
			if m.ProviderID == providerID {
				registered[m.ID] = true
			}
		}

		type discoveredModel struct {
			ID         string `json:"id"`
			OwnedBy    string `json:"owned_by,omitempty"`
			Registered bool   `json:"registered"`
		}

		result := make([]discoveredModel, 0, len(parsed.Data))
		for _, m := range parsed.Data {
			result = append(result, discoveredModel{
				ID:         m.ID,
				OwnedBy:    m.OwnedBy,
				Registered: registered[m.ID],
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id": providerID,
			"models":      result,
			"total":       len(result),
		})
	}
}

// RoutingSimulateHandler runs model selection without sending a request.
// Returns the ranked list of eligible models and the top pick for the given
// routing parameters. Used by the "what-if" simulator in the admin UI.
func RoutingSimulateHandler(d Dependencies) http.HandlerFunc {
	type simulateRequest struct {
		Mode         string  `json:"mode"`
		MaxBudgetUSD float64 `json:"max_budget_usd"`
		MaxLatencyMs int     `json:"max_latency_ms"`
		MinWeight    int     `json:"min_weight"`
		TokenCount   int     `json:"token_count"`
		ModelHint    string  `json:"model_hint,omitempty"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req simulateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		if req.TokenCount <= 0 {
			req.TokenCount = 500
		}

		routerReq := router.Request{
			EstimatedInputTokens: req.TokenCount,
			ModelHint:            req.ModelHint,
		}
		policy := router.Policy{
			Mode:         req.Mode,
			MaxBudgetUSD: req.MaxBudgetUSD,
			MaxLatencyMs: req.MaxLatencyMs,
			MinWeight:    req.MinWeight,
		}

		decision, eligible, err := d.Engine.SelectModel(r.Context(), routerReq, policy)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":    err.Error(),
				"eligible": []any{},
			})
			return
		}

		type eligibleModel struct {
			ID               string  `json:"id"`
			ProviderID       string  `json:"provider_id"`
			Weight           int     `json:"weight"`
			MaxContextTokens int     `json:"max_context_tokens"`
			InputPer1K       float64 `json:"input_per_1k"`
			OutputPer1K      float64 `json:"output_per_1k"`
			Enabled          bool    `json:"enabled"`
			Selected         bool    `json:"selected"`
		}

		models := make([]eligibleModel, 0, len(eligible))
		for _, m := range eligible {
			models = append(models, eligibleModel{
				ID:               m.ID,
				ProviderID:       m.ProviderID,
				Weight:           m.Weight,
				MaxContextTokens: m.MaxContextTokens,
				InputPer1K:       m.InputPer1K,
				OutputPer1K:      m.OutputPer1K,
				Enabled:          m.Enabled,
				Selected:         m.ID == decision.ModelID,
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"decision": map[string]any{
				"model_id":           decision.ModelID,
				"provider_id":        decision.ProviderID,
				"estimated_cost_usd": decision.EstimatedCostUSD,
				"reason":             decision.Reason,
			},
			"eligible": models,
			"total":    len(models),
		})
	}
}
