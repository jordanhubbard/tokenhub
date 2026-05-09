package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// anthropicError writes an Anthropic-compatible error response:
//
//	{"type":"error","error":{"type":"...","message":"..."}}
func writeAnthropicError(w http.ResponseWriter, msg, errType string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": msg,
		},
	})
}

// AnthropicMessagesHandler handles POST /v1/messages in Anthropic API format.
// It applies the acc-agent proxy transformations (header stripping, OpenAI
// tool_call normalization, orphaned tool_result injection) then forwards raw
// bytes to the configured Anthropic-capable provider.
func AnthropicMessagesHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := middleware.GetReqID(r.Context())

		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
		if err != nil {
			writeAnthropicError(w, "failed to read request body", "invalid_request_error", http.StatusBadRequest)
			return
		}

		// Peek at the model and stream flag.
		var peek struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if jerr := json.Unmarshal(body, &peek); jerr != nil {
			writeAnthropicError(w, "invalid JSON: "+jerr.Error(), "invalid_request_error", http.StatusBadRequest)
			return
		}

		modelHint := normalizeClientModelHint(d.Engine, peek.Model)
		aliasReq := router.Request{
			ID:        reqID,
			ModelHint: modelHint,
		}
		if rec := apikey.FromContext(r.Context()); rec != nil && rec.ID != "" {
			aliasReq.Meta = map[string]any{router.MetaAPIKeyID: rec.ID}
		}
		aliasFrom := d.Engine.ResolveModelHint(&aliasReq)
		modelHint = aliasReq.ModelHint

		// Apply the same sanitizations as acc-agent proxy.rs:
		//   1. normalize OpenAI-format tool_calls → Anthropic tool_use content blocks
		//   2. inject synthetic tool_result messages for orphaned tool_use IDs
		body = sanitizeAnthropicBody(body)

		sender, resolvedModel := d.Engine.GetAnthropicSenderAndModel(modelHint)
		if sender == nil {
			writeAnthropicError(w, "no Anthropic-compatible provider configured", "server_error", http.StatusServiceUnavailable)
			return
		}
		// Rewrite model in body when the registry resolved a different upstream ID
		// (e.g. "claude-sonnet-4-6" → "azure/anthropic/claude-sonnet-4-6").
		if resolvedModel != "" && resolvedModel != peek.Model {
			body = rewriteModel(body, resolvedModel)
		}

		reqCtx := providers.WithRequestID(r.Context(), reqID)

		if peek.Stream {
			stream, serr := sender.ForwardRawStream(reqCtx, body)
			if serr != nil {
				slog.Warn("anthropic passthrough: stream error",
					slog.String("request_id", reqID),
					slog.String("model", peek.Model),
					slog.String("error", serr.Error()),
				)
				writeAnthropicError(w, serr.Error(), "server_error", http.StatusBadGateway)
				return
			}
			defer func() { _ = stream.Close() }()

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Negotiated-Model", resolvedModel)
			if aliasFrom != "" {
				w.Header().Set("X-Alias-From", aliasFrom)
			}
			w.WriteHeader(http.StatusOK)

			flusher, _ := w.(http.Flusher)
			buf := make([]byte, 32*1024)
			for {
				n, readErr := stream.Read(buf)
				if n > 0 {
					if _, writeErr := w.Write(buf[:n]); writeErr != nil {
						break
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
				if readErr != nil {
					break
				}
			}
			slog.Debug("anthropic passthrough: stream done",
				slog.String("request_id", reqID),
				slog.String("model", peek.Model),
				slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			)
			return
		}

		// Non-streaming path.
		respBody, statusCode, ferr := sender.ForwardRaw(reqCtx, body)
		latencyMs := time.Since(start).Milliseconds()

		if ferr != nil {
			slog.Warn("anthropic passthrough: upstream error",
				slog.String("request_id", reqID),
				slog.String("model", peek.Model),
				slog.String("error", ferr.Error()),
				slog.Int64("latency_ms", latencyMs),
			)
			writeAnthropicError(w, ferr.Error(), "server_error", http.StatusBadGateway)
			return
		}

		slog.Debug("anthropic passthrough: ok",
			slog.String("request_id", reqID),
			slog.String("model", peek.Model),
			slog.Int("status", statusCode),
			slog.Int64("latency_ms", latencyMs),
		)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Negotiated-Model", resolvedModel)
		if aliasFrom != "" {
			w.Header().Set("X-Alias-From", aliasFrom)
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)
	}
}

// sanitizeAnthropicBody applies two fixes to the request body before forwarding:
//  1. Converts OpenAI-format assistant tool_calls → Anthropic tool_use content blocks.
//  2. Injects synthetic tool_result user messages for orphaned tool_use IDs
//     (prevents Bedrock HTTP 400 errors).
func sanitizeAnthropicBody(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	msgs, ok := payload["messages"].([]any)
	if !ok {
		return body
	}

	normalized := normalizeOpenAIToolCalls(msgs)
	msgs, injected := injectMissingToolResults(msgs)

	if normalized == 0 && injected == 0 {
		return body
	}

	payload["messages"] = msgs
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	if normalized > 0 {
		slog.Debug("anthropic passthrough: normalized OpenAI tool_calls", slog.Int("count", normalized))
	}
	if injected > 0 {
		slog.Debug("anthropic passthrough: injected synthetic tool_results", slog.Int("count", injected))
	}
	return out
}

// normalizeOpenAIToolCalls converts assistant messages that carry OpenAI-format
// tool_calls arrays into Anthropic-native content arrays with tool_use blocks.
func normalizeOpenAIToolCalls(msgs []any) int {
	converted := 0
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] != "assistant" {
			continue
		}
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok || len(toolCalls) == 0 {
			continue
		}

		var blocks []any

		// Preserve any existing text content.
		if text, ok := msg["content"].(string); ok && text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}

		for _, tc := range toolCalls {
			call, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			id, _ := call["id"].(string)
			fn, _ := call["function"].(map[string]any)
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)
			var input any
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
				input = map[string]any{}
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}

		delete(msg, "tool_calls")
		msg["content"] = blocks
		msgs[i] = msg
		converted++
	}
	return converted
}

// injectMissingToolResults walks msgs and inserts synthetic tool_result user
// messages for any assistant tool_use blocks that have no matching tool_result
// in the immediately following message. Returns the (possibly grown) slice and
// the count of tool_use IDs that were fixed.
func injectMissingToolResults(msgs []any) ([]any, int) {
	fixed := 0
	i := 0
	for i < len(msgs) {
		tuIDs := collectToolUseIDs(msgs[i])
		if len(tuIDs) == 0 {
			i++
			continue
		}

		covered := map[string]bool{}
		if i+1 < len(msgs) {
			for _, id := range collectToolResultIDs(msgs[i+1]) {
				covered[id] = true
			}
		}

		var missing []string
		for _, id := range tuIDs {
			if !covered[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			i++
			continue
		}

		var blocks []any
		for _, id := range missing {
			blocks = append(blocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     "[result unavailable — session was interrupted before this tool completed]",
				"is_error":    true,
			})
		}
		synthetic := map[string]any{"role": "user", "content": blocks}

		// Insert after current message, growing the slice.
		msgs = append(msgs, nil)
		copy(msgs[i+2:], msgs[i+1:])
		msgs[i+1] = synthetic

		fixed += len(missing)
		i += 2
	}
	return msgs, fixed
}

func collectToolUseIDs(m any) []string {
	msg, ok := m.(map[string]any)
	if !ok || msg["role"] != "assistant" {
		return nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_use" {
			if id, ok := block["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func collectToolResultIDs(m any) []string {
	msg, ok := m.(map[string]any)
	if !ok || msg["role"] != "user" {
		return nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_result" {
			if id, ok := block["tool_use_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// rewriteModel replaces the "model" field in a JSON body with newModel.
// Returns the original body unchanged if it cannot be parsed.
func rewriteModel(body []byte, newModel string) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = newModel
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
