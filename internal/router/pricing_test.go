package router

import (
	"context"
	"testing"
)

type testCostEstimator struct {
	totals map[string]float64
}

func (e testCostEstimator) EstimateCost(_, modelID string, _ TokenUsage) (CostEstimate, bool) {
	total, ok := e.totals[modelID]
	if !ok {
		return CostEstimate{}, false
	}
	return CostEstimate{Total: total, Currency: "USD", Source: "model_pricing", PricingSource: "test"}, true
}

func (e testCostEstimator) CostEstimatorStatus() CostEstimatorStatus {
	return CostEstimatorStatus{Enabled: true, Type: "test", Entries: len(e.totals)}
}

func TestCostEstimatorDrivesBudgetEligibility(t *testing.T) {
	eng := NewEngine(EngineConfig{DefaultMode: "cheap", DefaultMaxBudgetUSD: 1, ExplorationTemp: 0})
	eng.RegisterAdapter(newMockSender("p"))
	eng.RegisterModel(Model{ID: "catalog-cheap", ProviderID: "p", Weight: 5, Enabled: true, InputPer1K: 100, OutputPer1K: 100})
	eng.RegisterModel(Model{ID: "catalog-expensive", ProviderID: "p", Weight: 5, Enabled: true})
	eng.SetCostEstimator(testCostEstimator{totals: map[string]float64{
		"catalog-cheap":     0.001,
		"catalog-expensive": 0.1,
	}})

	decision, _, err := eng.SelectModel(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, Policy{Mode: "cheap", MaxBudgetUSD: 0.01})
	if err != nil {
		t.Fatalf("SelectModel() error = %v", err)
	}
	if decision.ModelID != "catalog-cheap" {
		t.Fatalf("selected %q, want catalog-cheap", decision.ModelID)
	}
	if decision.EstimatedCostUSD != 0.001 {
		t.Fatalf("cost = %f, want 0.001", decision.EstimatedCostUSD)
	}
	if status := eng.CostEstimatorStatus(); !status.Enabled || status.Type != "test" || status.Entries != 2 {
		t.Fatalf("unexpected estimator status: %+v", status)
	}
}

func TestCostEstimatorFallsBackToFlatModelPricing(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	eng.RegisterModel(Model{ID: "flat", ProviderID: "p", InputPer1K: 0.01, OutputPer1K: 0.03, PricingSource: "manual"})
	eng.SetCostEstimator(testCostEstimator{totals: map[string]float64{}})

	estimate, ok := eng.EstimateModelCost("flat", TokenUsage{PromptTokens: 1000, CompletionTokens: 500})
	if !ok {
		t.Fatal("expected flat pricing estimate")
	}
	if estimate.Total != 0.025 || estimate.PricingSource != "manual" {
		t.Fatalf("unexpected estimate: %+v", estimate)
	}
}
