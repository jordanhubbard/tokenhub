package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        any             `json:"stop,omitempty"`
	N           *int            `json:"n,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
}

// completionsResponse is the OpenAI-compatible response for /v1/chat/completions.
type completionsResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices json.RawMessage  `json:"choices"`
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

		// Messages are still required. Model is optional in TokenHub: omitted or
		// "*" delegates model choice to the router, while aliases and exact model
		// names keep their normal behavior.
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

		modelHint := normalizeClientModelHint(d.Engine, req.Model)

		// Look up tool name map for the hinted model. Used to rewrite tool names
		// in both directions: outbound (client→model) uses the reversed map,
		// inbound (model→client) uses the map directly.
		var toolNameMap map[string]string
		if m, ok := d.Engine.GetModel(modelHint); ok {
			toolNameMap = m.ToolNameMap
		}

		// Forward tool definitions, rewriting names from client-facing to model-facing.
		if len(req.Tools) > 0 {
			tools := rewriteToolNames(req.Tools, reverseMap(toolNameMap))
			var toolsVal any
			if err := json.Unmarshal(tools, &toolsVal); err == nil {
				params["tools"] = toolsVal
			}
		}
		if len(req.ToolChoice) > 0 {
			var tcVal any
			if err := json.Unmarshal(req.ToolChoice, &tcVal); err == nil {
				params["tool_choice"] = tcVal
			}
		}

		// Translate to router.Request. The ID is the chi request ID so blind A/B
		// alias rewrites land on the same variant for the lifetime of this HTTP
		// request (including hedging, retries, and fallback paths). The API key
		// ID is stamped into Meta so that api_key-sticky aliases can pin the
		// same credential to one variant across requests.
		routerReq := router.Request{
			ID:        middleware.GetReqID(r.Context()),
			Messages:  req.Messages,
			ModelHint: modelHint,
			Stream:    req.Stream,
		}
		if rec := apikey.FromContext(r.Context()); rec != nil && rec.ID != "" {
			if routerReq.Meta == nil {
				routerReq.Meta = map[string]any{}
			}
			routerReq.Meta[router.MetaAPIKeyID] = rec.ID
		}
		if len(params) > 0 {
			routerReq.Parameters = params
		}

		// Estimate tokens for observability.
		estimatedTokens := 0
		for _, msg := range req.Messages {
			estimatedTokens += len(msg.Content) / 4
		}

		// Determine API key ID and name for attribution.
		apiKeyID, apiKeyName := "", ""
		if rec := apikey.FromContext(r.Context()); rec != nil {
			apiKeyID = rec.ID
			apiKeyName = rec.Name
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
			// Apply model-specific output transformations.
			if m, ok := d.Engine.GetModel(decision.ModelID); ok {
				if m.Gemma4Output {
					body = newSSEGemma4Transformer(body)
				}
				if len(m.ToolNameMap) > 0 {
					body = newSSEToolNameRewriter(body, m.ToolNameMap)
				}
			}
			defer func() { _ = body.Close() }()

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Negotiated-Model", decision.ModelID)
			if decision.AliasFrom != "" {
				w.Header().Set("X-Alias-From", decision.AliasFrom)
			}
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
			httpStatus := http.StatusOK
			if !streamSuccess {
				errClass = "stream_error"
				httpStatus = http.StatusBadGateway
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
				APIKeyName:      apiKeyName,
				EstimatedTokens: estimatedTokens,
				HTTPStatus:      httpStatus,
				AliasFrom:       decision.AliasFrom,
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
				APIKeyName:      apiKeyName,
				EstimatedTokens: estimatedTokens,
				HTTPStatus:      http.StatusBadGateway,
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
			APIKeyName:      apiKeyName,
			EstimatedTokens: estimatedTokens,
			InputTokens:     usage.InputTokens,
			OutputTokens:    usage.OutputTokens,
			HTTPStatus:      http.StatusOK,
			AliasFrom:       decision.AliasFrom,
		})
		if decision.AliasFrom != "" {
			w.Header().Set("X-Alias-From", decision.AliasFrom)
		}
		w.Header().Set("X-Negotiated-Model", decision.ModelID)

		// Build OpenAI-compatible response.
		oaiResp := buildCompletionsResponse(reqID, decision.ModelID, resp)

		// Apply model-specific output transformations.
		if m, ok := d.Engine.GetModel(decision.ModelID); ok {
			if m.Gemma4Output {
				oaiResp.Choices = rewriteGemma4Choices(oaiResp.Choices)
			}
			if len(m.ToolNameMap) > 0 {
				oaiResp.Choices = rewriteChoicesToolCalls(oaiResp.Choices, m.ToolNameMap)
			}
		}

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
			ID            string `json:"id"`
			Object        string `json:"object"`
			OwnedBy       string `json:"owned_by"`
			ContextLength int    `json:"context_length,omitempty"`
			MaxModelLen   int    `json:"max_model_len,omitempty"`
		}
		var data []modelObj
		for _, m := range models {
			if !m.Enabled {
				continue
			}
			data = append(data, modelObj{
				ID:            m.ID,
				Object:        "model",
				OwnedBy:       m.ProviderID,
				ContextLength: m.MaxContextTokens,
				MaxModelLen:   m.MaxContextTokens,
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
// a "choices" array), the choices are passed through after sanitizing any
// Anthropic-native tool call IDs. Otherwise, a single choice is synthesised
// from the raw response content.
func buildCompletionsResponse(requestID, model string, raw json.RawMessage) completionsResponse {
	resp := completionsResponse{
		ID:      fmt.Sprintf("chatcmpl-%s", requestID),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}

	// Try to extract choices and usage from the provider response.
	var parsed struct {
		Choices json.RawMessage  `json:"choices"`
		Usage   *completionUsage `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Choices != nil {
		// Sanitize Anthropic-native tooluse_ IDs in tool_calls to call_ format.
		resp.Choices = sanitizeToolCallIDs(parsed.Choices)
		resp.Usage = parsed.Usage
		return resp
	}

	// Fallback: try Anthropic-style response with content array.
	// Anthropic responses may contain both "text" and "tool_use" content blocks.
	var anthropicResp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(raw, &anthropicResp); err == nil && len(anthropicResp.Content) > 0 {
		text := ""
		var toolCalls []map[string]any
		for i, c := range anthropicResp.Content {
			switch c.Type {
			case "text":
				text += c.Text
			case "tool_use":
				// Convert Anthropic tool_use block to OpenAI tool_calls entry.
				// Anthropic uses "tooluse_"-prefixed IDs; rewrite to "call_" prefix
				// so downstream clients (e.g. litellm) can process them correctly.
				callID := normalizeToolCallID(c.ID, i)
				args := "{}"
				if len(c.Input) > 0 {
					args = string(c.Input)
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]string{
						"name":      c.Name,
						"arguments": args,
					},
				})
			}
		}

		var contentVal any = text
		if text == "" {
			contentVal = nil
		}

		msg := map[string]any{
			"role":    "assistant",
			"content": contentVal,
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}

		finishReason := "stop"
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		}

		choices, _ := json.Marshal([]map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		})
		resp.Choices = choices
		if anthropicResp.Usage != nil {
			resp.Usage = &completionUsage{
				PromptTokens:     anthropicResp.Usage.InputTokens,
				CompletionTokens: anthropicResp.Usage.OutputTokens,
				TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
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

// normalizeToolCallID converts an Anthropic-native tool use ID (prefixed with
// "tooluse_") to an OpenAI-compatible call ID (prefixed with "call_"). If the
// ID already has a "call_" prefix or is empty, a synthetic ID is generated
// using the block index.
func normalizeToolCallID(id string, index int) string {
	if strings.HasPrefix(id, "call_") {
		return id
	}
	if strings.HasPrefix(id, "tooluse_") {
		// Replace "tooluse_" prefix with "call_" to produce an OpenAI-format ID.
		return "call_" + id[len("tooluse_"):]
	}
	if id != "" {
		return "call_" + id
	}
	return fmt.Sprintf("call_%d", index)
}

// sanitizeToolCallIDs rewrites any Anthropic-native "tooluse_"-prefixed IDs in
// an OpenAI choices array to the "call_" prefix that OpenAI clients expect.
// This handles the case where a backend has already converted Anthropic responses
// to OpenAI format but preserved the original Anthropic IDs verbatim.
func sanitizeToolCallIDs(choices json.RawMessage) json.RawMessage {
	type toolFunction struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}
	type toolCall struct {
		ID       string       `json:"id,omitempty"`
		Type     string       `json:"type,omitempty"`
		Function toolFunction `json:"function"`
	}
	type message struct {
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		ToolCalls []toolCall      `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int             `json:"index"`
		Message      message         `json:"message"`
		FinishReason *string         `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs,omitempty"`
	}

	var arr []choice
	if err := json.Unmarshal(choices, &arr); err != nil {
		return choices
	}

	changed := false
	for i, c := range arr {
		for j, tc := range c.Message.ToolCalls {
			normalized := normalizeToolCallID(tc.ID, j)
			if normalized != tc.ID {
				arr[i].Message.ToolCalls[j].ID = normalized
				changed = true
			}
		}
	}

	if !changed {
		return choices
	}
	out, err := json.Marshal(arr)
	if err != nil {
		return choices
	}
	return out
}
