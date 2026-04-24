package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// RouteAndStream selects a model and opens a streaming connection.
// Returns the decision, the SSE stream body, and any error.
//
// When all policy-eligible models fail (or none are eligible due to health
// cooldown), a last-resort pass tries every remaining enabled model that has
// an adapter. Clients are never shown a 502 due to internal routing preferences.
func (e *Engine) RouteAndStream(ctx context.Context, req Request, p Policy) (Decision, io.ReadCloser, error) {
	// Blind A/B rewrite happens here so the stream-fallback Decisions below
	// can attribute traffic to the original alias even when SelectModel never
	// got far enough to apply the rewrite itself (e.g. no eligible models,
	// then we fall through to last-resort streaming).
	aliasFrom := e.resolveAlias(&req)
	decision, eligible, err := e.SelectModel(ctx, req, p)
	if aliasFrom != "" {
		// SelectModel saw a post-rewrite ModelHint and cannot recover the
		// original alias on its own — carry it forward explicitly.
		decision.AliasFrom = aliasFrom
	}
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
						AliasFrom:        aliasFrom,
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
				AliasFrom:        aliasFrom,
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
