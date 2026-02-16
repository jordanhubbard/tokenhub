package stats

import (
	"testing"
	"time"
)

func TestRecordAndGlobal(t *testing.T) {
	c := NewCollector()
	now := time.Now()

	c.Record(Snapshot{Timestamp: now, ModelID: "m1", ProviderID: "p1", LatencyMs: 100, CostUSD: 0.01, Success: true})
	c.Record(Snapshot{Timestamp: now, ModelID: "m2", ProviderID: "p2", LatencyMs: 200, CostUSD: 0.02, Success: true})

	global := c.Global()
	if len(global) == 0 {
		t.Fatal("expected global aggregates")
	}

	// The 1m window should have 2 requests.
	found := false
	for _, a := range global {
		if a.Window == "1m" {
			found = true
			if a.RequestCount != 2 {
				t.Errorf("expected 2 requests, got %d", a.RequestCount)
			}
			if a.AvgLatencyMs != 150 {
				t.Errorf("expected avg latency 150, got %.1f", a.AvgLatencyMs)
			}
			if a.TotalCostUSD != 0.03 {
				t.Errorf("expected total cost 0.03, got %.4f", a.TotalCostUSD)
			}
		}
	}
	if !found {
		t.Error("expected 1m window in global stats")
	}
}

func TestSummaryByModel(t *testing.T) {
	c := NewCollector()
	now := time.Now()

	c.Record(Snapshot{Timestamp: now, ModelID: "gpt-4", ProviderID: "openai", LatencyMs: 100, Success: true})
	c.Record(Snapshot{Timestamp: now, ModelID: "gpt-4", ProviderID: "openai", LatencyMs: 200, Success: false})
	c.Record(Snapshot{Timestamp: now, ModelID: "claude", ProviderID: "anthropic", LatencyMs: 50, Success: true})

	summary := c.Summary()
	oneMin, ok := summary["1m"]
	if !ok {
		t.Fatal("expected 1m window")
	}

	// Should have two model groups.
	if len(oneMin) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(oneMin))
	}

	for _, a := range oneMin {
		if a.ModelID == "gpt-4" {
			if a.RequestCount != 2 {
				t.Errorf("expected 2 requests for gpt-4, got %d", a.RequestCount)
			}
			if a.ErrorCount != 1 {
				t.Errorf("expected 1 error for gpt-4, got %d", a.ErrorCount)
			}
			if a.ErrorRate != 0.5 {
				t.Errorf("expected 0.5 error rate, got %.2f", a.ErrorRate)
			}
		}
	}
}

func TestSummaryByProvider(t *testing.T) {
	c := NewCollector()
	now := time.Now()

	c.Record(Snapshot{Timestamp: now, ModelID: "m1", ProviderID: "openai", LatencyMs: 100, Success: true})
	c.Record(Snapshot{Timestamp: now, ModelID: "m2", ProviderID: "openai", LatencyMs: 200, Success: true})
	c.Record(Snapshot{Timestamp: now, ModelID: "m3", ProviderID: "anthropic", LatencyMs: 50, Success: true})

	byProvider := c.SummaryByProvider()
	oneMin, ok := byProvider["1m"]
	if !ok {
		t.Fatal("expected 1m window")
	}

	if len(oneMin) != 2 {
		t.Fatalf("expected 2 provider groups, got %d", len(oneMin))
	}
}

func TestPrune(t *testing.T) {
	c := NewCollector()
	c.maxAge = time.Second // short window for testing

	old := time.Now().Add(-2 * time.Second)
	recent := time.Now()

	c.Record(Snapshot{Timestamp: old, ModelID: "old", Success: true})
	c.Record(Snapshot{Timestamp: recent, ModelID: "new", Success: true})

	c.Prune()

	if c.SnapshotCount() != 1 {
		t.Errorf("expected 1 snapshot after prune, got %d", c.SnapshotCount())
	}
}

func TestP95Latency(t *testing.T) {
	c := NewCollector()
	now := time.Now()

	// 20 samples: 19 fast (10ms) + 1 slow (500ms).
	for i := 0; i < 19; i++ {
		c.Record(Snapshot{Timestamp: now, ModelID: "m1", ProviderID: "p1", LatencyMs: 10, Success: true})
	}
	c.Record(Snapshot{Timestamp: now, ModelID: "m1", ProviderID: "p1", LatencyMs: 500, Success: true})

	global := c.Global()
	for _, a := range global {
		if a.Window == "1m" {
			if a.P95LatencyMs != 500 {
				t.Errorf("expected p95=500, got %.1f", a.P95LatencyMs)
			}
		}
	}
}

func TestEmptyCollector(t *testing.T) {
	c := NewCollector()
	global := c.Global()
	if len(global) != 0 {
		t.Errorf("expected empty global, got %d", len(global))
	}
}
