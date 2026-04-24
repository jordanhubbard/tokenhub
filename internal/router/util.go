package router

import (
	"encoding/json"
	"math"
)

// EstimateTokens estimates the token count for a request (chars/4 heuristic).
// If EstimatedInputTokens is set on the request, that value is returned directly.
func EstimateTokens(req Request) int {
	if req.EstimatedInputTokens > 0 {
		return req.EstimatedInputTokens
	}
	total := 0
	for _, msg := range req.Messages {
		total += len(msg.Content) / 4
	}
	return total
}

// MessagesContent concatenates all user message content into a single string.
func MessagesContent(msgs []Message) string {
	var s string
	for _, m := range msgs {
		if m.Role == "user" {
			if s != "" {
				s += "\n"
			}
			s += m.Content
		}
	}
	return s
}

// ExtractContent tries to pull the text content from a provider response JSON.
// It supports OpenAI, vLLM reasoning-model, and Anthropic response formats,
// falling back to raw string.
//
// For vLLM reasoning models (e.g. Nemotron), the response includes both
// reasoning_content and content fields. Both are returned concatenated so
// that orchestration chains (adversarial, vote, refine) operate on the full
// model output. Callers that want to separate reasoning from output should
// use ExtractContentParts instead.
func ExtractContent(resp ProviderResponse) string {
	content, _ := ExtractContentParts(resp)
	return content
}

// ExtractContentParts returns (fullContent, reasoningContent) from a provider
// response. fullContent is always non-empty when the response is valid;
// reasoningContent is only set for vLLM reasoning models that emit
// reasoning_content alongside content.
//
// For non-reasoning responses, reasoningContent is empty and fullContent is
// the assistant message content as usual.
func ExtractContentParts(resp ProviderResponse) (fullContent, reasoningContent string) {
	// Try vLLM / OpenAI format with optional reasoning_content field.
	var oai struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(resp, &oai) == nil && len(oai.Choices) > 0 {
		content := oai.Choices[0].Message.Content
		reasoning := oai.Choices[0].Message.ReasoningContent
		if reasoning != "" {
			// Return reasoning prepended to content so orchestration chains see
			// the full chain-of-thought; callers can split on the separator if needed.
			return reasoning + "\n\n" + content, reasoning
		}
		return content, ""
	}
	// Try Anthropic format.
	var ant struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(resp, &ant) == nil && len(ant.Content) > 0 {
		return ant.Content[0].Text, ""
	}
	return string(resp), ""
}

func estimateCostUSD(inTokens, outTokens int, inPer1k, outPer1k float64) float64 {
	return (float64(inTokens)/1000.0)*inPer1k + (float64(outTokens)/1000.0)*outPer1k
}

// estOutTokens returns the output-token estimate from the policy, defaulting to 512.
func estOutTokens(p Policy) int {
	if p.EstimatedOutputTokens > 0 {
		return p.EstimatedOutputTokens
	}
	return 512
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
