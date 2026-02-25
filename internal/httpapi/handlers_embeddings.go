package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// EmbeddingsHandler implements POST /v1/embeddings (OpenAI-compatible).
// It resolves the target provider from the model field, looks up the base URL
// and API key, and proxies the request to the provider's /v1/embeddings path.
func EmbeddingsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
		if err != nil {
			jsonError(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var reqBody struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &reqBody); err != nil || reqBody.Model == "" {
			jsonError(w, "model is required", http.StatusBadRequest)
			return
		}

		// Find the provider for this model.
		var providerID string
		for _, m := range d.Engine.ListModels() {
			if m.ID == reqBody.Model {
				providerID = m.ProviderID
				break
			}
		}
		if providerID == "" {
			jsonError(w, "model not found: "+reqBody.Model, http.StatusNotFound)
			return
		}

		// Look up the provider's base URL and credentials.
		providers, err := d.Store.ListProviders(r.Context())
		if err != nil {
			jsonError(w, "failed to look up provider", http.StatusInternalServerError)
			return
		}
		var baseURL, apiKey string
		for _, p := range providers {
			if p.ID == providerID {
				baseURL = p.BaseURL
				if p.CredStore == "vault" && d.Vault != nil && !d.Vault.IsLocked() {
					apiKey, _ = d.Vault.Get("provider:" + p.ID + ":api_key")
				}
				break
			}
		}
		if baseURL == "" {
			jsonError(w, "provider has no base URL configured", http.StatusServiceUnavailable)
			return
		}

		// Proxy to the provider's /v1/embeddings endpoint.
		target := strings.TrimRight(baseURL, "/") + "/v1/embeddings"
		proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			jsonError(w, "failed to build proxy request", http.StatusInternalServerError)
			return
		}
		proxyReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			proxyReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: d.ProviderTimeout}
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
		start := time.Now()
		resp, err := client.Do(proxyReq)
		if err != nil {
			slog.Warn("embeddings: provider request failed",
				slog.String("provider", providerID),
				slog.String("model", reqBody.Model),
				slog.String("error", err.Error()))
			jsonError(w, "provider error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		latencyMs := time.Since(start).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)

		success := resp.StatusCode >= 200 && resp.StatusCode < 300
		recordObservability(d, observeParams{
			Ctx:        r.Context(),
			ModelID:    reqBody.Model,
			ProviderID: providerID,
			Mode:       "embeddings",
			LatencyMs:  latencyMs,
			Success:    success,
			HTTPStatus: resp.StatusCode,
		})
	}
}
