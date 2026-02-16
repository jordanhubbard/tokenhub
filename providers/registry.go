package providers

import (
	"context"
	"fmt"
)

// Provider defines the interface for LLM providers
type Provider interface {
	Name() string
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

// CompletionRequest represents a completion request
type CompletionRequest struct {
	Model       string
	Prompt      string
	MaxTokens   int
	Temperature float64
}

// CompletionResponse represents a completion response
type CompletionResponse struct {
	Text         string
	TokensUsed   int
	FinishReason string
}

// ChatRequest represents a chat request
type ChatRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

// Message represents a chat message
type Message struct {
	Role    string // "system", "user", or "assistant"
	Content string
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Message      Message
	TokensUsed   int
	FinishReason string
}

// Registry manages LLM providers
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a new provider registry
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry
func (r *Registry) Register(provider Provider) {
	r.providers[provider.Name()] = provider
}

// Get retrieves a provider by name
func (r *Registry) Get(name string) (Provider, error) {
	provider, exists := r.providers[name]
	if !exists {
		return nil, fmt.Errorf("provider not found: %s", name)
	}
	return provider, nil
}

// List returns all registered provider names
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
