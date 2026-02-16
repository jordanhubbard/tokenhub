package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
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

// HealthChecker is an optional interface for provider health tracking.
// Defined here to avoid import cycles with the health package.
type HealthChecker interface {
	IsAvailable(providerID string) bool
	RecordSuccess(providerID string, latencyMs float64)
	RecordError(providerID string, errMsg string)
}

// StatsProvider optionally extends HealthChecker with scoring data.
type StatsProvider interface {
	GetAvgLatencyMs(providerID string) float64
	GetErrorRate(providerID string) float64
}

// ModeWeights defines scoring coefficients for a routing mode.
type ModeWeights struct {
	Cost    float64
	Latency float64
	Failure float64
	Weight  float64
}

// modeWeightProfiles maps routing modes to scoring coefficients.
// Lower score = better model choice.
var modeWeightProfiles = map[string]ModeWeights{
	"cheap":           {Cost: 0.7, Latency: 0.1, Failure: 0.1, Weight: 0.1},
	"normal":          {Cost: 0.25, Latency: 0.25, Failure: 0.25, Weight: 0.25},
	"high_confidence": {Cost: 0.05, Latency: 0.1, Failure: 0.15, Weight: 0.7},
	"planning":        {Cost: 0.1, Latency: 0.1, Failure: 0.2, Weight: 0.6},
	"adversarial":     {Cost: 0.1, Latency: 0.1, Failure: 0.2, Weight: 0.6},
}

type EngineConfig struct {
	DefaultMode         string
	DefaultMaxBudgetUSD float64
	DefaultMaxLatencyMs int
	MaxRetries          int
}

type Engine struct {
	cfg    EngineConfig
	health HealthChecker

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

// SetHealthChecker attaches a health tracker to the engine.
func (e *Engine) SetHealthChecker(h HealthChecker) {
	e.health = h
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

// UpdateDefaults updates the runtime routing policy defaults.
func (e *Engine) UpdateDefaults(mode string, maxBudget float64, maxLatencyMs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode != "" {
		e.cfg.DefaultMode = mode
	}
	if maxBudget > 0 {
		e.cfg.DefaultMaxBudgetUSD = maxBudget
	}
	if maxLatencyMs > 0 {
		e.cfg.DefaultMaxLatencyMs = maxLatencyMs
	}
}

// ListModels returns all registered models.
func (e *Engine) ListModels() []Model {
	e.mu.RLock()
	defer e.mu.RUnlock()
	models := make([]Model, 0, len(e.models))
	for _, m := range e.models {
		models = append(models, m)
	}
	return models
}

// ListAdapterIDs returns the IDs of all registered adapters.
func (e *Engine) ListAdapterIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]string, 0, len(e.adapters))
	for id := range e.adapters {
		ids = append(ids, id)
	}
	return ids
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

// eligibleModels returns models sorted by multi-objective score that meet the given constraints.
func (e *Engine) eligibleModels(tokensNeeded int, p Policy) []Model {
	var eligible []Model
	for _, m := range e.models {
		if !m.Enabled {
			continue
		}
		if p.MinWeight > 0 && m.Weight < p.MinWeight {
			continue
		}
		// Reserve 15% headroom for context estimation.
		contextWithHeadroom := int(float64(tokensNeeded) * 1.15)
		if m.MaxContextTokens > 0 && contextWithHeadroom > 0 && contextWithHeadroom > m.MaxContextTokens {
			continue
		}
		if _, ok := e.adapters[m.ProviderID]; !ok {
			continue // skip models without a registered adapter
		}
		if e.health != nil && !e.health.IsAvailable(m.ProviderID) {
			continue // skip providers in cooldown
		}
		if p.MaxBudgetUSD > 0 {
			est := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)
			if est > p.MaxBudgetUSD {
				continue
			}
		}
		eligible = append(eligible, m)
	}

	// Sort by multi-objective score (lower is better).
	scores := e.scoreModels(eligible, tokensNeeded, p.Mode)
	sort.Slice(eligible, func(i, j int) bool {
		return scores[eligible[i].ID] < scores[eligible[j].ID]
	})
	return eligible
}

// scoreModels computes a multi-objective score for each eligible model.
// Lower score = better model for the given routing mode.
func (e *Engine) scoreModels(models []Model, tokensNeeded int, mode string) map[string]float64 {
	w := modeWeightProfiles["normal"]
	if mw, ok := modeWeightProfiles[mode]; ok {
		w = mw
	}

	// Compute raw values for normalization.
	var maxCost, maxWeight float64
	var maxLatency, maxFailure float64

	// Get stats provider if available.
	sp, hasStats := e.health.(StatsProvider)

	for _, m := range models {
		cost := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)
		if cost > maxCost {
			maxCost = cost
		}
		if float64(m.Weight) > maxWeight {
			maxWeight = float64(m.Weight)
		}
		if hasStats {
			lat := sp.GetAvgLatencyMs(m.ProviderID)
			if lat > maxLatency {
				maxLatency = lat
			}
			fail := sp.GetErrorRate(m.ProviderID)
			if fail > maxFailure {
				maxFailure = fail
			}
		}
	}

	scores := make(map[string]float64, len(models))
	for _, m := range models {
		cost := estimateCostUSD(tokensNeeded, 512, m.InputPer1K, m.OutputPer1K)
		normCost := safeNorm(cost, maxCost)
		normWeight := safeNorm(float64(m.Weight), maxWeight)

		var normLatency, normFailure float64
		if hasStats {
			normLatency = safeNorm(sp.GetAvgLatencyMs(m.ProviderID), maxLatency)
			normFailure = safeNorm(sp.GetErrorRate(m.ProviderID), maxFailure)
		}

		// Lower score is better. Weight is subtracted (higher weight = better).
		score := w.Cost*normCost + w.Latency*normLatency + w.Failure*normFailure - w.Weight*normWeight
		scores[m.ID] = score
	}
	return scores
}

func safeNorm(v, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return clamp(v/max, 0, 1)
}

// backoffRetry retries fn with exponential backoff and jitter.
func backoffRetry(ctx context.Context, fn func() error, maxRetries int, baseDelay time.Duration) error {
	for i := 0; i < maxRetries; i++ {
		delay := baseDelay * time.Duration(1<<uint(i))
		// Add jitter: 50-150% of delay
		jitter := time.Duration(float64(delay) * (0.5 + rand.Float64()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}
		if err := fn(); err == nil {
			return nil
		}
	}
	return errors.New("retries exhausted")
}

func (e *Engine) RouteAndSend(ctx context.Context, req Request, p Policy) (Decision, ProviderResponse, error) {
	if p.Mode == "" {
		p.Mode = e.cfg.DefaultMode
	}
	if p.MaxBudgetUSD == 0 {
		p.MaxBudgetUSD = e.cfg.DefaultMaxBudgetUSD
	}
	if p.MaxLatencyMs == 0 {
		p.MaxLatencyMs = e.cfg.DefaultMaxLatencyMs
	}

	// Enforce latency budget with context timeout.
	if p.MaxLatencyMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(p.MaxLatencyMs)*time.Millisecond)
		defer cancel()
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	tokensNeeded := estimateTokens(req)
	eligible := e.eligibleModels(tokensNeeded, p)

	// Honor model hint if specified.
	if req.ModelHint != "" {
		if hinted, ok := e.models[req.ModelHint]; ok && hinted.Enabled {
			if adapter, hasAdapter := e.adapters[hinted.ProviderID]; hasAdapter {
				if e.health == nil || e.health.IsAvailable(hinted.ProviderID) {
					estCost := estimateCostUSD(tokensNeeded, 512, hinted.InputPer1K, hinted.OutputPer1K)
					slog.Info("trying model hint",
						slog.String("model", hinted.ID),
						slog.String("provider", hinted.ProviderID),
					)
					sendStart := time.Now()
					resp, err := adapter.Send(ctx, hinted.ID, req)
					sendMs := float64(time.Since(sendStart).Milliseconds())
					if err == nil {
						if e.health != nil {
							e.health.RecordSuccess(hinted.ProviderID, sendMs)
						}
						return Decision{
							ModelID:          hinted.ID,
							ProviderID:       hinted.ProviderID,
							EstimatedCostUSD: estCost,
							Reason:           "model-hint",
						}, resp, nil
					}
					if e.health != nil {
						e.health.RecordError(hinted.ProviderID, err.Error())
					}
					slog.Warn("model hint failed, falling through to scored routing",
						slog.String("model", hinted.ID),
						slog.String("error", err.Error()),
					)
				}
			}
		}
	}

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

		sendStart := time.Now()
		resp, err := adapter.Send(ctx, m.ID, req)
		sendMs := float64(time.Since(sendStart).Milliseconds())

		if err == nil {
			if e.health != nil {
				e.health.RecordSuccess(m.ProviderID, sendMs)
			}
			return Decision{
				ModelID:          m.ID,
				ProviderID:       m.ProviderID,
				EstimatedCostUSD: estCost,
				Reason:           fmt.Sprintf("routed-weight-%d", m.Weight),
			}, resp, nil
		}

		if e.health != nil {
			e.health.RecordError(m.ProviderID, err.Error())
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
			if classified.RetryAfter > 0 {
				slog.Info("rate limited, retry-after reported",
					slog.Int("retry_after_sec", classified.RetryAfter),
				)
			}
			// Skip to next provider (different provider ID preferred).
			continue

		case ErrTransient:
			// Retry with exponential backoff + jitter.
			var resp2 ProviderResponse
			retryErr := backoffRetry(ctx, func() error {
				var sendErr error
				resp2, sendErr = adapter.Send(ctx, m.ID, req)
				return sendErr
			}, 2, 100*time.Millisecond)
			if retryErr == nil {
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
	case "vote":
		return e.vote(ctx, req, d)
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

// vote implements multi-model voting: sends the same request to N models,
// then picks the best response via a judge model.
func (e *Engine) vote(ctx context.Context, req Request, d OrchestrationDirective) (Decision, ProviderResponse, error) {
	voters := d.Iterations
	if voters < 2 {
		voters = 3 // default to 3 voters
	}

	e.mu.RLock()
	tokensNeeded := estimateTokens(req)
	voterPolicy := Policy{
		Mode:      "normal",
		MinWeight: d.PrimaryMinWeight,
	}
	eligible := e.eligibleModels(tokensNeeded, voterPolicy)
	e.mu.RUnlock()

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
		adapter, ok := e.adapters[m.ProviderID]
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
			content := extractContent(resp)
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
			{Role: "user", Content: fmt.Sprintf("Original prompt: %s\n\nResponses:%s\n\nWhich response number is best?", messagesContent(req.Messages), responseSummary)},
		},
	}
	judgePolicy := Policy{
		Mode:      "high_confidence",
		MinWeight: d.ReviewMinWeight,
	}
	judgeDec, judgeResp, err := e.RouteAndSend(ctx, judgeReq, judgePolicy)
	totalCost += judgeDec.EstimatedCostUSD

	// Parse the judge's selection.
	selectedIdx := 0 // default to first
	if err == nil {
		judgeContent := extractContent(judgeResp)
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
