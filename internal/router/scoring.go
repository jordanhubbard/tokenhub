package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

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

// SelectModel performs pure model selection (eligible models + scoring) without
// making any provider calls. Returns the top pick Decision and ranked fallback list.
func (e *Engine) SelectModel(ctx context.Context, req Request, p Policy) (Decision, []Model, error) {
	if p.Mode == "" {
		p.Mode = e.cfg.DefaultMode
	}
	if p.MaxBudgetUSD == 0 {
		p.MaxBudgetUSD = e.cfg.DefaultMaxBudgetUSD
	}

	// Blind A/B: rewrite ModelHint through the alias resolver (if any) before
	// eligibility runs so the selected model is the resolved variant.
	aliasFrom := e.resolveAlias(&req)

	e.mu.RLock()
	defer e.mu.RUnlock()

	tokensNeeded := EstimateTokens(req)

	eligible := e.eligibleModels(tokensNeeded, p)
	if aliasFrom == WildcardModelHint {
		eligible = filterModelsByIDSet(eligible, e.wildcardRoutingPoolLocked(req.ModelHint))
	}
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
					AliasFrom:        aliasFrom,
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
		AliasFrom:        aliasFrom,
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
