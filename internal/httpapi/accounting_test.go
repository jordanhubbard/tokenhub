package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/pricing/nemorelay"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/stretchr/testify/require"
)

func TestExtractUsageNormalizesCacheAndProviderCost(t *testing.T) {
	usage := extractUsage(json.RawMessage(`{
  "usage": {
    "prompt_tokens": 1000,
    "completion_tokens": 200,
    "prompt_tokens_details": {"cached_tokens": 400},
    "cache_write_tokens": 50,
    "cost": {
      "input": 0.001,
      "output": 0.002,
      "cache_read": 0.0001,
      "currency": "USD",
      "source": "provider_reported",
      "pricing_source": "relay-upstream"
    }
  }
}`))

	require.True(t, usage.HasUsage)
	require.Equal(t, 1000, usage.InputTokens)
	require.Equal(t, 200, usage.OutputTokens)
	require.Equal(t, 400, usage.CacheReadTokens)
	require.Equal(t, 50, usage.CacheWriteTokens)
	require.NotNil(t, usage.Cost)
	require.InDelta(t, 0.0031, usage.Cost.Total, 1e-12)
	require.Equal(t, "relay-upstream", usage.Cost.PricingSource)
}

func TestComputeActualCostUsesNeMoRelayCatalog(t *testing.T) {
	catalog, err := nemorelay.ParseCatalog([]byte(`{
  "version": 1,
  "entries": [{
    "provider": "openai", "model_id": "gpt-test",
    "rates": {"input_per_million": 2, "output_per_million": 4, "cache_read_per_million": 0.2},
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01", "pricing_source": "test-catalog"
  }]
}`))
	require.NoError(t, err)
	resolver, err := nemorelay.NewResolver(catalog)
	require.NoError(t, err)
	eng := router.NewEngine(router.EngineConfig{})
	eng.RegisterModel(router.Model{ID: "gpt-test", ProviderID: "openai", InputPer1K: 99, OutputPer1K: 99})
	eng.SetCostEstimator(resolver)

	estimate := computeActualCost(tokenUsage{
		InputTokens:     1000,
		OutputTokens:    200,
		CacheReadTokens: 400,
		HasUsage:        true,
	}, 9.9, eng, "gpt-test")
	require.InDelta(t, 0.00208, estimate.Total, 1e-12)
	require.Equal(t, "test-catalog", estimate.PricingSource)
}

func TestComputeActualCostPrefersProviderReportedUSD(t *testing.T) {
	eng := router.NewEngine(router.EngineConfig{})
	cost := &router.CostEstimate{Total: 0.42, Currency: "USD", Source: "provider_reported"}
	estimate := computeActualCost(tokenUsage{Cost: cost, HasUsage: true}, 9.9, eng, "missing")
	require.Equal(t, 0.42, estimate.Total)
	require.Equal(t, "provider_reported", estimate.Source)
}

func TestPricingStatusHandler(t *testing.T) {
	eng := router.NewEngine(router.EngineConfig{})
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/pricing", nil)
	w := httptest.NewRecorder()
	PricingStatusHandler(Dependencies{Engine: eng}).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":false,"entries":0}`, w.Body.String())
}
