package temporal

import (
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

const (
	maxEscalations   = 5
	activityTimeout  = 60 * time.Second
	workflowTimeout  = 5 * time.Minute
)

// ChatWorkflow replaces engine.RouteAndSend() as a Temporal workflow.
func ChatWorkflow(ctx workflow.Context, input ChatInput) (ChatOutput, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: activityTimeout,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1, // Activities handle their own retry logic.
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	start := workflow.Now(ctx)

	// Step 1: Select model.
	var decision router.Decision
	err := workflow.ExecuteActivity(ctx, (*Activities).SelectModel, input).Get(ctx, &decision)
	if err != nil {
		return ChatOutput{Error: err.Error()}, err
	}

	// Step 2: Send to provider (with escalation loop on failure).
	var sendOutput SendOutput
	currentModelID := decision.ModelID
	currentProviderID := decision.ProviderID

	for attempt := 0; attempt < maxEscalations; attempt++ {
		sendInput := SendInput{
			ProviderID: currentProviderID,
			ModelID:    currentModelID,
			Request:    input.Request,
		}

		err = workflow.ExecuteActivity(ctx, (*Activities).SendToProvider, sendInput).Get(ctx, &sendOutput)
		if err == nil {
			break
		}

		// Step 3: Classify error and escalate.
		tokens := EstimateTokens(input.Request)

		var escOutput EscalateOutput
		escInput := EscalateInput{
			ErrorMsg:       err.Error(),
			CurrentModelID: currentModelID,
			TokensNeeded:   tokens,
		}
		escErr := workflow.ExecuteActivity(ctx, (*Activities).ClassifyAndEscalate, escInput).Get(ctx, &escOutput)
		if escErr != nil || !escOutput.ShouldRetry || escOutput.NextModelID == "" {
			break // no more fallbacks
		}

		currentModelID = escOutput.NextModelID
		// Resolve the provider for the new model.
		var newProviderID string
		if resolveErr := workflow.ExecuteActivity(ctx, (*Activities).ResolveModel, currentModelID).Get(ctx, &newProviderID); resolveErr == nil {
			currentProviderID = newProviderID
		}
	}

	latencyMs := workflow.Now(ctx).Sub(start).Milliseconds()

	// Step 4: Log result.
	logInput := LogInput{
		RequestID:  input.RequestID,
		ModelID:    currentModelID,
		ProviderID: currentProviderID,
		Mode:       input.Policy.Mode,
		LatencyMs:  latencyMs,
		CostUSD:    sendOutput.EstimatedCost,
		Success:    err == nil,
		ErrorClass: sendOutput.ErrorClass,
	}
	_ = workflow.ExecuteActivity(ctx, (*Activities).LogResult, logInput).Get(ctx, nil)

	if err != nil {
		return ChatOutput{
			Decision:  decision,
			LatencyMs: latencyMs,
			Error:     err.Error(),
		}, err
	}

	return ChatOutput{
		Decision: router.Decision{
			ModelID:          currentModelID,
			ProviderID:       currentProviderID,
			EstimatedCostUSD: sendOutput.EstimatedCost,
			Reason:           decision.Reason,
		},
		Response:  sendOutput.Response,
		LatencyMs: latencyMs,
	}, nil
}

// OrchestrationWorkflow replaces engine.Orchestrate() as a Temporal workflow.
func OrchestrationWorkflow(ctx workflow.Context, input OrchestrationInput) (ChatOutput, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: activityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	start := workflow.Now(ctx)

	switch input.Directive.Mode {
	case "adversarial":
		return adversarialWorkflow(ctx, input, start)
	case "vote":
		return voteWorkflow(ctx, input, start)
	case "refine":
		return refineWorkflow(ctx, input, start)
	default:
		// Default: single chat workflow (planning mode).
		return ChatWorkflow(ctx, ChatInput{
			RequestID: input.RequestID,
			APIKeyID:  input.APIKeyID,
			Request:   input.Request,
			Policy: router.Policy{
				Mode:      input.Directive.Mode,
				MinWeight: input.Directive.PrimaryMinWeight,
			},
		})
	}
}

func adversarialWorkflow(ctx workflow.Context, input OrchestrationInput, start time.Time) (ChatOutput, error) {
	iterations := input.Directive.Iterations
	if iterations == 0 {
		iterations = 1
	}

	// Phase 1: Generate plan.
	planReq := router.Request{
		Messages: []router.Message{
			{Role: "system", Content: "You are a planning assistant. Generate a detailed plan to address the user's request."},
			{Role: "user", Content: messagesContent(input.Request.Messages)},
		},
	}
	planInput := ChatInput{
		RequestID: input.RequestID + "-plan",
		Request:   planReq,
		Policy:    router.Policy{Mode: "planning", MinWeight: input.Directive.PrimaryMinWeight},
	}
	if input.Directive.PrimaryModelID != "" {
		planInput.Request.ModelHint = input.Directive.PrimaryModelID
	}

	var planOutput ChatOutput
	err := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, planInput).Get(ctx, &planOutput)
	if err != nil {
		return ChatOutput{Error: "adversarial plan phase: " + err.Error()}, err
	}
	plan := extractContentFromRaw(planOutput.Response)
	totalCost := planOutput.Decision.EstimatedCostUSD

	var critique, refinedPlan string
	lastDecision := planOutput.Decision

	for i := 0; i < iterations; i++ {
		// Phase 2: Critique.
		critiqueReq := router.Request{
			Messages: []router.Message{
				{Role: "system", Content: "You are a critical reviewer. Analyze the plan below and provide constructive criticism."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nProposed plan:\n%s\n\nProvide your critique:", messagesContent(input.Request.Messages), plan)},
			},
		}
		critiqueInput := ChatInput{
			RequestID: fmt.Sprintf("%s-critique-%d", input.RequestID, i),
			Request:   critiqueReq,
			Policy:    router.Policy{Mode: "adversarial", MinWeight: input.Directive.ReviewMinWeight},
		}
		if input.Directive.ReviewModelID != "" {
			critiqueInput.Request.ModelHint = input.Directive.ReviewModelID
		}

		var critiqueOutput ChatOutput
		err := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, critiqueInput).Get(ctx, &critiqueOutput)
		if err != nil {
			return ChatOutput{Error: "adversarial critique phase: " + err.Error()}, err
		}
		critique = extractContentFromRaw(critiqueOutput.Response)
		totalCost += critiqueOutput.Decision.EstimatedCostUSD

		// Phase 3: Refine.
		refineReq := router.Request{
			Messages: []router.Message{
				{Role: "system", Content: "You are a planning assistant. Refine your plan based on the critique provided."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nYour plan:\n%s\n\nCritique:\n%s\n\nProvide a refined plan:", messagesContent(input.Request.Messages), plan, critique)},
			},
		}
		refineInput := ChatInput{
			RequestID: fmt.Sprintf("%s-refine-%d", input.RequestID, i),
			Request:   refineReq,
			Policy:    router.Policy{Mode: "planning", MinWeight: input.Directive.PrimaryMinWeight},
		}
		if input.Directive.PrimaryModelID != "" {
			refineInput.Request.ModelHint = input.Directive.PrimaryModelID
		}

		var refineOutput ChatOutput
		err = workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, refineInput).Get(ctx, &refineOutput)
		if err != nil {
			return ChatOutput{Error: "adversarial refine phase: " + err.Error()}, err
		}
		refinedPlan = extractContentFromRaw(refineOutput.Response)
		plan = refinedPlan
		lastDecision = refineOutput.Decision
		totalCost += refineOutput.Decision.EstimatedCostUSD
	}

	result := map[string]any{
		"initial_plan": extractContentFromRaw(planOutput.Response),
		"critique":     critique,
		"refined_plan": refinedPlan,
	}
	resultJSON, _ := json.Marshal(result)

	return ChatOutput{
		Decision: router.Decision{
			ModelID:          lastDecision.ModelID,
			ProviderID:       lastDecision.ProviderID,
			EstimatedCostUSD: totalCost,
			Reason:           "adversarial-orchestration",
		},
		Response:  resultJSON,
		LatencyMs: workflow.Now(ctx).Sub(start).Milliseconds(),
	}, nil
}

func voteWorkflow(ctx workflow.Context, input OrchestrationInput, start time.Time) (ChatOutput, error) {
	voters := input.Directive.Iterations
	if voters < 2 {
		voters = 3
	}

	// Fan out N parallel child workflows.
	var futures []workflow.ChildWorkflowFuture
	for i := 0; i < voters; i++ {
		childInput := ChatInput{
			RequestID: fmt.Sprintf("%s-vote-%d", input.RequestID, i),
			Request:   input.Request,
			Policy:    router.Policy{Mode: "normal", MinWeight: input.Directive.PrimaryMinWeight},
		}
		future := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, childInput)
		futures = append(futures, future)
	}

	// Collect results.
	type voteResult struct {
		modelID    string
		providerID string
		content    string
		cost       float64
	}

	var results []voteResult
	var totalCost float64
	for _, f := range futures {
		var out ChatOutput
		if err := f.Get(ctx, &out); err != nil {
			continue // skip failed voters
		}
		results = append(results, voteResult{
			modelID:    out.Decision.ModelID,
			providerID: out.Decision.ProviderID,
			content:    extractContentFromRaw(out.Response),
			cost:       out.Decision.EstimatedCostUSD,
		})
		totalCost += out.Decision.EstimatedCostUSD
	}

	if len(results) == 0 {
		return ChatOutput{Error: "all voters failed"}, fmt.Errorf("all voters failed")
	}

	// If only one result, return directly.
	if len(results) == 1 {
		resultJSON, _ := json.Marshal(map[string]any{
			"responses": []map[string]any{
				{"model": results[0].modelID, "content": results[0].content},
			},
			"selected": 0,
		})
		return ChatOutput{
			Decision: router.Decision{
				ModelID:          results[0].modelID,
				ProviderID:       results[0].providerID,
				EstimatedCostUSD: totalCost,
				Reason:           "vote-single-response",
			},
			Response:  resultJSON,
			LatencyMs: workflow.Now(ctx).Sub(start).Milliseconds(),
		}, nil
	}

	// Judge phase.
	var responseSummary string
	for i, r := range results {
		responseSummary += fmt.Sprintf("\n--- Response %d (model: %s) ---\n%s\n", i+1, r.modelID, r.content)
	}

	judgeReq := router.Request{
		Messages: []router.Message{
			{Role: "system", Content: "You are a judge. Given multiple AI responses to the same prompt, select the best one. Reply with ONLY the number (1-based) of the best response."},
			{Role: "user", Content: fmt.Sprintf("Original prompt: %s\n\nResponses:%s\n\nWhich response number is best?", messagesContent(input.Request.Messages), responseSummary)},
		},
	}
	if input.Directive.ReviewModelID != "" {
		judgeReq.ModelHint = input.Directive.ReviewModelID
	}
	judgeInput := ChatInput{
		RequestID: input.RequestID + "-judge",
		Request:   judgeReq,
		Policy:    router.Policy{Mode: "high_confidence", MinWeight: input.Directive.ReviewMinWeight},
	}

	var judgeOutput ChatOutput
	if judgeErr := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, judgeInput).Get(ctx, &judgeOutput); judgeErr != nil {
		// Judge failed — return first response as fallback.
		resultJSON, _ := json.Marshal(map[string]any{
			"responses":   []map[string]any{{"model": results[0].modelID, "content": results[0].content}},
			"selected":    0,
			"judge_error": judgeErr.Error(),
		})
		return ChatOutput{
			Decision: router.Decision{
				ModelID:          results[0].modelID,
				ProviderID:       results[0].providerID,
				EstimatedCostUSD: totalCost,
				Reason:           "vote-judge-failed",
			},
			Response:  resultJSON,
			LatencyMs: workflow.Now(ctx).Sub(start).Milliseconds(),
		}, nil
	}
	totalCost += judgeOutput.Decision.EstimatedCostUSD

	// Parse judge selection.
	selectedIdx := 0
	judgeContent := extractContentFromRaw(judgeOutput.Response)
	for i := len(results); i >= 1; i-- {
		if containsDigit(judgeContent, i) {
			selectedIdx = i - 1
			break
		}
	}

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
		"judge":     judgeOutput.Decision.ModelID,
	})

	return ChatOutput{
		Decision: router.Decision{
			ModelID:          results[selectedIdx].modelID,
			ProviderID:       results[selectedIdx].providerID,
			EstimatedCostUSD: totalCost,
			Reason:           "vote-orchestration",
		},
		Response:  resultJSON,
		LatencyMs: workflow.Now(ctx).Sub(start).Milliseconds(),
	}, nil
}

func refineWorkflow(ctx workflow.Context, input OrchestrationInput, start time.Time) (ChatOutput, error) {
	iterations := input.Directive.Iterations
	if iterations == 0 {
		iterations = 2
	}

	// Phase 1: Initial response.
	initialInput := ChatInput{
		RequestID: input.RequestID + "-initial",
		Request:   input.Request,
		Policy:    router.Policy{Mode: "high_confidence", MinWeight: input.Directive.PrimaryMinWeight},
	}
	if input.Directive.PrimaryModelID != "" {
		initialInput.Request.ModelHint = input.Directive.PrimaryModelID
	}

	var initialOutput ChatOutput
	err := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, initialInput).Get(ctx, &initialOutput)
	if err != nil {
		return ChatOutput{Error: "refine initial phase: " + err.Error()}, err
	}

	currentContent := extractContentFromRaw(initialOutput.Response)
	totalCost := initialOutput.Decision.EstimatedCostUSD
	lastDecision := initialOutput.Decision

	// Phase 2: Iterative refinement.
	for i := 0; i < iterations; i++ {
		refineReq := router.Request{
			Messages: []router.Message{
				{Role: "system", Content: "Review and improve the following response. Fix any errors, add missing details, and improve clarity."},
				{Role: "user", Content: fmt.Sprintf("Original request: %s\n\nCurrent response:\n%s\n\nProvide an improved version:", messagesContent(input.Request.Messages), currentContent)},
			},
		}

		refineInput := ChatInput{
			RequestID: fmt.Sprintf("%s-refine-%d", input.RequestID, i),
			Request:   refineReq,
			Policy:    router.Policy{Mode: "high_confidence", MinWeight: input.Directive.PrimaryMinWeight},
		}
		refineInput.Request.ModelHint = lastDecision.ModelID // same model for refinement

		var refineOutput ChatOutput
		err := workflow.ExecuteChildWorkflow(ctx, ChatWorkflow, refineInput).Get(ctx, &refineOutput)
		if err != nil {
			break // return best so far
		}

		currentContent = extractContentFromRaw(refineOutput.Response)
		lastDecision = refineOutput.Decision
		totalCost += refineOutput.Decision.EstimatedCostUSD
	}

	result := map[string]any{
		"refined_response": currentContent,
		"iterations":       iterations,
		"model":            lastDecision.ModelID,
	}
	resultJSON, _ := json.Marshal(result)

	return ChatOutput{
		Decision: router.Decision{
			ModelID:          lastDecision.ModelID,
			ProviderID:       lastDecision.ProviderID,
			EstimatedCostUSD: totalCost,
			Reason:           "refine-orchestration",
		},
		Response:  resultJSON,
		LatencyMs: workflow.Now(ctx).Sub(start).Milliseconds(),
	}, nil
}

// Helper functions.

func messagesContent(msgs []router.Message) string {
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

func extractContentFromRaw(raw json.RawMessage) string {
	var oai struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &oai) == nil && len(oai.Choices) > 0 {
		return oai.Choices[0].Message.Content
	}
	var ant struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &ant) == nil && len(ant.Content) > 0 {
		return ant.Content[0].Text
	}
	return string(raw)
}

func containsDigit(s string, n int) bool {
	ns := fmt.Sprintf("%d", n)
	for i := 0; i <= len(s)-len(ns); i++ {
		if s[i:i+len(ns)] == ns {
			return true
		}
	}
	return false
}
