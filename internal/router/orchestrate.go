package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// sendToModel sends a request to a specific model by ID. Returns an error if
// the model is not found, not enabled, or the adapter is missing.
func (e *Engine) sendToModel(ctx context.Context, modelID string, req Request) (Decision, ProviderResponse, error) {
	m, ok := e.models[modelID]
	if !ok || !m.Enabled {
		return Decision{}, nil, fmt.Errorf("model %q not found or disabled", modelID)
	}
	adapter, ok := e.adapters[m.ProviderID]
	if !ok {
		return Decision{}, nil, fmt.Errorf("no adapter for provider %q", m.ProviderID)
	}
	tokens := EstimateTokens(req)
	estCost := estimateCostUSD(tokens, 512, m.InputPer1K, m.OutputPer1K)
	resp, err := adapter.Send(ctx, m.ID, req)
	if err != nil {
		return Decision{}, nil, err
	}
	return Decision{
		ModelID:          m.ID,
		ProviderID:       m.ProviderID,
		EstimatedCostUSD: estCost,
		Reason:           "explicit-model",
	}, resp, nil
}

func (e *Engine) Orchestrate(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	switch d.Mode {
	case "adversarial":
		return e.adversarial(ctx, req, d)
	case "vote":
		return e.vote(ctx, req, d)
	case "refine":
		return e.refine(ctx, req, d)
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
			{Role: "user", Content: MessagesContent(req.Messages)},
		},
	}
	var planDec Decision
	var planResp ProviderResponse
	var err error
	if d.PrimaryModelID != "" {
		planDec, planResp, err = e.sendToModel(ctx, d.PrimaryModelID, planReq)
		if err != nil {
			slog.Warn("explicit primary model failed for plan phase, falling through to routing",
				slog.String("model", d.PrimaryModelID),
				slog.String("error", err.Error()),
			)
			err = nil // reset so fallback can proceed
		}
	}
	if planResp == nil {
		planPolicy := Policy{
			Mode:      "planning",
			MinWeight: d.PrimaryMinWeight,
		}
		planDec, planResp, err = e.RouteAndSend(ctx, planReq, planPolicy)
		if err != nil {
			return Decision{}, nil, fmt.Errorf("adversarial plan phase: %w", err)
		}
	}

	plan := ExtractContent(planResp)

	var critique, refinedPlan string
	var lastDec Decision

	for i := 0; i < iterations; i++ {
		// Phase 2: Model B critiques the plan.
		critiqueReq := Request{
			Messages: []Message{
				{Role: "system", Content: "You are a critical reviewer. Analyze the plan below and provide constructive criticism."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nProposed plan:\n%s\n\nProvide your critique:", MessagesContent(req.Messages), plan)},
			},
		}
		var critiqueResp ProviderResponse
		var critiqueErr error
		if d.ReviewModelID != "" {
			_, critiqueResp, critiqueErr = e.sendToModel(ctx, d.ReviewModelID, critiqueReq)
			if critiqueErr != nil {
				slog.Warn("explicit review model failed for critique phase, falling through to routing",
					slog.String("model", d.ReviewModelID),
					slog.String("error", critiqueErr.Error()),
				)
			}
		}
		if critiqueResp == nil {
			critiquePolicy := Policy{
				Mode:      "adversarial",
				MinWeight: d.ReviewMinWeight,
			}
			_, critiqueResp, critiqueErr = e.RouteAndSend(ctx, critiqueReq, critiquePolicy)
			if critiqueErr != nil {
				return Decision{}, nil, fmt.Errorf("adversarial critique phase: %w", critiqueErr)
			}
		}
		critique = ExtractContent(critiqueResp)

		// Phase 3: Model A refines based on critique.
		refineReq := Request{
			Messages: []Message{
				{Role: "system", Content: "You are a planning assistant. Refine your plan based on the critique provided."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nYour plan:\n%s\n\nCritique:\n%s\n\nProvide a refined plan:", MessagesContent(req.Messages), plan, critique)},
			},
		}
		var dec Decision
		var refineResp ProviderResponse
		var refineErr error
		if d.PrimaryModelID != "" {
			dec, refineResp, refineErr = e.sendToModel(ctx, d.PrimaryModelID, refineReq)
			if refineErr != nil {
				slog.Warn("explicit primary model failed for refine phase, falling through to routing",
					slog.String("model", d.PrimaryModelID),
					slog.String("error", refineErr.Error()),
				)
			}
		}
		if refineResp == nil {
			refinePolicy := Policy{
				Mode:      "planning",
				MinWeight: d.PrimaryMinWeight,
			}
			dec, refineResp, refineErr = e.RouteAndSend(ctx, refineReq, refinePolicy)
			if refineErr != nil {
				return Decision{}, nil, fmt.Errorf("adversarial refine phase: %w", refineErr)
			}
		}
		refinedPlan = ExtractContent(refineResp)
		plan = refinedPlan // use refined plan for next iteration
		lastDec = dec
	}

	// Build composite response.
	result := map[string]any{
		"initial_plan": ExtractContent(planResp),
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

// vote implements multi-model voting: sends the same request to N models,
// then picks the best response via a judge model.
func (e *Engine) vote(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	voters := d.Iterations
	if voters < 2 {
		voters = 3 // default to 3 voters
	}

	e.mu.RLock()
	tokensNeeded := EstimateTokens(req)
	voterPolicy := Policy{
		Mode:      "normal",
		MinWeight: d.PrimaryMinWeight,
	}
	eligible := e.eligibleModels(tokensNeeded, voterPolicy)
	// Snapshot adapters for eligible models before releasing the lock.
	voteAdapters := make(map[string]Sender, len(eligible))
	for _, m := range eligible {
		if a, ok := e.adapters[m.ProviderID]; ok {
			voteAdapters[m.ProviderID] = a
		}
	}
	e.mu.RUnlock()

	// Exclude the explicit judge from voters to avoid duplication.
	if d.ReviewModelID != "" {
		filtered := eligible[:0]
		for _, m := range eligible {
			if m.ID != d.ReviewModelID {
				filtered = append(filtered, m)
			}
		}
		eligible = filtered
	}

	if len(eligible) == 0 {
		return Decision{}, nil, errors.New("no eligible models for vote")
	}

	// Limit voters to available models.
	if voters > len(eligible) {
		voters = len(eligible)
	}

	// Collect responses from each voter concurrently.
	type voteResult struct {
		modelID    string
		providerID string
		content    string
		cost       float64
	}

	resultsCh := make(chan voteResult, voters)
	var wg sync.WaitGroup

	for i := 0; i < voters; i++ {
		m := eligible[i%len(eligible)]
		adapter, ok := voteAdapters[m.ProviderID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(m Model, adapter Sender) {
			defer wg.Done()
			estCost := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)
			resp, err := adapter.Send(ctx, m.ID, req)
			if err != nil {
				return
			}
			content := ExtractContent(resp)
			resultsCh <- voteResult{
				modelID:    m.ID,
				providerID: m.ProviderID,
				content:    content,
				cost:       estCost,
			}
		}(m, adapter)
	}

	// Close channel once all goroutines complete.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var results []voteResult
	var totalCost float64
	for r := range resultsCh {
		results = append(results, r)
		totalCost += r.cost
	}

	if len(results) == 0 {
		return Decision{}, nil, errors.New("all voters failed")
	}

	// If only one response, return it directly.
	if len(results) == 1 {
		resultJSON, _ := json.Marshal(map[string]any{
			"responses": []map[string]any{
				{"model": results[0].modelID, "content": results[0].content},
			},
			"selected": 0,
		})
		return Decision{
			ModelID:          results[0].modelID,
			ProviderID:       results[0].providerID,
			EstimatedCostUSD: totalCost,
			Reason:           "vote-single-response",
		}, ProviderResponse(resultJSON), nil
	}

	// Build judge prompt.
	var responseSummary string
	for i, r := range results {
		responseSummary += fmt.Sprintf("\n--- Response %d (model: %s) ---\n%s\n", i+1, r.modelID, r.content)
	}

	judgeReq := Request{
		Messages: []Message{
			{Role: "system", Content: "You are a judge. Given multiple AI responses to the same prompt, select the best one. Reply with ONLY the number (1-based) of the best response."},
			{Role: "user", Content: fmt.Sprintf("Original prompt: %s\n\nResponses:%s\n\nWhich response number is best?", MessagesContent(req.Messages), responseSummary)},
		},
	}
	var judgeDec Decision
	var judgeResp ProviderResponse
	var err error
	if d.ReviewModelID != "" {
		judgeDec, judgeResp, err = e.sendToModel(ctx, d.ReviewModelID, judgeReq)
		if err != nil {
			slog.Warn("explicit review model failed for judge phase, falling through to routing",
				slog.String("model", d.ReviewModelID),
				slog.String("error", err.Error()),
			)
			err = nil // reset so fallback can proceed
		}
	}
	if judgeResp == nil {
		judgePolicy := Policy{
			Mode:      "high_confidence",
			MinWeight: d.ReviewMinWeight,
		}
		judgeDec, judgeResp, err = e.RouteAndSend(ctx, judgeReq, judgePolicy)
	}
	totalCost += judgeDec.EstimatedCostUSD

	// Parse the judge's selection.
	selectedIdx := 0 // default to first
	if err == nil {
		judgeContent := ExtractContent(judgeResp)
		for i := len(results); i >= 1; i-- {
			if strings.Contains(judgeContent, fmt.Sprintf("%d", i)) {
				selectedIdx = i - 1
				break
			}
		}
	}

	// Build composite result.
	var responses []map[string]any
	for i, r := range results {
		responses = append(responses, map[string]any{
			"model":    r.modelID,
			"content":  r.content,
			"selected": i == selectedIdx,
		})
	}
	resultJSON, _ := json.Marshal(map[string]any{
		"responses": responses,
		"selected":  selectedIdx,
		"judge":     judgeDec.ModelID,
	})

	winner := results[selectedIdx]
	return Decision{
		ModelID:          winner.modelID,
		ProviderID:       winner.providerID,
		EstimatedCostUSD: totalCost,
		Reason:           "vote-orchestration",
	}, ProviderResponse(resultJSON), nil
}

// refine implements same-model iterative refinement. It sends the request to a
// single model, then iteratively asks the same model to review and improve its
// own response. The number of refinement iterations is controlled by
// d.Iterations (default 2 if not set).
func (e *Engine) refine(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	iterations := d.Iterations
	if iterations == 0 {
		iterations = 2
	}

	// Phase 1: Initial response from a single model.
	var initialDec Decision
	var initialResp ProviderResponse
	var err error

	if d.PrimaryModelID != "" {
		initialDec, initialResp, err = e.sendToModel(ctx, d.PrimaryModelID, req)
		if err != nil {
			slog.Warn("explicit primary model failed for refine initial phase, falling through to routing",
				slog.String("model", d.PrimaryModelID),
				slog.String("error", err.Error()),
			)
			err = nil // reset so fallback can proceed
		}
	}
	if initialResp == nil {
		refinePolicy := Policy{
			Mode:      "high_confidence",
			MinWeight: d.PrimaryMinWeight,
		}
		initialDec, initialResp, err = e.RouteAndSend(ctx, req, refinePolicy)
		if err != nil {
			return Decision{}, nil, fmt.Errorf("refine initial phase: %w", err)
		}
	}

	currentContent := ExtractContent(initialResp)
	lastDec := initialDec
	totalCost := initialDec.EstimatedCostUSD

	// Determine which model ID to use for refinement iterations.
	refineModelID := lastDec.ModelID

	// Phase 2: Iterative refinement using the same model.
	for i := 0; i < iterations; i++ {
		refineReq := Request{
			Messages: []Message{
				{Role: "system", Content: "Review and improve the following response. Fix any errors, add missing details, and improve clarity."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nCurrent response:\n%s\n\nProvide an improved version:", MessagesContent(req.Messages), currentContent)},
			},
		}

		var dec Decision
		var refineResp ProviderResponse
		var refineErr error

		dec, refineResp, refineErr = e.sendToModel(ctx, refineModelID, refineReq)
		if refineErr != nil {
			slog.Warn("refine iteration failed",
				slog.String("model", refineModelID),
				slog.Int("iteration", i+1),
				slog.String("error", refineErr.Error()),
			)
			// On failure, return the best response we have so far.
			break
		}

		currentContent = ExtractContent(refineResp)
		lastDec = dec
		totalCost += dec.EstimatedCostUSD
	}

	// Build composite response.
	result := map[string]any{
		"refined_response": currentContent,
		"iterations":       iterations,
		"model":            lastDec.ModelID,
	}
	resultJSON, _ := json.Marshal(result)

	return Decision{
		ModelID:          lastDec.ModelID,
		ProviderID:       lastDec.ProviderID,
		EstimatedCostUSD: totalCost,
		Reason:           "refine-orchestration",
	}, ProviderResponse(resultJSON), nil
}
