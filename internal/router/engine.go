package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
)

// Sender is the interface that provider adapters must implement for the engine.
// Defined here to avoid an import cycle with the providers package.
type Sender interface {
	ID() string
	Send(ctx context.Context, model string, req Request) (ProviderResponse, error)
	ClassifyError(err error) *ClassifiedError
}

// ErrorClass classifies provider errors for routing decisions.
type ErrorClass string

const (
	ErrContextOverflow ErrorClass = "context_overflow"
	ErrRateLimited     ErrorClass = "rate_limited"
	ErrTransient       ErrorClass = "transient"
	ErrFatal           ErrorClass = "fatal"
)

// ClassifiedError wraps an error with routing classification.
type ClassifiedError struct {
	Err        error
	Class      ErrorClass
	RetryAfter int
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

type EngineConfig struct {
	DefaultMode         string
	DefaultMaxBudgetUSD float64
	DefaultMaxLatencyMs int
	MaxRetries          int
}

type Engine struct {
	cfg EngineConfig

	mu       sync.RWMutex
	models   map[string]Model
	adapters map[string]Sender // provider_id -> adapter
}

func NewEngine(cfg EngineConfig) *Engine {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	return &Engine{
		cfg:      cfg,
		models:   make(map[string]Model),
		adapters: make(map[string]Sender),
	}
}

// RegisterAdapter registers a provider adapter.
func (e *Engine) RegisterAdapter(a Sender) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adapters[a.ID()] = a
}

func (e *Engine) RegisterModel(m Model) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models[m.ID] = m
}

// estimateTokens estimates the token count for a request (chars/4 heuristic).
func estimateTokens(req Request) int {
	if req.EstimatedInputTokens > 0 {
		return req.EstimatedInputTokens
	}
	total := 0
	for _, msg := range req.Messages {
		total += len(msg.Content) / 4
	}
	return total
}

// eligibleModels returns models sorted by weight (descending) that meet the given constraints.
func (e *Engine) eligibleModels(tokensNeeded int, p Policy) []Model {
	var eligible []Model
	for _, m := range e.models {
		if !m.Enabled {
			continue
		}
		if p.MinWeight > 0 && m.Weight < p.MinWeight {
			continue
		}
		if m.MaxContextTokens > 0 && tokensNeeded > 0 && tokensNeeded > m.MaxContextTokens {
			continue
		}
		if _, ok := e.adapters[m.ProviderID]; !ok {
			continue // skip models without a registered adapter
		}
		if p.MaxBudgetUSD > 0 {
			est := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)
			if est > p.MaxBudgetUSD {
				continue
			}
		}
		eligible = append(eligible, m)
	}
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].Weight > eligible[j].Weight
	})
	return eligible
}

func (e *Engine) RouteAndSend(ctx context.Context, req Request, p Policy) (Decision, ProviderResponse, error) {
	if p.Mode == "" {
		p.Mode = e.cfg.DefaultMode
	}
	if p.MaxBudgetUSD == 0 {
		p.MaxBudgetUSD = e.cfg.DefaultMaxBudgetUSD
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	tokensNeeded := estimateTokens(req)
	eligible := e.eligibleModels(tokensNeeded, p)

	if len(eligible) == 0 {
		return Decision{}, nil, errors.New("no eligible models registered")
	}

	// Try each eligible model in weight order, with escalation on failure.
	for i, m := range eligible {
		adapter := e.adapters[m.ProviderID]
		estCost := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)

		slog.Info("routing request",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
			slog.Int("attempt", i+1),
			slog.Int("total", len(eligible)),
		)

		resp, err := adapter.Send(ctx, m.ID, req)
		if err == nil {
			return Decision{
				ModelID:          m.ID,
				ProviderID:       m.ProviderID,
				EstimatedCostUSD: estCost,
				Reason:           fmt.Sprintf("routed-weight-%d", m.Weight),
			}, resp, nil
		}

		// Classify the error and decide whether to escalate.
		classified := adapter.ClassifyError(err)
		slog.Warn("provider failed",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
			slog.String("error", err.Error()),
			slog.String("class", string(classified.Class)),
		)

		switch classified.Class {
		case ErrContextOverflow:
			// Find a model with larger context.
			larger := e.findLargerContextModel(m, tokensNeeded*2)
			if larger != nil {
				slog.Info("escalating on context overflow",
					slog.String("target_model", larger.ID),
					slog.Int("context_tokens", larger.MaxContextTokens),
				)
				a2 := e.adapters[larger.ProviderID]
				resp2, err2 := a2.Send(ctx, larger.ID, req)
				if err2 == nil {
					return Decision{
						ModelID:          larger.ID,
						ProviderID:       larger.ProviderID,
						EstimatedCostUSD: estimateCostUSD(tokensNeeded, 512, larger.InputPer1K, larger.OutputPer1K),
						Reason:           "escalated-context-overflow",
					}, resp2, nil
				}
			}
			// Fall through to try next eligible model.

		case ErrRateLimited:
			// Skip to next provider (different provider ID preferred).
			continue

		case ErrTransient:
			// Retry once with same model, then fall through.
			resp2, err2 := adapter.Send(ctx, m.ID, req)
			if err2 == nil {
				return Decision{
					ModelID:          m.ID,
					ProviderID:       m.ProviderID,
					EstimatedCostUSD: estCost,
					Reason:           "retried-transient",
				}, resp2, nil
			}
			continue

		case ErrFatal:
			// Don't retry fatals, try next model.
			continue
		}
	}

	return Decision{}, nil, errors.New("all providers failed")
}

// findLargerContextModel finds the smallest model with context larger than needed.
func (e *Engine) findLargerContextModel(current Model, tokensNeeded int) *Model {
	var best *Model
	for _, m := range e.models {
		if !m.Enabled || m.ID == current.ID {
			continue
		}
		if _, ok := e.adapters[m.ProviderID]; !ok {
			continue
		}
		if m.MaxContextTokens >= tokensNeeded && m.MaxContextTokens > current.MaxContextTokens {
			if best == nil || m.MaxContextTokens < best.MaxContextTokens {
				cp := m
				best = &cp
			}
		}
	}
	return best
}

func (e *Engine) Orchestrate(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	switch d.Mode {
	case "adversarial":
		return e.adversarial(ctx, req, d)
	default:
		// Default: single route-and-send (planning mode or fallback).
		p := Policy{
			Mode:         d.Mode,
			MinWeight:    d.PrimaryMinWeight,
			MaxLatencyMs: e.cfg.DefaultMaxLatencyMs,
			MaxBudgetUSD: e.cfg.DefaultMaxBudgetUSD,
		}
		return e.RouteAndSend(ctx, req, p)
	}
}

// adversarial implements the 3-phase plan/critique/refine pipeline.
func (e *Engine) adversarial(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	iterations := d.Iterations
	if iterations == 0 {
		iterations = 1
	}

	// Phase 1: Model A generates plan.
	planReq := Request{
		Messages: []Message{
			{Role: "system", Content: "You are a planning assistant. Generate a detailed plan to address the user's request."},
			{Role: "user", Content: messagesContent(req.Messages)},
		},
	}
	planPolicy := Policy{
		Mode:      "planning",
		MinWeight: d.PrimaryMinWeight,
	}
	planDec, planResp, err := e.RouteAndSend(ctx, planReq, planPolicy)
	if err != nil {
		return Decision{}, nil, fmt.Errorf("adversarial plan phase: %w", err)
	}

	plan := extractContent(planResp)

	var critique, refinedPlan string
	var lastDec Decision

	for i := 0; i < iterations; i++ {
		// Phase 2: Model B critiques the plan.
		critiqueReq := Request{
			Messages: []Message{
				{Role: "system", Content: "You are a critical reviewer. Analyze the plan below and provide constructive criticism."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nProposed plan:\n%s\n\nProvide your critique:", messagesContent(req.Messages), plan)},
			},
		}
		critiquePolicy := Policy{
			Mode:      "adversarial",
			MinWeight: d.ReviewMinWeight,
		}
		_, critiqueResp, err := e.RouteAndSend(ctx, critiqueReq, critiquePolicy)
		if err != nil {
			return Decision{}, nil, fmt.Errorf("adversarial critique phase: %w", err)
		}
		critique = extractContent(critiqueResp)

		// Phase 3: Model A refines based on critique.
		refineReq := Request{
			Messages: []Message{
				{Role: "system", Content: "You are a planning assistant. Refine your plan based on the critique provided."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nYour plan:\n%s\n\nCritique:\n%s\n\nProvide a refined plan:", messagesContent(req.Messages), plan, critique)},
			},
		}
		refinePolicy := Policy{
			Mode:      "planning",
			MinWeight: d.PrimaryMinWeight,
		}
		dec, refineResp, err := e.RouteAndSend(ctx, refineReq, refinePolicy)
		if err != nil {
			return Decision{}, nil, fmt.Errorf("adversarial refine phase: %w", err)
		}
		refinedPlan = extractContent(refineResp)
		plan = refinedPlan // use refined plan for next iteration
		lastDec = dec
	}

	// Build composite response.
	result := map[string]any{
		"initial_plan": extractContent(planResp),
		"critique":     critique,
		"refined_plan": refinedPlan,
	}
	resultJSON, _ := json.Marshal(result)

	return Decision{
		ModelID:          lastDec.ModelID,
		ProviderID:       lastDec.ProviderID,
		EstimatedCostUSD: planDec.EstimatedCostUSD + lastDec.EstimatedCostUSD,
		Reason:           "adversarial-orchestration",
	}, ProviderResponse(resultJSON), nil
}

// messagesContent concatenates all user message content.
func messagesContent(msgs []Message) string {
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

// extractContent tries to pull the text content from a provider response JSON.
func extractContent(resp ProviderResponse) string {
	// Try OpenAI format.
	var oai struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(resp, &oai) == nil && len(oai.Choices) > 0 {
		return oai.Choices[0].Message.Content
	}
	// Try Anthropic format.
	var ant struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(resp, &ant) == nil && len(ant.Content) > 0 {
		return ant.Content[0].Text
	}
	return string(resp)
}

func estimateCostUSD(inTokens, outTokens int, inPer1k, outPer1k float64) float64 {
	return (float64(inTokens)/1000.0)*inPer1k + (float64(outTokens)/1000.0)*outPer1k
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
