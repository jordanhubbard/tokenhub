package router

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// AnthropicRawSender is an optional interface for provider adapters that can
// forward a raw Anthropic-format /v1/messages request body without translating
// through router.Request. Used by the Anthropic passthrough proxy endpoint.
type AnthropicRawSender interface {
	Sender
	ForwardRaw(ctx context.Context, body []byte) ([]byte, int, error)
	// ForwardRawStream forwards a raw Anthropic-format request body and returns
	// the upstream response body for streaming (SSE) consumption. The caller
	// is responsible for closing the returned ReadCloser.
	ForwardRawStream(ctx context.Context, body []byte) (io.ReadCloser, error)
}

// EmbeddingsSender is an optional interface for OpenAI-compatible providers
// that can proxy raw /v1/embeddings requests using their registered runtime
// credentials.
type EmbeddingsSender interface {
	Sender
	SendEmbeddings(ctx context.Context, body []byte) ([]byte, int, error)
}

// Request is a provider-agnostic envelope. Provider adapters translate this
// into provider-specific API calls.
type Request struct {
	ID string `json:"id,omitempty"`

	// Chat-style messages (OpenAI-ish envelope). Provider adapters can map.
	Messages []Message `json:"messages"`

	// Optional model hint from client; router may ignore.
	ModelHint string `json:"model_hint,omitempty"`

	// Optional: known/estimated token count from client.
	EstimatedInputTokens int `json:"estimated_input_tokens,omitempty"`

	// Arbitrary metadata for policy & tracing; NOT forwarded to providers.
	Meta map[string]any `json:"meta,omitempty"`

	// Optional JSON Schema that the orchestration output should conform to.
	// Used for structured output from LLMs.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// Parameters forwarded to the provider (temperature, max_tokens, top_p, etc.)
	// These are merged directly into the provider request payload.
	Parameters map[string]any `json:"parameters,omitempty"`

	// Stream requests SSE streaming from the provider.
	Stream bool `json:"stream,omitempty"`
}

// Message represents a single chat message with a role and content.
// Content is normalized to a plain string on unmarshal: if the incoming JSON
// sends content as an array of parts (OpenAI multi-modal format), the text
// parts are concatenated so downstream providers that only accept string
// content (e.g. vLLM with Nemotron) never see an unexpected type.
//
// ToolCalls/ToolCallID/Name carry the OpenAI tool-calling shape verbatim so
// downstream providers receive the assistant.tool_calls and tool.tool_call_id
// fields they require. Without these, upstream providers (Azure, litellm) raise
// "tool_call_id" schema errors because the linkage between an assistant tool
// call and its tool-result message is lost when forwarding.
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler so that Message.Content can accept
// either a plain JSON string or an OpenAI-style content-parts array:
//
//	[{"type":"text","text":"hello"}, {"type":"text","text":" world"}]
//
// Non-text parts (image_url, etc.) are silently skipped; their textual
// representation is not meaningful for text-only backends.
func (m *Message) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion.
	type alias struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	m.Role = a.Role
	m.ToolCalls = a.ToolCalls
	m.ToolCallID = a.ToolCallID
	m.Name = a.Name

	if len(a.Content) == 0 {
		return nil
	}

	// Fast path: plain string.
	if a.Content[0] == '"' {
		return json.Unmarshal(a.Content, &m.Content)
	}

	// Array path: OpenAI multi-part content.
	if a.Content[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(a.Content, &parts); err != nil {
			return err
		}
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		m.Content = sb.String()
		return nil
	}

	// Null or unexpected type — leave content empty.
	return nil
}

// Policy specifies routing constraints such as mode, budget, latency, and quality.
type Policy struct {
	Mode         string
	MaxBudgetUSD float64
	MaxLatencyMs int
	MinWeight    int
	OutputSchema string
	// EstimatedOutputTokens is the caller's estimate of how many output tokens the
	// request will produce. Used for cost estimation and budget enforcement.
	// Defaults to 512 when zero.
	EstimatedOutputTokens int
}

// Decision captures the routing outcome: which model and provider were selected.
type Decision struct {
	ModelID          string
	ProviderID       string
	EstimatedCostUSD float64
	Reason           string
	// AliasFrom is the original client-supplied alias name when a blind
	// A/B alias rewrote ModelHint before routing. Empty otherwise. Recorded
	// in request logs so experiments can group results by the alias that
	// fanned them out.
	AliasFrom string
}

// Model describes a registered LLM with its provider, pricing, and capabilities.
type Model struct {
	ID               string  `json:"id"`
	ProviderID       string  `json:"provider_id"`
	Weight           int     `json:"weight"`
	MaxContextTokens int     `json:"max_context_tokens"`
	InputPer1K       float64 `json:"input_per_1k"`
	OutputPer1K      float64 `json:"output_per_1k"`
	Enabled          bool    `json:"enabled"`
	PricingSource    string  `json:"pricing_source,omitempty"`
	// ToolNameMap maps model-facing tool names to client-facing tool names.
	// Applied to tool_calls in responses (model→client) and inverted for
	// tool definitions in requests (client→model).
	ToolNameMap map[string]string `json:"tool_name_map,omitempty"`
	// Gemma4Output enables parsing of Gemma 4's non-standard response tokens:
	// <|channel>thought\n...<channel|> thinking blocks are stripped, and
	// <|tool_call>call:name{...}<tool_call|> inline tool calls are converted
	// to the standard OpenAI tool_calls format.
	Gemma4Output bool `json:"gemma4_output,omitempty"`
}

// OrchestrationDirective configures multi-model orchestration (adversarial, vote, refine).
type OrchestrationDirective struct {
	Mode string `json:"mode"` // planning|adversarial|vote|refine

	PrimaryMinWeight int `json:"primary_min_weight,omitempty"`
	ReviewMinWeight  int `json:"review_min_weight,omitempty"`
	Iterations       int `json:"iterations,omitempty"`

	// Optional explicit model IDs
	PrimaryModelID string `json:"primary_model_id,omitempty"`
	ReviewModelID  string `json:"review_model_id,omitempty"`

	// Output shaping (non-forwarded)
	ReturnPlanOnly bool   `json:"return_plan_only,omitempty"`
	OutputSchema   string `json:"output_schema,omitempty"`
}

// OutputFormat specifies how the response should be shaped before returning to the client.
type OutputFormat struct {
	Type       string `json:"type,omitempty"`        // json, markdown, text, xml
	Schema     string `json:"schema,omitempty"`      // JSON schema to enforce (for type=json)
	MaxTokens  int    `json:"max_tokens,omitempty"`  // Truncate response beyond this
	StripThink bool   `json:"strip_think,omitempty"` // Remove <think>...</think> blocks
}

type ProviderResponse = json.RawMessage

// WildcardModelHint lets callers explicitly opt into server-side model
// selection. It behaves like an omitted model hint unless an alias named "*"
// is configured, in which case the alias controls the resolved target.
const WildcardModelHint = "*"

var defaultWildcardRoundRobinModels = []string{
	"deepseek-v4-flash",
	"claude-opus-4-7",
	"gpt-5.5",
}

// DefaultWildcardRoundRobinModelIDs returns the built-in diversity pool used
// when a caller sends model="*" and operators have not configured an explicit
// "*" alias. Only registered, enabled models with live adapters participate.
func DefaultWildcardRoundRobinModelIDs() []string {
	return append([]string(nil), defaultWildcardRoundRobinModels...)
}

// IsWildcardModelHint reports whether hint is the wildcard model selector.
func IsWildcardModelHint(hint string) bool {
	return strings.TrimSpace(hint) == WildcardModelHint
}
