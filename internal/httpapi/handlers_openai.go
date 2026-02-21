package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// CompletionsRequest is the OpenAI-compatible request format for /v1/chat/completions.
type CompletionsRequest struct {
	Model    string           `json:"model"`
	Messages []router.Message `json:"messages"`
	Stream   bool             `json:"stream,omitempty"`

	// Optional parameters forwarded to the provider.
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stop        any      `json:"stop,omitempty"`
	N           *int     `json:"n,omitempty"`
}

// completionsResponse is the OpenAI-compatible response for /v1/chat/completions.
type completionsResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices json.RawMessage `json:"choices"`
	Usage   *completionUsage `json:"usage,omitempty"`
}

// completionUsage mirrors the OpenAI usage object.
type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// openaiError writes an OpenAI-compatible error response:
//
//	{"error": {"message": "...", "type": "...", "code": null}}
type openaiErrorBody struct {
	Error openaiErrorDetail `json:"error"`
}

type openaiErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func writeOpenAIError(w http.ResponseWriter, msg, errType string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(openaiErrorBody{
		Error: openaiErrorDetail{
			Message: msg,
			Type:    errType,
			Code:    nil,
		},
	})
}

// ChatCompletionsHandler returns an http.HandlerFunc for the OpenAI-compatible
// /v1/chat/completions endpoint. It translates between the standard OpenAI
// request/response format and TokenHub's internal routing engine.
func ChatCompletionsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var req CompletionsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, "invalid JSON: "+err.Error(), "invalid_request_error", http.StatusBadRequest)
			return
		}

		// Validate required fields per OpenAI spec.
		if req.Model == "" {
			writeOpenAIError(w, "model is required", "invalid_request_error", http.StatusBadRequest)
			return
		}
		if len(req.Messages) == 0 {
			writeOpenAIError(w, "messages is required", "invalid_request_error", http.StatusBadRequest)
			return
		}

		// Build parameters map from optional fields.
		params := make(map[string]any)
		if req.Temperature != nil {
			params["temperature"] = *req.Temperature
		}
		if req.MaxTokens != nil {
			params["max_tokens"] = *req.MaxTokens
		}
		if req.TopP != nil {
			params["top_p"] = *req.TopP
		}
		if req.Stop != nil {
			params["stop"] = req.Stop
		}
		if req.N != nil {
			params["n"] = *req.N
		}

		// Translate to router.Request.
		routerReq := router.Request{
			Messages:  req.Messages,
			ModelHint: req.Model,
			Stream:    req.Stream,
		}
		if len(params) > 0 {
			routerReq.Parameters = params
		}

		// Estimate tokens for observability.
		estimatedTokens := 0
		for _, msg := range req.Messages {
			estimatedTokens += len(msg.Content) / 4
		}

		// Determine API key ID for attribution.
		apiKeyID := ""
		if rec := apikey.FromContext(r.Context()); rec != nil {
			apiKeyID = rec.ID
		}

		// Use default policy (model hint drives selection).
		var policy router.Policy

		reqID := middleware.GetReqID(r.Context())
		reqCtx := providers.WithRequestID(r.Context(), reqID)

		// Handle streaming requests.
		if req.Stream {
			decision, body, serr := d.Engine.RouteAndStream(reqCtx, routerReq, policy)
			if serr != nil {
				writeOpenAIError(w, serr.Error(), "server_error", http.StatusBadGateway)
				return
			}
			defer func() { _ = body.Close() }()

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Negotiated-Model", decision.ModelID)
			w.WriteHeader(http.StatusOK)

			flusher, _ := w.(http.Flusher)
			buf := make([]byte, 32*1024)
			var totalBytes int64
			streamSuccess := true
			for {
				n, readErr := body.Read(buf)
				if n > 0 {
					totalBytes += int64(n)
					if totalBytes > maxStreamBytes {
						slog.Warn("openai stream: max size exceeded",
							slog.String("request_id", reqID),
							slog.String("model", decision.ModelID),
							slog.Int64("bytes", totalBytes))
						streamSuccess = false
						break
					}
					if _, writeErr := w.Write(buf[:n]); writeErr != nil {
						slog.Warn("openai stream: write error",
							slog.String("request_id", reqID),
							slog.String("error", writeErr.Error()))
						streamSuccess = false
						break
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						slog.Warn("openai stream: read error",
							slog.String("request_id", reqID),
							slog.String("model", decision.ModelID),
							slog.String("error", readErr.Error()))
						streamSuccess = false
					}
					break
				}
			}

			streamLatencyMs := time.Since(start).Milliseconds()
			errClass := ""
			if !streamSuccess {
				errClass = "stream_error"
			}
			recordObservability(d, observeParams{
				Ctx:             r.Context(),
				ModelID:         decision.ModelID,
				ProviderID:      decision.ProviderID,
				Mode:            policy.Mode,
				CostUSD:         decision.EstimatedCostUSD,
				LatencyMs:       streamLatencyMs,
				Success:         streamSuccess,
				ErrorClass:      errClass,
				RequestID:       reqID,
				APIKeyID:        apiKeyID,
				EstimatedTokens: estimatedTokens,
			})
			return
		}

		// Non-streaming path.
		decision, resp, err := d.Engine.RouteAndSend(reqCtx, routerReq, policy)
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			recordObservability(d, observeParams{
				Ctx:             r.Context(),
				Mode:            policy.Mode,
				LatencyMs:       latencyMs,
				Success:         false,
				ErrorClass:      "routing_failure",
				ErrorMsg:        err.Error(),
				RequestID:       reqID,
				APIKeyID:        apiKeyID,
				EstimatedTokens: estimatedTokens,
			})
			writeOpenAIError(w, err.Error(), "server_error", http.StatusBadGateway)
			return
		}

		usage := extractUsage(resp)
		actualCost := computeActualCost(usage, decision.EstimatedCostUSD, d.Engine, decision.ModelID)

		recordObservability(d, observeParams{
			Ctx:             r.Context(),
			ModelID:         decision.ModelID,
			ProviderID:      decision.ProviderID,
			Mode:            policy.Mode,
			CostUSD:         actualCost,
			LatencyMs:       latencyMs,
			Success:         true,
			Reason:          decision.Reason,
			RequestID:       reqID,
			APIKeyID:        apiKeyID,
			EstimatedTokens: estimatedTokens,
			InputTokens:     usage.InputTokens,
			OutputTokens:    usage.OutputTokens,
		})

		// Build OpenAI-compatible response.
		oaiResp := buildCompletionsResponse(reqID, decision.ModelID, resp)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oaiResp)
	}
}

// ModelsListPublicHandler returns an OpenAI-compatible GET /v1/models response
// listing all enabled models visible to the authenticated API key.
func ModelsListPublicHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models := d.Engine.ListModels()
		type modelObj struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		}
		var data []modelObj
		for _, m := range models {
			if !m.Enabled {
				continue
			}
			data = append(data, modelObj{
				ID:      m.ID,
				Object:  "model",
				OwnedBy: m.ProviderID,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
		})
	}
}

// buildCompletionsResponse constructs an OpenAI-compatible response from the
// raw provider response. If the provider already returns OpenAI format (with
// a "choices" array), the choices are passed through. Otherwise, a single
// choice is synthesised from the raw response content.
func buildCompletionsResponse(requestID, model string, raw json.RawMessage) completionsResponse {
	resp := completionsResponse{
		ID:      fmt.Sprintf("chatcmpl-%s", requestID),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}

	// Try to extract choices and usage from the provider response.
	var parsed struct {
		Choices json.RawMessage `json:"choices"`
		Usage   *completionUsage `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Choices != nil {
		resp.Choices = parsed.Choices
		resp.Usage = parsed.Usage
		return resp
	}

	// Fallback: try Anthropic-style response with content array.
	var anthropic struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(raw, &anthropic); err == nil && len(anthropic.Content) > 0 {
		text := ""
		for _, c := range anthropic.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		choices, _ := json.Marshal([]map[string]any{
			{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		})
		resp.Choices = choices
		if anthropic.Usage != nil {
			resp.Usage = &completionUsage{
				PromptTokens:     anthropic.Usage.InputTokens,
				CompletionTokens: anthropic.Usage.OutputTokens,
				TotalTokens:      anthropic.Usage.InputTokens + anthropic.Usage.OutputTokens,
			}
		}
		return resp
	}

	// Last resort: wrap the raw response as a single choice.
	choices, _ := json.Marshal([]map[string]any{
		{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": string(raw)},
			"finish_reason": "stop",
		},
	})
	resp.Choices = choices
	return resp
}
