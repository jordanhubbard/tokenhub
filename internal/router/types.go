package router

import "encoding/json"

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

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Policy struct {
	Mode         string
	MaxBudgetUSD float64
	MaxLatencyMs int
	MinWeight    int
	OutputSchema string
}

type Decision struct {
	ModelID           string
	ProviderID        string
	EstimatedCostUSD  float64
	Reason            string
}

type Model struct {
	ID              string  `json:"id"`
	ProviderID       string  `json:"provider_id"`
	Weight           int     `json:"weight"`
	MaxContextTokens int     `json:"max_context_tokens"`
	InputPer1K       float64 `json:"input_per_1k"`
	OutputPer1K      float64 `json:"output_per_1k"`
	Enabled          bool    `json:"enabled"`
}

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
	Schema     string `json:"schema,omitempty"`       // JSON schema to enforce (for type=json)
	MaxTokens  int    `json:"max_tokens,omitempty"`   // Truncate response beyond this
	StripThink bool   `json:"strip_think,omitempty"`  // Remove <think>...</think> blocks
}

type ProviderResponse = json.RawMessage
