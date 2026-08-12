package nemorelay

import (
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/stretchr/testify/require"
)

func mustResolver(t *testing.T, catalog string) *Resolver {
	t.Helper()
	parsed, err := ParseCatalog([]byte(catalog))
	require.NoError(t, err)
	resolver, err := NewResolver(parsed)
	require.NoError(t, err)
	return resolver
}

func TestEstimateCostIncludesCacheAndProvenance(t *testing.T) {
	resolver := mustResolver(t, `{
  "version": 1,
  "entries": [{
    "provider": "anthropic",
    "model_id": "claude-sonnet",
    "aliases": ["anthropic/claude-sonnet-latest"],
    "currency": "USD",
    "unit": "per_token",
    "rates": {
      "input_per_million": 3,
      "output_per_million": 15,
      "cache_read_per_million": 0.3,
      "cache_write_per_million": 3.75
    },
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01",
    "pricing_source": "https://example.test/pricing"
  }]
}`)

	estimate, ok := resolver.EstimateCost("anthropic-prod", "anthropic/claude-sonnet-latest", router.TokenUsage{
		PromptTokens:     1_000,
		CompletionTokens: 200,
		CacheReadTokens:  400,
		CacheWriteTokens: 100,
	})
	require.True(t, ok)
	require.InDelta(t, 0.005295, estimate.Total, 1e-12)
	require.Equal(t, "model_pricing", estimate.Source)
	require.Equal(t, "anthropic", estimate.PricingProvider)
	require.Equal(t, "claude-sonnet", estimate.PricingModel)
	require.Equal(t, "2026-08-01", estimate.PricingAsOf)
	require.NotNil(t, estimate.CacheRead)
	require.NotNil(t, estimate.CacheWrite)
}

func TestEstimateCostSeparateCacheAccounting(t *testing.T) {
	resolver := mustResolver(t, `{
  "version": 1,
  "entries": [{
    "provider": "openai",
    "model_id": "gpt-test",
    "rates": {"input_per_million": 2, "output_per_million": 4, "cache_read_per_million": 0.2},
    "prompt_cache": {"read_accounting": "separate"},
    "pricing_as_of": "2026-08-01",
    "pricing_source": "test"
  }]
}`)

	estimate, ok := resolver.EstimateCost("openai", "gpt-test", router.TokenUsage{
		PromptTokens:    1_000,
		CacheReadTokens: 500,
	})
	require.True(t, ok)
	require.InDelta(t, 0.0021, estimate.Total, 1e-12)
}

func TestPromptTokenThresholdUsesFullRequestTier(t *testing.T) {
	resolver := mustResolver(t, `{
  "version": 1,
  "entries": [{
    "provider": "openai",
    "model_id": "tiered",
    "rate_schedule": {
      "type": "prompt_token_threshold",
      "applies_to": "full_request",
      "tiers": [
        {"max_prompt_tokens": 1000, "rates": {"input_per_million": 1, "output_per_million": 2}},
        {"min_prompt_tokens": 1001, "rates": {"input_per_million": 3, "output_per_million": 6}}
      ]
    },
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01",
    "pricing_source": "test"
  }]
}`)

	estimate, ok := resolver.EstimateCost("openai", "tiered", router.TokenUsage{PromptTokens: 1_001, CompletionTokens: 100})
	require.True(t, ok)
	require.InDelta(t, 0.003603, estimate.Total, 1e-12)
}

func TestCatalogValidation(t *testing.T) {
	tests := map[string]string{
		"version":              `{"version":2,"entries":[]}`,
		"missing provenance":   `{"version":1,"entries":[{"provider":"openai","model_id":"m","rates":{"input_per_million":1,"output_per_million":1},"prompt_cache":{"read_accounting":"separate"}}]}`,
		"missing output rate":  `{"version":1,"entries":[{"provider":"openai","model_id":"m","rates":{"input_per_million":1},"prompt_cache":{"read_accounting":"separate"},"pricing_as_of":"2026-01-01","pricing_source":"test"}]}`,
		"unknown unit":         `{"version":1,"entries":[{"provider":"openai","model_id":"m","unit":"tokens","rates":{"input_per_million":1,"output_per_million":1},"prompt_cache":{"read_accounting":"separate"},"pricing_as_of":"2026-01-01","pricing_source":"test"}]}`,
		"bad cache accounting": `{"version":1,"entries":[{"provider":"openai","model_id":"m","rates":{"input_per_million":1,"output_per_million":1},"prompt_cache":{"read_accounting":"sometimes"},"pricing_as_of":"2026-01-01","pricing_source":"test"}]}`,
		"duplicate alias":      `{"version":1,"entries":[{"provider":"openai","model_id":"m","aliases":["M"],"rates":{"input_per_million":1,"output_per_million":1},"prompt_cache":{"read_accounting":"separate"},"pricing_as_of":"2026-01-01","pricing_source":"test"}]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCatalog([]byte(raw))
			require.Error(t, err)
		})
	}
}

func TestNonUSDCatalogFallsBack(t *testing.T) {
	resolver := mustResolver(t, `{
  "version": 1,
  "entries": [{
    "provider": "openai", "model_id": "m", "currency": "EUR",
    "rates": {"input_per_million": 1, "output_per_million": 1},
    "prompt_cache": {"read_accounting": "separate"},
    "pricing_as_of": "2026-08-01", "pricing_source": "test"
  }]
}`)
	_, ok := resolver.EstimateCost("openai", "m", router.TokenUsage{PromptTokens: 100})
	require.False(t, ok)
}
