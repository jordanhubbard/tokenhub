package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"
)

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

// FindLargerContextModel finds the smallest model with context larger than needed.
// Exported for use by Temporal activities.
func (e *Engine) FindLargerContextModel(current Model, tokensNeeded int) *Model {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.findLargerContextModel(current, tokensNeeded)
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

	// Blind A/B: rewrite ModelHint to a variant via the alias resolver (if set).
	// The rewrite is stable per request.ID so idempotent replays / retries land
	// on the same backend. The original alias name rides in Decision.AliasFrom
	// so request logs can group outcomes for experiment analysis.
	aliasFrom := e.resolveAlias(&req)

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
				AliasFrom:        aliasFrom,
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
					AliasFrom:        aliasFrom,
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
							AliasFrom:        aliasFrom,
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
							AliasFrom:        aliasFrom,
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
						AliasFrom:        aliasFrom,
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
				AliasFrom:        aliasFrom,
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
