package temporal

import (
	"encoding/json"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// ChatInput is the input for the ChatWorkflow.
type ChatInput struct {
	RequestID string         `json:"request_id"`
	APIKeyID  string         `json:"api_key_id"`
	Request   router.Request `json:"request"`
	Policy    router.Policy  `json:"policy"`
}

// ChatOutput is the output of the ChatWorkflow.
type ChatOutput struct {
	Decision  router.Decision `json:"decision"`
	Response  json.RawMessage `json:"response"`
	LatencyMs int64           `json:"latency_ms"`
	Error     string          `json:"error,omitempty"`
}

// OrchestrationInput is the input for the OrchestrationWorkflow.
type OrchestrationInput struct {
	RequestID string                        `json:"request_id"`
	APIKeyID  string                        `json:"api_key_id"`
	Request   router.Request                `json:"request"`
	Directive router.OrchestrationDirective `json:"directive"`
}

// SendInput is the input for the SendToProvider activity.
type SendInput struct {
	ProviderID string         `json:"provider_id"`
	ModelID    string         `json:"model_id"`
	Request    router.Request `json:"request"`
}

// SendOutput is the output of the SendToProvider activity.
type SendOutput struct {
	Response      json.RawMessage `json:"response"`
	LatencyMs     int64           `json:"latency_ms"`
	EstimatedCost float64         `json:"estimated_cost"`
	ErrorClass    string          `json:"error_class,omitempty"`
	InputTokens   int             `json:"input_tokens,omitempty"`
	OutputTokens  int             `json:"output_tokens,omitempty"`
}

// LogInput is the input for the LogResult activity.
type LogInput struct {
	RequestID    string  `json:"request_id"`
	ModelID      string  `json:"model_id"`
	ProviderID   string  `json:"provider_id"`
	Mode         string  `json:"mode"`
	LatencyMs    int64   `json:"latency_ms"`
	CostUSD      float64 `json:"cost_usd"`
	Success      bool    `json:"success"`
	ErrorClass   string  `json:"error_class,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

// StreamLogInput is the input for the StreamLogResult activity.
// It extends LogInput with streaming-specific metrics.
type StreamLogInput struct {
	LogInput
	BytesStreamed int64 `json:"bytes_streamed"`
}

// EscalateInput is the input for the ClassifyAndEscalate activity.
type EscalateInput struct {
	ErrorMsg       string `json:"error_msg"`
	CurrentModelID string `json:"current_model_id"`
	TokensNeeded   int    `json:"tokens_needed"`
}

// EscalateOutput is the output of the ClassifyAndEscalate activity.
type EscalateOutput struct {
	NextModelID string `json:"next_model_id,omitempty"`
	ShouldRetry bool   `json:"should_retry"`
}
