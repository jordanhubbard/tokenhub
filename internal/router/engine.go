package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Describer is an optional interface that adapters can implement to expose
// metadata like base URL and health endpoint for the admin UI.
type Describer interface {
	HealthEndpoint() string
}

// StreamSender is an optional interface for provider adapters that support SSE streaming.
type StreamSender interface {
	Sender
	SendStream(ctx context.Context, model string, req Request) (io.ReadCloser, error)
}

// ErrorClass classifies provider errors for routing decisions.
type ErrorClass string

const (
	ErrContextOverflow ErrorClass = "context_overflow"
	ErrRateLimited     ErrorClass = "rate_limited"
	ErrTransient       ErrorClass = "transient"
	ErrFatal           ErrorClass = "fatal"
	// ErrBudgetExceeded signals that the provider's spending budget is exhausted.
	// The engine will disable the model so it is not selected again until manually
	// re-enabled via the admin API, saving wasted round-trips to a permanently
	// unavailable provider.
	ErrBudgetExceeded ErrorClass = "budget_exceeded"
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

// SkipRecorder receives a notification each time a model/provider is excluded
// from routing. Implemented by the metrics registry to count skip reasons.
type SkipRecorder interface {
	RecordProviderSkip(providerID string, reason string)
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
	// ExplorationTemp controls softmax temperature for load distribution.
	// 0 = always pick top model, 0.5 = moderate exploration, 1.0 = strong.
	ExplorationTemp float64
	// PerProviderTimeoutMs caps how long a single provider attempt may take.
	// When set, each adapter.Send call receives its own context deadline so
	// that one slow or unreachable provider cannot consume the entire routing
	// budget and starve all fallback providers. If 0, per-provider capping is
	// disabled and only the overall DefaultMaxLatencyMs deadline applies.
	PerProviderTimeoutMs int
	// HedgeAfterMs, if > 0, fires a parallel request to the next-best provider
	// when the primary hasn't responded within this interval. Set to 0 to keep
	// purely sequential fallback. A value of 5000 (5 s) is a reasonable starting
	// point: it hedges on slow providers without wasting tokens on fast ones.
	// When 0, all parallelism is disabled and providers are tried sequentially.
	HedgeAfterMs int
	// MaxHedgedProviders caps the number of concurrent in-flight hedged requests.
	// Defaults to 3 (primary + 2 hedges) when HedgeAfterMs > 0 and not set.
	MaxHedgedProviders int
}

type Engine struct {
	cfg          EngineConfig
	health       HealthChecker
	bandit       *ThompsonSampler // nil = disabled
	skipRecorder SkipRecorder

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

// SetSkipRecorder attaches a recorder that is notified whenever a model or
// provider is excluded from routing (e.g. health_down, budget_exceeded).
func (e *Engine) SetSkipRecorder(sr SkipRecorder) {
	e.skipRecorder = sr
}

// SetBanditPolicy attaches a Thompson Sampling policy for RL-based routing.
// When set and mode is "thompson", model selection uses probabilistic sampling
// instead of the deterministic multi-objective scoring function.
func (e *Engine) SetBanditPolicy(ts *ThompsonSampler) {
	e.bandit = ts
}

// RegisterAdapter registers a provider adapter.
func (e *Engine) RegisterAdapter(a Sender) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adapters[a.ID()] = a
}

// UnregisterAdapter removes a provider adapter by ID. Models that reference
// this provider remain registered but become ineligible for routing until a
// new adapter with the same ID is registered.
func (e *Engine) UnregisterAdapter(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.adapters, id)
}

func (e *Engine) RegisterModel(m Model) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models[m.ID] = m
}

// HasModel returns true if a model with the given ID is registered (enabled or not).
func (e *Engine) HasModel(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.models[id]
	return ok
}

// UnregisterModel removes a model by ID so it is no longer eligible for routing.
func (e *Engine) UnregisterModel(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.models, id)
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

// EstimateTokens estimates the token count for a request (chars/4 heuristic).
// If EstimatedInputTokens is set on the request, that value is returned directly.
func EstimateTokens(req Request) int {
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
	skip := func(providerID, reason string) {
		if e.skipRecorder != nil {
			e.skipRecorder.RecordProviderSkip(providerID, reason)
		}
	}

	outTok := estOutTokens(p)

	var eligible []Model
	for _, m := range e.models {
		if !m.Enabled {
			skip(m.ProviderID, "disabled")
			continue
		}
		if p.MinWeight > 0 && m.Weight < p.MinWeight {
			skip(m.ProviderID, "weight_below_minimum")
			continue
		}
		// Reserve 15% headroom for context estimation.
		contextWithHeadroom := int(float64(tokensNeeded) * 1.15)
		if m.MaxContextTokens > 0 && contextWithHeadroom > 0 && contextWithHeadroom > m.MaxContextTokens {
			skip(m.ProviderID, "context_overflow")
			continue
		}
		if _, ok := e.adapters[m.ProviderID]; !ok {
			skip(m.ProviderID, "no_adapter")
			continue // skip models without a registered adapter
		}
		if e.health != nil && !e.health.IsAvailable(m.ProviderID) {
			skip(m.ProviderID, "health_down")
			continue // skip providers in cooldown
		}
		if p.MaxBudgetUSD > 0 {
			est := estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K)
			if est > p.MaxBudgetUSD {
				skip(m.ProviderID, "budget_exceeded")
				continue
			}
		}
		eligible = append(eligible, m)
	}

	// When Thompson Sampling is enabled and mode is "thompson", use
	// probabilistic selection instead of deterministic scoring.
	if p.Mode == "thompson" && e.bandit != nil {
		bucket := TokenBucketLabel(tokensNeeded)
		ids := make([]string, len(eligible))
		for i, m := range eligible {
			ids[i] = m.ID
		}
		ranked := e.bandit.Sample(ids, bucket)
		idxMap := make(map[string]int, len(ranked))
		for i, id := range ranked {
			idxMap[id] = i
		}
		sort.Slice(eligible, func(i, j int) bool {
			return idxMap[eligible[i].ID] < idxMap[eligible[j].ID]
		})
		return eligible
	}

	// Sort by multi-objective score (lower is better).
	scores := e.scoreModels(eligible, tokensNeeded, outTok, p.Mode)
	sort.Slice(eligible, func(i, j int) bool {
		return scores[eligible[i].ID] < scores[eligible[j].ID]
	})
	return eligible
}

// applyHintBoost boosts the hinted model's weight by +3 (capped at 10), re-scores,
// and re-sorts eligible in place. Returns true if the hint model was found.
func (e *Engine) applyHintBoost(eligible []Model, hintID string, tokensNeeded, outTokens int, mode string) bool {
	for i := range eligible {
		if eligible[i].ID == hintID {
			eligible[i].Weight = min(10, eligible[i].Weight+3)
			scores := e.scoreModels(eligible, tokensNeeded, outTokens, mode)
			sort.Slice(eligible, func(a, b int) bool {
				return scores[eligible[a].ID] < scores[eligible[b].ID]
			})
			return true
		}
	}
	return false
}

// scoreModels computes a multi-objective score for each eligible model.
// Lower score = better model for the given routing mode.
func (e *Engine) scoreModels(models []Model, tokensNeeded, outTokens int, mode string) map[string]float64 {
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
		cost := estimateCostUSD(tokensNeeded, outTokens, m.InputPer1K, m.OutputPer1K)
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
		cost := estimateCostUSD(tokensNeeded, outTokens, m.InputPer1K, m.OutputPer1K)
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

// SelectModel performs pure model selection (eligible models + scoring) without
// making any provider calls. Returns the top pick Decision and ranked fallback list.
func (e *Engine) SelectModel(ctx context.Context, req Request, p Policy) (Decision, []Model, error) {
	if p.Mode == "" {
		p.Mode = e.cfg.DefaultMode
	}
	if p.MaxBudgetUSD == 0 {
		p.MaxBudgetUSD = e.cfg.DefaultMaxBudgetUSD
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	tokensNeeded := EstimateTokens(req)

	eligible := e.eligibleModels(tokensNeeded, p)
	if len(eligible) == 0 {
		return Decision{}, nil, errors.New("no eligible models registered")
	}

	outTok := estOutTokens(p)

	// If a model hint is provided and matches an eligible model exactly,
	// route deterministically to that model — skip softmax entirely.
	// This ensures "give me Claude" actually gives Claude, not a probabilistic
	// chance of Claude. Softmax/exploration is for load-balancing, not overriding
	// explicit model requests.
	if req.ModelHint != "" {
		for _, m := range eligible {
			if m.ID == req.ModelHint {
				estCost := estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K)
				return Decision{
					ModelID:          m.ID,
					ProviderID:       m.ProviderID,
					EstimatedCostUSD: estCost,
					Reason:           "routed-hint-exact",
				}, eligible, nil
			}
		}
	}

	// No exact hint match — fall back to weighted softmax selection.
	e.applyHintBoost(eligible, req.ModelHint, tokensNeeded, outTok, p.Mode)
	selected := softmaxSelect(eligible, e.cfg.ExplorationTemp)
	estCost := estimateCostUSD(tokensNeeded, outTok, selected.InputPer1K, selected.OutputPer1K)
	reason := fmt.Sprintf("routed-weight-%d", selected.Weight)
	return Decision{
		ModelID:          selected.ID,
		ProviderID:       selected.ProviderID,
		EstimatedCostUSD: estCost,
		Reason:           reason,
	}, eligible, nil
}

// softmaxSelect picks a model from the pre-sorted eligible list using
// softmax-weighted random selection. Temperature controls exploration:
//
//	0.0 => always pick the top model (greedy)
//	0.5 => moderate exploration across top candidates
//	1.0 => strong exploration (nearly uniform among close scores)
//
// Models are already sorted best-first by eligibleModels, so their index
// position serves as a proxy for relative score difference.
func softmaxSelect(models []Model, temperature float64) Model {
	if len(models) == 1 || temperature <= 0 {
		return models[0]
	}
	weights := make([]float64, len(models))
	for i := range models {
		weights[i] = math.Exp(-float64(i) / temperature)
	}
	var sum float64
	for _, w := range weights {
		sum += w
	}
	r := rand.Float64() * sum
	var cumulative float64
	for i, w := range weights {
		cumulative += w
		if r <= cumulative {
			return models[i]
		}
	}
	return models[len(models)-1]
}

// GetAdapter returns the registered provider adapter for the given provider ID.
func (e *Engine) GetAdapter(providerID string) Sender {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.adapters[providerID]
}

// AdapterInfo holds metadata about a registered adapter for the admin UI.
type AdapterInfo struct {
	ID             string `json:"id"`
	HealthEndpoint string `json:"health_endpoint,omitempty"`
}

// ListAdapterInfo returns metadata for all registered adapters.
func (e *Engine) ListAdapterInfo() []AdapterInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	infos := make([]AdapterInfo, 0, len(e.adapters))
	for id, a := range e.adapters {
		info := AdapterInfo{ID: id}
		if d, ok := a.(Describer); ok {
			info.HealthEndpoint = d.HealthEndpoint()
		}
		infos = append(infos, info)
	}
	return infos
}

// GetModel returns a registered model by ID.
func (e *Engine) GetModel(modelID string) (Model, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	m, ok := e.models[modelID]
	return m, ok
}

// GetAnthropicSender returns an AnthropicRawSender for the model's provider,
// or the first available one if the model isn't found. Returns nil when no
// Anthropic-capable adapter is registered.
func (e *Engine) GetAnthropicSender(modelHint string) AnthropicRawSender {
	s, _ := e.GetAnthropicSenderAndModel(modelHint)
	return s
}

// GetAnthropicSenderAndModel returns the sender and the resolved upstream model
// ID that should be used in the forwarded request body. The resolved ID may
// differ from the hint: e.g. hint "claude-sonnet-4-6" resolves to the
// registered "azure/anthropic/claude-sonnet-4-6" so NVIDIA NIM accepts it.
func (e *Engine) GetAnthropicSenderAndModel(modelHint string) (AnthropicRawSender, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Prefer the adapter for the hinted model's provider (exact match).
	if m, ok := e.models[modelHint]; ok {
		if a, ok := e.adapters[m.ProviderID]; ok {
			if ars, ok := a.(AnthropicRawSender); ok {
				return ars, modelHint
			}
		}
	}
	// Suffix match: find a model whose ID ends with /<hint>.
	// Allows "claude-sonnet-4-6" to resolve to "azure/anthropic/claude-sonnet-4-6".
	suffix := "/" + modelHint
	for id, m := range e.models {
		if !strings.HasSuffix(id, suffix) {
			continue
		}
		if a, ok := e.adapters[m.ProviderID]; ok {
			if ars, ok := a.(AnthropicRawSender); ok {
				return ars, id
			}
		}
	}
	// Fallback: first adapter that implements AnthropicRawSender.
	for _, a := range e.adapters {
		if ars, ok := a.(AnthropicRawSender); ok {
			return ars, modelHint
		}
	}
	return nil, ""
}

// FindLargerContextModel finds the smallest model with context larger than needed.
// Exported for use by Temporal activities.
func (e *Engine) FindLargerContextModel(current Model, tokensNeeded int) *Model {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.findLargerContextModel(current, tokensNeeded)
}

// shrinkMaxTokens returns a copy of req with the "max_tokens" parameter reduced
// so that input_tokens + max_tokens fits within the model's context window.
// A 256-token safety buffer is reserved. Returns the modified request and true
// when a meaningful output budget (≥64 tokens) remains after shrinking.
// Returns req unchanged and false when the model's context window is unknown or
// already fits, so callers can check the bool before retrying.
func shrinkMaxTokens(req Request, m Model, inputTokens int) (Request, bool) {
	if m.MaxContextTokens <= 0 {
		return req, false
	}
	available := m.MaxContextTokens - inputTokens - 256
	if available < 64 {
		// Context so full there is no meaningful output budget even after shrinking.
		return req, false
	}
	// Skip if the current max_tokens already fits within the available budget.
	if req.Parameters != nil {
		if cur, ok := req.Parameters["max_tokens"]; ok {
			var curInt int
			switch v := cur.(type) {
			case int:
				curInt = v
			case float64:
				curInt = int(v)
			case int64:
				curInt = int(v)
			}
			if curInt > 0 && curInt <= available {
				return req, false
			}
		}
	}
	newParams := make(map[string]any, len(req.Parameters)+1)
	for k, v := range req.Parameters {
		newParams[k] = v
	}
	newParams["max_tokens"] = available
	return Request{
		ID:                   req.ID,
		Messages:             req.Messages,
		ModelHint:            req.ModelHint,
		EstimatedInputTokens: req.EstimatedInputTokens,
		Meta:                 req.Meta,
		OutputSchema:         req.OutputSchema,
		Parameters:           newParams,
		Stream:               req.Stream,
	}, true
}

// hedgeResult is the outcome of a single hedged provider attempt.
type hedgeResult struct {
	model      Model
	resp       ProviderResponse
	err        error
	latMs      float64
	classified *ClassifiedError // non-nil when err != nil
}

// hedgedSend dispatches req to models with staggered concurrency.
//
// The first model fires immediately; each subsequent model is launched after
// hedgeAfterMs milliseconds unless a success has already been received. When
// hedgeAfterMs is 0 all providers launch in parallel immediately.
//
// The first successful response wins; remaining in-flight requests are cancelled
// via context cancellation. maxHedge caps the number of concurrent attempts
// (0 = use all models in the list).
//
// ErrBudgetExceeded responses are handled inline: the offending model is
// disabled in the engine registry immediately so it is not selected again.
func (e *Engine) hedgedSend(
	ctx context.Context,
	models []Model,
	adapters map[string]Sender,
	req Request,
	perProviderMs int,
	hedgeAfterMs int,
	maxHedge int,
) (Model, ProviderResponse, float64, error) {
	if maxHedge <= 0 || maxHedge > len(models) {
		maxHedge = len(models)
	}
	n := maxHedge

	hedgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// resultCh is buffered to n so goroutines can always send without blocking,
	// even after we have returned (preventing goroutine leaks).
	resultCh := make(chan hedgeResult, n)

	for i := 0; i < n; i++ {
		m := models[i]
		adapter, ok := adapters[m.ProviderID]
		if !ok {
			resultCh <- hedgeResult{model: m, err: fmt.Errorf("no adapter for provider %s", m.ProviderID)}
			continue
		}
		delay := time.Duration(i) * time.Duration(hedgeAfterMs) * time.Millisecond
		go func(m Model, adapter Sender, delay time.Duration) {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-hedgeCtx.Done():
					resultCh <- hedgeResult{model: m, err: hedgeCtx.Err()}
					return
				}
			}
			sendCtx := hedgeCtx
			var sendCancel context.CancelFunc
			if perProviderMs > 0 {
				sendCtx, sendCancel = context.WithTimeout(hedgeCtx, time.Duration(perProviderMs)*time.Millisecond)
			}
			start := time.Now()
			resp, err := adapter.Send(sendCtx, m.ID, req)
			latMs := float64(time.Since(start).Milliseconds())
			if sendCancel != nil {
				sendCancel()
			}
			var classified *ClassifiedError
			if err != nil {
				classified = adapter.ClassifyError(err)
			}
			resultCh <- hedgeResult{model: m, resp: resp, err: err, latMs: latMs, classified: classified}
		}(m, adapter, delay)
	}

	received := 0
	var lastErr error
	for received < n {
		select {
		case <-hedgeCtx.Done():
			return Model{}, nil, 0, hedgeCtx.Err()
		case result := <-resultCh:
			received++
			if result.err == nil {
				cancel() // stop all remaining in-flight requests
				// Drain remaining results in the background to let goroutines exit.
				go func(remaining int) {
					for i := 0; i < remaining; i++ {
						<-resultCh
					}
				}(n - received)
				return result.model, result.resp, result.latMs, nil
			}
			// Disable the model immediately when its budget is exhausted.
			if result.classified != nil && result.classified.Class == ErrBudgetExceeded {
				slog.Warn("provider budget exhausted (hedged), disabling model",
					slog.String("provider", result.model.ProviderID),
					slog.String("model", result.model.ID),
				)
				e.mu.Lock()
				if mod, ok := e.models[result.model.ID]; ok {
					mod.Enabled = false
					e.models[result.model.ID] = mod
				}
				e.mu.Unlock()
			}
			lastErr = result.err
			slog.Warn("hedged provider failed",
				slog.String("provider", result.model.ProviderID),
				slog.String("model", result.model.ID),
				slog.String("error", result.err.Error()),
			)
		}
	}
	return Model{}, nil, 0, lastErr
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

	// Snapshot eligible models, adapters, and the full model set under the lock,
	// then release the lock before any network calls. This prevents RegisterModel
	// and RegisterAdapter write operations from blocking for the full provider round-trip.
	e.mu.RLock()
	tokensNeeded := EstimateTokens(req)
	eligible := e.eligibleModels(tokensNeeded, p)

	if len(eligible) == 0 {
		// All models were excluded by normal filters (health cooldown, budget, etc.).
		// Attempt a last-resort pass over every enabled model that has an adapter
		// before surfacing an error to the client. The health tracker is a routing
		// preference, not a hard constraint; clients should never see a 502 simply
		// because an internal cooldown window is active.
		for _, m := range e.models {
			if m.Enabled {
				if _, ok := e.adapters[m.ProviderID]; ok {
					eligible = append(eligible, m)
				}
			}
		}
		if len(eligible) == 0 {
			e.mu.RUnlock()
			return Decision{}, nil, errors.New("no eligible models registered")
		}
		slog.Warn("primary routing found no eligible models; attempting last-resort routing over all providers",
			slog.Int("candidates", len(eligible)),
		)
	}

	outTok := estOutTokens(p)

	// Deterministic hint routing: if the requested model is in the eligible set,
	// pin to it and skip softmax. Exact match takes priority over load-balancing.
	hintModelID := ""
	if req.ModelHint != "" {
		for i, m := range eligible {
			if m.ID == req.ModelHint {
				hintModelID = req.ModelHint
				// Move hinted model to front so it's selected first in sequential passes.
				eligible[0], eligible[i] = eligible[i], eligible[0]
				break
			}
		}
	}
	if hintModelID == "" {
		// No exact match — fall back to probabilistic hint boost.
		e.applyHintBoost(eligible, req.ModelHint, tokensNeeded, outTok, p.Mode)
	}
	// Snapshot adapters for all eligible providers plus the full model map for escalation.
	adapters := make(map[string]Sender, len(eligible))
	for _, m := range eligible {
		if a, ok := e.adapters[m.ProviderID]; ok {
			adapters[m.ProviderID] = a
		}
	}
	modelSnap := make(map[string]Model, len(e.models))
	for id, m := range e.models {
		modelSnap[id] = m
		if a, ok := e.adapters[m.ProviderID]; ok {
			adapters[m.ProviderID] = a // also include escalation targets
		}
	}
	e.mu.RUnlock()

	// Track which model IDs were attempted in the primary pass so the fallback
	// pass can skip them and only probe untried candidates.
	tried := make(map[string]bool, len(eligible))

	// When hedging is enabled, dispatch the primary eligible set in parallel
	// with staggered launch times. The first successful response wins.
	// Exception: if we have an exact model hint match, bypass hedging entirely —
	// we must not race the hinted model against local vLLM providers that respond faster.
	if e.cfg.HedgeAfterMs > 0 && hintModelID == "" {
		maxHedge := e.cfg.MaxHedgedProviders
		if maxHedge <= 0 {
			maxHedge = 3
		}
		hedgeN := len(eligible)
		if hedgeN > maxHedge {
			hedgeN = maxHedge
		}
		for _, m := range eligible[:hedgeN] {
			tried[m.ID] = true
		}
		selectedModel, resp, latMs, err := e.hedgedSend(
			ctx, eligible, adapters, req,
			e.cfg.PerProviderTimeoutMs, e.cfg.HedgeAfterMs, maxHedge,
		)
		if err == nil {
			if e.health != nil {
				e.health.RecordSuccess(selectedModel.ProviderID, latMs)
			}
			reason := "hedged-dispatch"
			if hintModelID != "" && selectedModel.ID == hintModelID {
				reason = "hedged-hint-boost"
			}
			return Decision{
				ModelID:          selectedModel.ID,
				ProviderID:       selectedModel.ProviderID,
				EstimatedCostUSD: estimateCostUSD(tokensNeeded, outTok, selectedModel.InputPer1K, selectedModel.OutputPer1K),
				Reason:           reason,
			}, resp, nil
		}
		slog.Warn("hedged dispatch exhausted all candidates; falling through to last-resort",
			slog.Int("candidates", hedgeN),
			slog.String("last_error", err.Error()),
		)
		// Fall through to the last-resort pass below.
	} else {
		// Sequential fallback: try each eligible model in weight order with full
		// per-error-class handling (context shrink, rate-limit skip, etc.).
		for i, m := range eligible {
			tried[m.ID] = true
			adapter := adapters[m.ProviderID]
			estCost := estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K)

			slog.Info("routing request",
				slog.String("provider", m.ProviderID),
				slog.String("model", m.ID),
				slog.Int("attempt", i+1),
				slog.Int("total", len(eligible)),
			)

			sendCtx := ctx
			var sendCancel context.CancelFunc
			if e.cfg.PerProviderTimeoutMs > 0 {
				sendCtx, sendCancel = context.WithTimeout(ctx, time.Duration(e.cfg.PerProviderTimeoutMs)*time.Millisecond)
			}

			sendStart := time.Now()
			resp, err := adapter.Send(sendCtx, m.ID, req)
			sendMs := float64(time.Since(sendStart).Milliseconds())
			if sendCancel != nil {
				sendCancel()
			}

			if err == nil {
				if e.health != nil {
					e.health.RecordSuccess(m.ProviderID, sendMs)
				}
				reason := fmt.Sprintf("routed-weight-%d", m.Weight)
				if hintModelID != "" && m.ID == hintModelID {
					reason = "routed-hint-boost"
				}
				return Decision{
					ModelID:          m.ID,
					ProviderID:       m.ProviderID,
					EstimatedCostUSD: estCost,
					Reason:           reason,
				}, resp, nil
			}

			if e.health != nil {
				e.health.RecordError(m.ProviderID, err.Error())
			}

			classified := adapter.ClassifyError(err)
			slog.Warn("provider failed",
				slog.String("provider", m.ProviderID),
				slog.String("model", m.ID),
				slog.String("error", err.Error()),
				slog.String("class", string(classified.Class)),
			)

			switch classified.Class {
			case ErrContextOverflow:
				// Step 1: reduce max_tokens so the request fits the current model's
				// context window. This is cheaper than escalating to a larger model and
				// covers the common vLLM case: "max_tokens too large: N > window - input".
				if reducedReq, ok := shrinkMaxTokens(req, m, tokensNeeded); ok {
					retryCtx := ctx
					var retryCancel context.CancelFunc
					if e.cfg.PerProviderTimeoutMs > 0 {
						retryCtx, retryCancel = context.WithTimeout(ctx, time.Duration(e.cfg.PerProviderTimeoutMs)*time.Millisecond)
					}
					retryStart := time.Now()
					resp2, err2 := adapter.Send(retryCtx, m.ID, reducedReq)
					retryMs := float64(time.Since(retryStart).Milliseconds())
					if retryCancel != nil {
						retryCancel()
					}
					if err2 == nil {
						if e.health != nil {
							e.health.RecordSuccess(m.ProviderID, retryMs)
						}
						slog.Info("context overflow resolved by reducing max_tokens",
							slog.String("provider", m.ProviderID),
							slog.String("model", m.ID),
							slog.Int("available_tokens", m.MaxContextTokens-tokensNeeded-256),
						)
						return Decision{
							ModelID:          m.ID,
							ProviderID:       m.ProviderID,
							EstimatedCostUSD: estCost,
							Reason:           "reduced-max-tokens",
						}, resp2, nil
					}
				}
				// Step 2: escalate to a model with a larger context window.
				larger := findLargerContextModelIn(modelSnap, adapters, m, tokensNeeded*2)
				if larger != nil {
					slog.Info("escalating on context overflow",
						slog.String("target_model", larger.ID),
						slog.Int("context_tokens", larger.MaxContextTokens),
					)
					a2 := adapters[larger.ProviderID]
					resp2, err2 := a2.Send(ctx, larger.ID, req)
					if err2 == nil {
						return Decision{
							ModelID:          larger.ID,
							ProviderID:       larger.ProviderID,
							EstimatedCostUSD: estimateCostUSD(tokensNeeded, outTok, larger.InputPer1K, larger.OutputPer1K),
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
				// Retry with exponential backoff + jitter on the same provider.
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

			case ErrBudgetExceeded:
				// The provider's spending budget is permanently exhausted. Disable the
				// model immediately so it is not selected again until manually re-enabled
				// via the admin API. This prevents wasteful round-trips on every request.
				slog.Warn("provider budget exhausted, disabling model",
					slog.String("provider", m.ProviderID),
					slog.String("model", m.ID),
				)
				e.mu.Lock()
				if mod, ok := e.models[m.ID]; ok {
					mod.Enabled = false
					e.models[m.ID] = mod
				}
				e.mu.Unlock()
				continue

			case ErrFatal:
				// Don't retry fatals, try next model.
				continue
			}
		}
	} // end sequential-fallback else block

	// Primary pass exhausted all eligible models. As a last resort, probe any
	// enabled model that was excluded by the primary filter (e.g., health
	// cooldown, budget, MinWeight) but has not been tried yet. The client must
	// not receive a 502 due to internal routing preferences — only surface an
	// error when there is genuinely no provider that can serve the request.
	e.mu.RLock()
	var lastResort []Model
	var lrAdapters map[string]Sender
	for _, m := range e.models {
		if !tried[m.ID] && m.Enabled {
			if a, ok := e.adapters[m.ProviderID]; ok {
				lastResort = append(lastResort, m)
				if lrAdapters == nil {
					lrAdapters = make(map[string]Sender)
				}
				lrAdapters[m.ProviderID] = a
			}
		}
	}
	e.mu.RUnlock()

	for _, m := range lastResort {
		adapter := lrAdapters[m.ProviderID]
		slog.Warn("last-resort routing: probing health-excluded provider",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
		)

		// Use a fresh context for the last-resort pass — the original ctx may
		// have already expired (e.g., a slow provider consumed the whole budget).
		// Always cap each attempt so we never block indefinitely and violate the
		// upstream TCP timeout window. Use PerProviderTimeoutMs when set, otherwise
		// fall back to a 60 s hard minimum.
		lrTimeoutMs := e.cfg.PerProviderTimeoutMs
		if lrTimeoutMs <= 0 {
			lrTimeoutMs = 60_000
		}
		lrCtx, lrCancel := context.WithTimeout(context.Background(), time.Duration(lrTimeoutMs)*time.Millisecond)

		sendStart := time.Now()
		resp, err := adapter.Send(lrCtx, m.ID, req)
		sendMs := float64(time.Since(sendStart).Milliseconds())
		lrCancel()

		if err == nil {
			if e.health != nil {
				e.health.RecordSuccess(m.ProviderID, sendMs)
			}
			return Decision{
				ModelID:          m.ID,
				ProviderID:       m.ProviderID,
				EstimatedCostUSD: estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K),
				Reason:           "last-resort-fallback",
			}, resp, nil
		}
		if e.health != nil {
			e.health.RecordError(m.ProviderID, err.Error())
		}
		slog.Warn("last-resort provider also failed",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
			slog.String("error", err.Error()),
		)
	}

	return Decision{}, nil, errors.New("all providers failed")
}

// findLargerContextModelIn finds the smallest model with context larger than needed
// from a pre-snapshotted model map and adapter map.
func findLargerContextModelIn(models map[string]Model, adapters map[string]Sender, current Model, tokensNeeded int) *Model {
	var best *Model
	for _, m := range models {
		if !m.Enabled || m.ID == current.ID {
			continue
		}
		if _, ok := adapters[m.ProviderID]; !ok {
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

// RouteAndStream selects a model and opens a streaming connection.
// Returns the decision, the SSE stream body, and any error.
//
// When all policy-eligible models fail (or none are eligible due to health
// cooldown), a last-resort pass tries every remaining enabled model that has
// an adapter. Clients are never shown a 502 due to internal routing preferences.
func (e *Engine) RouteAndStream(ctx context.Context, req Request, p Policy) (Decision, io.ReadCloser, error) {
	decision, eligible, err := e.SelectModel(ctx, req, p)
	if err != nil {
		// SelectModel failed because eligibleModels returned empty. Attempt
		// last-resort routing over all enabled models before surfacing the error.
		e.mu.RLock()
		var candidates []Model
		var lrAdapters map[string]Sender
		tokensNeeded := EstimateTokens(req)
		for _, m := range e.models {
			if m.Enabled {
				if a, ok := e.adapters[m.ProviderID]; ok {
					candidates = append(candidates, m)
					if lrAdapters == nil {
						lrAdapters = make(map[string]Sender)
					}
					lrAdapters[m.ProviderID] = a
				}
			}
		}
		outTok := estOutTokens(p)
		e.mu.RUnlock()

		if len(candidates) > 0 {
			slog.Warn("stream: no eligible models from primary filter; attempting last-resort routing",
				slog.Int("candidates", len(candidates)),
			)
			for _, m := range candidates {
				a := lrAdapters[m.ProviderID]
				fs, ok := a.(StreamSender)
				if !ok {
					continue
				}
				body, serr := fs.SendStream(ctx, m.ID, req)
				if serr == nil {
					if e.health != nil {
						e.health.RecordSuccess(m.ProviderID, 0)
					}
					return Decision{
						ModelID:          m.ID,
						ProviderID:       m.ProviderID,
						EstimatedCostUSD: estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K),
						Reason:           "last-resort-fallback",
					}, body, nil
				}
				if e.health != nil {
					e.health.RecordError(m.ProviderID, serr.Error())
				}
				if fc := a.ClassifyError(serr); fc.Class == ErrBudgetExceeded {
					e.mu.Lock()
					if mod, ok := e.models[m.ID]; ok {
						mod.Enabled = false
						e.models[m.ID] = mod
					}
					e.mu.Unlock()
				}
			}
		}
		return Decision{}, nil, err
	}

	// Build set of tried model IDs for the fallback pass below.
	tried := map[string]bool{decision.ModelID: true}

	e.mu.RLock()
	adapter := e.adapters[decision.ProviderID]
	e.mu.RUnlock()

	streamer, ok := adapter.(StreamSender)
	if !ok {
		return Decision{}, nil, fmt.Errorf("provider %s does not support streaming", decision.ProviderID)
	}

	body, err := streamer.SendStream(ctx, decision.ModelID, req)
	if err == nil {
		return decision, body, nil
	}

	// Primary model failed. Handle budget exhaustion then try eligible fallbacks.
	if classified := adapter.ClassifyError(err); classified.Class == ErrBudgetExceeded {
		slog.Warn("provider budget exhausted (stream), disabling model",
			slog.String("provider", decision.ProviderID),
			slog.String("model", decision.ModelID),
		)
		e.mu.Lock()
		if mod, ok := e.models[decision.ModelID]; ok {
			mod.Enabled = false
			e.models[decision.ModelID] = mod
		}
		e.mu.Unlock()
	}

	// Try remaining eligible models (those returned by SelectModel).
	for _, m := range eligible[1:] {
		tried[m.ID] = true
		e.mu.RLock()
		fallbackAdapter := e.adapters[m.ProviderID]
		e.mu.RUnlock()
		fs, ok := fallbackAdapter.(StreamSender)
		if !ok {
			continue
		}
		body, err = fs.SendStream(ctx, m.ID, req)
		if err == nil {
			decision.ModelID = m.ID
			decision.ProviderID = m.ProviderID
			decision.Reason = "stream-fallback"
			return decision, body, nil
		}
		if fc := fallbackAdapter.ClassifyError(err); fc.Class == ErrBudgetExceeded {
			slog.Warn("provider budget exhausted (stream fallback), disabling model",
				slog.String("provider", m.ProviderID),
				slog.String("model", m.ID),
			)
			e.mu.Lock()
			if mod, ok := e.models[m.ID]; ok {
				mod.Enabled = false
				e.models[m.ID] = mod
			}
			e.mu.Unlock()
		}
	}

	// All eligible models failed. Last-resort pass: try any enabled model not
	// yet attempted (e.g., health-cooldown providers, budget-excluded models).
	e.mu.RLock()
	var lastResort []Model
	var lrAdapters map[string]Sender
	tokensNeeded := EstimateTokens(req)
	outTok := estOutTokens(p)
	for _, m := range e.models {
		if !tried[m.ID] && m.Enabled {
			if a, ok := e.adapters[m.ProviderID]; ok {
				lastResort = append(lastResort, m)
				if lrAdapters == nil {
					lrAdapters = make(map[string]Sender)
				}
				lrAdapters[m.ProviderID] = a
			}
		}
	}
	e.mu.RUnlock()

	for _, m := range lastResort {
		a := lrAdapters[m.ProviderID]
		fs, ok := a.(StreamSender)
		if !ok {
			continue
		}
		slog.Warn("stream: last-resort routing: probing health-excluded provider",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
		)
		body, serr := fs.SendStream(ctx, m.ID, req)
		if serr == nil {
			if e.health != nil {
				e.health.RecordSuccess(m.ProviderID, 0)
			}
			return Decision{
				ModelID:          m.ID,
				ProviderID:       m.ProviderID,
				EstimatedCostUSD: estimateCostUSD(tokensNeeded, outTok, m.InputPer1K, m.OutputPer1K),
				Reason:           "last-resort-fallback",
			}, body, nil
		}
		if e.health != nil {
			e.health.RecordError(m.ProviderID, serr.Error())
		}
		slog.Warn("stream: last-resort provider also failed",
			slog.String("provider", m.ProviderID),
			slog.String("model", m.ID),
			slog.String("error", serr.Error()),
		)
	}

	return Decision{}, nil, fmt.Errorf("all providers failed for streaming: %w", err)
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

// MessagesContent concatenates all user message content into a single string.
func MessagesContent(msgs []Message) string {
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

// ExtractContent tries to pull the text content from a provider response JSON.
// It supports OpenAI, vLLM reasoning-model, and Anthropic response formats,
// falling back to raw string.
//
// For vLLM reasoning models (e.g. Nemotron), the response includes both
// reasoning_content and content fields. Both are returned concatenated so
// that orchestration chains (adversarial, vote, refine) operate on the full
// model output. Callers that want to separate reasoning from output should
// use ExtractContentParts instead.
func ExtractContent(resp ProviderResponse) string {
	content, _ := ExtractContentParts(resp)
	return content
}

// ExtractContentParts returns (fullContent, reasoningContent) from a provider
// response. fullContent is always non-empty when the response is valid;
// reasoningContent is only set for vLLM reasoning models that emit
// reasoning_content alongside content.
//
// For non-reasoning responses, reasoningContent is empty and fullContent is
// the assistant message content as usual.
func ExtractContentParts(resp ProviderResponse) (fullContent, reasoningContent string) {
	// Try vLLM / OpenAI format with optional reasoning_content field.
	var oai struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(resp, &oai) == nil && len(oai.Choices) > 0 {
		content := oai.Choices[0].Message.Content
		reasoning := oai.Choices[0].Message.ReasoningContent
		if reasoning != "" {
			// Return reasoning prepended to content so orchestration chains see
			// the full chain-of-thought; callers can split on the separator if needed.
			return reasoning + "\n\n" + content, reasoning
		}
		return content, ""
	}
	// Try Anthropic format.
	var ant struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(resp, &ant) == nil && len(ant.Content) > 0 {
		return ant.Content[0].Text, ""
	}
	return string(resp), ""
}

func estimateCostUSD(inTokens, outTokens int, inPer1k, outPer1k float64) float64 {
	return (float64(inTokens)/1000.0)*inPer1k + (float64(outTokens)/1000.0)*outPer1k
}

// estOutTokens returns the output-token estimate from the policy, defaulting to 512.
func estOutTokens(p Policy) int {
	if p.EstimatedOutputTokens > 0 {
		return p.EstimatedOutputTokens
	}
	return 512
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
