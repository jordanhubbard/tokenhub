package router

import "strings"

// TokenUsage is the provider-neutral usage shape used for cost accounting.
// Cache reads and writes are separate because providers bill them at different
// rates and disagree on whether cache reads are included in prompt tokens.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// CostEstimate is TokenHub's normalized cost record. Its shape mirrors NeMo
// Relay's CostEstimate so provider-reported and catalog-derived costs retain
// their provenance instead of collapsing into an unexplained float.
type CostEstimate struct {
	Total           float64  `json:"total"`
	Currency        string   `json:"currency"`
	Input           *float64 `json:"input,omitempty"`
	Output          *float64 `json:"output,omitempty"`
	CacheRead       *float64 `json:"cache_read,omitempty"`
	CacheWrite      *float64 `json:"cache_write,omitempty"`
	Source          string   `json:"source"`
	PricingProvider string   `json:"pricing_provider,omitempty"`
	PricingModel    string   `json:"pricing_model,omitempty"`
	PricingAsOf     string   `json:"pricing_as_of,omitempty"`
	PricingSource   string   `json:"pricing_source,omitempty"`
}

// CostEstimator supplies provider/model-aware cost estimates. Implementations
// must be safe for concurrent use on the routing hot path.
type CostEstimator interface {
	EstimateCost(providerID, modelID string, usage TokenUsage) (CostEstimate, bool)
}

// CostEstimatorStatus is safe, non-secret diagnostic metadata exposed through
// the admin API.
type CostEstimatorStatus struct {
	Enabled bool     `json:"enabled"`
	Type    string   `json:"type,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Entries int      `json:"entries"`
}

// CostEstimatorInspector is optionally implemented by estimators that can
// describe their loaded snapshot.
type CostEstimatorInspector interface {
	CostEstimatorStatus() CostEstimatorStatus
}

type costEstimatorBox struct {
	estimator CostEstimator
}

// SetCostEstimator atomically replaces the process-wide estimator used by the
// engine. A loaded NeMo Relay catalog takes precedence over the legacy flat
// per-1K model prices; unmatched models continue to use those flat prices.
func (e *Engine) SetCostEstimator(estimator CostEstimator) {
	e.costEstimator.Store(costEstimatorBox{estimator: estimator})
}

func (e *Engine) currentCostEstimator() CostEstimator {
	value := e.costEstimator.Load()
	if value == nil {
		return nil
	}
	return value.(costEstimatorBox).estimator
}

// CostEstimatorStatus reports whether a first-class estimator is active.
func (e *Engine) CostEstimatorStatus() CostEstimatorStatus {
	estimator := e.currentCostEstimator()
	if estimator == nil {
		return CostEstimatorStatus{}
	}
	if inspector, ok := estimator.(CostEstimatorInspector); ok {
		return inspector.CostEstimatorStatus()
	}
	return CostEstimatorStatus{Enabled: true, Type: "custom"}
}

// EstimateModelCost applies the active estimator to a registered model and
// falls back to TokenHub's legacy flat per-1K prices when no catalog matches.
func (e *Engine) EstimateModelCost(modelID string, usage TokenUsage) (CostEstimate, bool) {
	m, ok := e.GetModel(modelID)
	if !ok {
		return CostEstimate{}, false
	}
	return e.estimateModelCost(m, usage)
}

func (e *Engine) estimateModelCost(m Model, usage TokenUsage) (CostEstimate, bool) {
	if estimator := e.currentCostEstimator(); estimator != nil {
		if estimate, ok := estimator.EstimateCost(m.ProviderID, m.ID, usage); ok {
			return estimate, true
		}
	}

	inputCost := (float64(usage.PromptTokens) / 1000) * m.InputPer1K
	outputCost := (float64(usage.CompletionTokens) / 1000) * m.OutputPer1K
	total := inputCost + outputCost
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return CostEstimate{}, false
	}

	source := strings.TrimSpace(m.PricingSource)
	if source == "" {
		source = "manual"
	}
	return CostEstimate{
		Total:           total,
		Currency:        "USD",
		Input:           costComponent(inputCost, usage.PromptTokens),
		Output:          costComponent(outputCost, usage.CompletionTokens),
		Source:          "model_pricing",
		PricingProvider: m.ProviderID,
		PricingModel:    m.ID,
		PricingSource:   source,
	}, true
}

func (e *Engine) estimateModelCostUSD(m Model, promptTokens, completionTokens int) float64 {
	estimate, ok := e.estimateModelCost(m, TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	})
	if !ok {
		return 0
	}
	return estimate.Total
}

func costComponent(cost float64, tokens int) *float64 {
	if tokens <= 0 {
		return nil
	}
	return &cost
}
