package temporal

import (
	"encoding/json"
	"math"
	"testing"
)

func TestExtractProviderUsagePreservesNeMoRelayAccounting(t *testing.T) {
	usage := extractProviderUsage(json.RawMessage(`{
  "usage": {
    "input_tokens": 1000,
    "output_tokens": 200,
    "cache_read_input_tokens": 400,
    "cache_creation_input_tokens": 50,
    "cost": {
      "input": 0.001,
      "output": 0.002,
      "cache_read": 0.0001,
      "currency": "USD",
      "pricing_source": "relay-upstream"
    }
  }
}`))

	if !usage.hasUsage || usage.input != 1000 || usage.output != 200 || usage.cacheRead != 400 || usage.cacheWrite != 50 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if usage.cost == nil || math.Abs(usage.cost.Total-0.0031) > 1e-12 || usage.cost.Source != "provider_reported" || usage.cost.PricingSource != "relay-upstream" {
		t.Fatalf("unexpected cost: %+v", usage.cost)
	}
}
