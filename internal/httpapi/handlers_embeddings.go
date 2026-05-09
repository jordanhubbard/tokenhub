package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/router"
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
		if err := json.Unmarshal(body, &reqBody); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		modelHint := normalizeClientModelHint(d.Engine, reqBody.Model)
		if strings.TrimSpace(modelHint) == "" {
			modelHint = router.WildcardModelHint
		}
		routeReq := router.Request{
			ID:        middleware.GetReqID(r.Context()),
			ModelHint: modelHint,
		}
		if rec := apikey.FromContext(r.Context()); rec != nil && rec.ID != "" {
			routeReq.Meta = map[string]any{router.MetaAPIKeyID: rec.ID}
		}

		aliasFrom := d.Engine.ResolveModelHint(&routeReq)
		modelID := routeReq.ModelHint
		if modelID == "" {
			decision, _, err := d.Engine.SelectModel(r.Context(), routeReq, router.Policy{})
			if err != nil {
				jsonError(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			modelID = decision.ModelID
			aliasFrom = decision.AliasFrom
		}
		if idx := strings.IndexByte(modelID, '/'); idx > 0 {
			bare := modelID[idx+1:]
			if d.Engine.HasModel(bare) {
				modelID = bare
			}
		}

		model, ok := d.Engine.GetModel(modelID)
		if !ok || !model.Enabled {
			jsonError(w, "model not found: "+reqBody.Model, http.StatusNotFound)
			return
		}
		adapter := d.Engine.GetAdapter(model.ProviderID)
		embeddings, ok := adapter.(router.EmbeddingsSender)
		if !ok {
			jsonError(w, "provider does not support embeddings", http.StatusServiceUnavailable)
			return
		}
		if modelID != reqBody.Model {
			body = rewriteModel(body, modelID)
		}

		start := time.Now()
		respBody, statusCode, err := embeddings.SendEmbeddings(r.Context(), body)
		if err != nil {
			slog.Warn("embeddings: provider request failed",
				slog.String("provider", model.ProviderID),
				slog.String("model", modelID),
				slog.String("error", err.Error()))
			jsonError(w, "provider error: "+err.Error(), http.StatusBadGateway)
			return
		}

		latencyMs := time.Since(start).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Negotiated-Model", modelID)
		if aliasFrom != "" {
			w.Header().Set("X-Alias-From", aliasFrom)
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)

		success := statusCode >= 200 && statusCode < 300
		recordObservability(d, observeParams{
			Ctx:        r.Context(),
			ModelID:    modelID,
			ProviderID: model.ProviderID,
			Mode:       "embeddings",
			LatencyMs:  latencyMs,
			Success:    success,
			HTTPStatus: statusCode,
		})
	}
}
