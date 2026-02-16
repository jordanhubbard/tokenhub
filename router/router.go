package router

import (
	"context"
	"fmt"
	"log"

	"github.com/jordanhubbard/tokenhub/models"
	"github.com/jordanhubbard/tokenhub/providers"
)

// Router handles request routing between providers
type Router struct {
	providerRegistry *providers.Registry
	modelRegistry    *models.Registry
}

// NewRouter creates a new router
func NewRouter(providerRegistry *providers.Registry, modelRegistry *models.Registry) *Router {
	return &Router{
		providerRegistry: providerRegistry,
		modelRegistry:    modelRegistry,
	}
}

// RouteChatRequest routes a chat request to the appropriate provider with fallback
func (r *Router) RouteChatRequest(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	// Estimate tokens needed
	tokensNeeded := r.estimateTokens(req)

	// Select best model
	model := r.modelRegistry.SelectBestModel(tokensNeeded, 0)
	if model == nil {
		return nil, fmt.Errorf("no suitable model found for required context size: %d", tokensNeeded)
	}

	req.Model = model.Name
	log.Printf("Routing request to provider: %s, model: %s", model.Provider, model.Name)

	// Try primary provider
	provider, err := r.providerRegistry.Get(model.Provider)
	if err != nil {
		return nil, fmt.Errorf("provider not found: %s", model.Provider)
	}

	resp, err := provider.Chat(ctx, req)
	if err != nil {
		log.Printf("Primary provider failed: %v, attempting escalation", err)
		return r.escalate(ctx, req, model, err)
	}

	// Check for context overflow
	if resp.FinishReason == "length" || resp.FinishReason == "context_length_exceeded" {
		log.Printf("Context overflow detected, escalating to larger model")
		return r.escalateContextOverflow(ctx, req, model)
	}

	return resp, nil
}

// RouteCompletionRequest routes a completion request to the appropriate provider
func (r *Router) RouteCompletionRequest(ctx context.Context, req *providers.CompletionRequest) (*providers.CompletionResponse, error) {
	tokensNeeded := len(req.Prompt)/4 + req.MaxTokens

	model := r.modelRegistry.SelectBestModel(tokensNeeded, 0)
	if model == nil {
		return nil, fmt.Errorf("no suitable model found for required context size: %d", tokensNeeded)
	}

	req.Model = model.Name
	log.Printf("Routing request to provider: %s, model: %s", model.Provider, model.Name)

	provider, err := r.providerRegistry.Get(model.Provider)
	if err != nil {
		return nil, fmt.Errorf("provider not found: %s", model.Provider)
	}

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		log.Printf("Primary provider failed: %v, attempting escalation", err)
		return r.escalateCompletion(ctx, req, model, err)
	}

	if resp.FinishReason == "length" || resp.FinishReason == "context_length_exceeded" {
		log.Printf("Context overflow detected, escalating to larger model")
		return r.escalateCompletionContextOverflow(ctx, req, model)
	}

	return resp, nil
}

// escalate attempts to use an alternative provider on failure
func (r *Router) escalate(ctx context.Context, req *providers.ChatRequest, failedModel *models.Model, originalErr error) (*providers.ChatResponse, error) {
	// Find alternative providers
	allModels := r.modelRegistry.List()
	for _, model := range allModels {
		if model.ID == failedModel.ID {
			continue
		}
		if model.ContextSize < r.estimateTokens(req) {
			continue
		}

		log.Printf("Escalating to provider: %s, model: %s", model.Provider, model.Name)
		provider, err := r.providerRegistry.Get(model.Provider)
		if err != nil {
			continue
		}

		req.Model = model.Name
		resp, err := provider.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		log.Printf("Escalation to %s failed: %v", model.Name, err)
	}

	return nil, fmt.Errorf("all providers failed, original error: %w", originalErr)
}

// escalateContextOverflow escalates to a model with larger context
func (r *Router) escalateContextOverflow(ctx context.Context, req *providers.ChatRequest, currentModel *models.Model) (*providers.ChatResponse, error) {
	tokensNeeded := r.estimateTokens(req) * 2

	allModels := r.modelRegistry.List()
	var largerModel *models.Model
	for _, model := range allModels {
		if model.ContextSize > currentModel.ContextSize && model.ContextSize >= tokensNeeded {
			if largerModel == nil || model.ContextSize < largerModel.ContextSize {
				largerModel = model
			}
		}
	}

	if largerModel == nil {
		return nil, fmt.Errorf("no model with sufficient context size available (need %d)", tokensNeeded)
	}

	log.Printf("Escalating to larger model: %s (context: %d)", largerModel.Name, largerModel.ContextSize)
	provider, err := r.providerRegistry.Get(largerModel.Provider)
	if err != nil {
		return nil, err
	}

	req.Model = largerModel.Name
	return provider.Chat(ctx, req)
}

// escalateCompletion handles completion request escalation
func (r *Router) escalateCompletion(ctx context.Context, req *providers.CompletionRequest, failedModel *models.Model, originalErr error) (*providers.CompletionResponse, error) {
	tokensNeeded := len(req.Prompt)/4 + req.MaxTokens
	allModels := r.modelRegistry.List()
	
	for _, model := range allModels {
		if model.ID == failedModel.ID {
			continue
		}
		if model.ContextSize < tokensNeeded {
			continue
		}

		log.Printf("Escalating to provider: %s, model: %s", model.Provider, model.Name)
		provider, err := r.providerRegistry.Get(model.Provider)
		if err != nil {
			continue
		}

		req.Model = model.Name
		resp, err := provider.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		log.Printf("Escalation to %s failed: %v", model.Name, err)
	}

	return nil, fmt.Errorf("all providers failed, original error: %w", originalErr)
}

// escalateCompletionContextOverflow handles completion context overflow
func (r *Router) escalateCompletionContextOverflow(ctx context.Context, req *providers.CompletionRequest, currentModel *models.Model) (*providers.CompletionResponse, error) {
	tokensNeeded := (len(req.Prompt)/4 + req.MaxTokens) * 2

	allModels := r.modelRegistry.List()
	var largerModel *models.Model
	for _, model := range allModels {
		if model.ContextSize > currentModel.ContextSize && model.ContextSize >= tokensNeeded {
			if largerModel == nil || model.ContextSize < largerModel.ContextSize {
				largerModel = model
			}
		}
	}

	if largerModel == nil {
		return nil, fmt.Errorf("no model with sufficient context size available (need %d)", tokensNeeded)
	}

	log.Printf("Escalating to larger model: %s (context: %d)", largerModel.Name, largerModel.ContextSize)
	provider, err := r.providerRegistry.Get(largerModel.Provider)
	if err != nil {
		return nil, err
	}

	req.Model = largerModel.Name
	return provider.Complete(ctx, req)
}

// estimateTokens estimates the number of tokens in a chat request
func (r *Router) estimateTokens(req *providers.ChatRequest) int {
	total := 0
	for _, msg := range req.Messages {
		total += len(msg.Content) / 4
	}
	return total + req.MaxTokens
}
