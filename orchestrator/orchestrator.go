package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/jordanhubbard/tokenhub/providers"
	"github.com/jordanhubbard/tokenhub/router"
)

// Orchestrator handles adversarial orchestration mode
type Orchestrator struct {
	router *router.Router
}

// NewOrchestrator creates a new orchestrator
func NewOrchestrator(router *router.Router) *Orchestrator {
	return &Orchestrator{
		router: router,
	}
}

// AdversarialMode runs adversarial orchestration:
// Model A generates a plan, Model B critiques it
func (o *Orchestrator) AdversarialMode(ctx context.Context, prompt string) (*AdversarialResult, error) {
	log.Printf("Starting adversarial orchestration for prompt: %s", prompt)

	// Phase 1: Model A generates the plan
	planRequest := &providers.ChatRequest{
		Messages: []providers.Message{
			{
				Role:    "system",
				Content: "You are a planning assistant. Generate a detailed plan to address the user's request.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		MaxTokens:   1000,
		Temperature: 0.7,
	}

	log.Println("Phase 1: Generating plan with Model A")
	planResponse, err := o.router.RouteChatRequest(ctx, planRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	plan := planResponse.Message.Content
	log.Printf("Generated plan (%d tokens)", planResponse.TokensUsed)

	// Phase 2: Model B critiques the plan
	critiqueRequest := &providers.ChatRequest{
		Messages: []providers.Message{
			{
				Role:    "system",
				Content: "You are a critical reviewer. Analyze the plan below and provide constructive criticism, identifying potential issues, gaps, or improvements.",
			},
			{
				Role:    "user",
				Content: fmt.Sprintf("Original request: %s\n\nProposed plan:\n%s\n\nProvide your critique:", prompt, plan),
			},
		},
		MaxTokens:   1000,
		Temperature: 0.7,
	}

	log.Println("Phase 2: Critiquing plan with Model B")
	critiqueResponse, err := o.router.RouteChatRequest(ctx, critiqueRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate critique: %w", err)
	}

	critique := critiqueResponse.Message.Content
	log.Printf("Generated critique (%d tokens)", critiqueResponse.TokensUsed)

	// Phase 3: Model A refines the plan based on critique
	refinementRequest := &providers.ChatRequest{
		Messages: []providers.Message{
			{
				Role:    "system",
				Content: "You are a planning assistant. Refine your plan based on the critique provided.",
			},
			{
				Role:    "user",
				Content: fmt.Sprintf("Original request: %s\n\nYour plan:\n%s\n\nCritique:\n%s\n\nProvide a refined plan:", prompt, plan, critique),
			},
		},
		MaxTokens:   1500,
		Temperature: 0.7,
	}

	log.Println("Phase 3: Refining plan with Model A based on critique")
	refinementResponse, err := o.router.RouteChatRequest(ctx, refinementRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to refine plan: %w", err)
	}

	refinedPlan := refinementResponse.Message.Content
	log.Printf("Generated refined plan (%d tokens)", refinementResponse.TokensUsed)

	return &AdversarialResult{
		OriginalPrompt: prompt,
		InitialPlan:    plan,
		Critique:       critique,
		RefinedPlan:    refinedPlan,
		TotalTokens:    planResponse.TokensUsed + critiqueResponse.TokensUsed + refinementResponse.TokensUsed,
	}, nil
}

// AdversarialResult contains the results of adversarial orchestration
type AdversarialResult struct {
	OriginalPrompt string
	InitialPlan    string
	Critique       string
	RefinedPlan    string
	TotalTokens    int
}
