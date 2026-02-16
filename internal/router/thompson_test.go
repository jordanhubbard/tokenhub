package router

import (
	"testing"
)

func TestBetaSampleRange(t *testing.T) {
	// Beta samples should always be in [0, 1].
	for i := 0; i < 1000; i++ {
		v := betaSample(1, 1)
		if v < 0 || v > 1 {
			t.Fatalf("beta sample out of range: %f", v)
		}
	}
}

func TestBetaSampleSkew(t *testing.T) {
	// Beta(10, 1) should produce high values (mean ~0.91).
	var sum float64
	n := 5000
	for i := 0; i < n; i++ {
		sum += betaSample(10, 1)
	}
	mean := sum / float64(n)
	if mean < 0.8 {
		t.Errorf("expected high mean for Beta(10,1), got %f", mean)
	}

	// Beta(1, 10) should produce low values (mean ~0.09).
	sum = 0
	for i := 0; i < n; i++ {
		sum += betaSample(1, 10)
	}
	mean = sum / float64(n)
	if mean > 0.2 {
		t.Errorf("expected low mean for Beta(1,10), got %f", mean)
	}
}

func TestThompsonSamplerUniformPrior(t *testing.T) {
	ts := NewThompsonSampler()
	models := []string{"a", "b", "c"}

	// With no data (uniform priors), all models should appear across many samples.
	counts := make(map[string]int)
	for i := 0; i < 300; i++ {
		ranked := ts.Sample(models, "small")
		counts[ranked[0]]++
	}

	for _, id := range models {
		if counts[id] == 0 {
			t.Errorf("model %s never selected as top with uniform prior", id)
		}
	}
}

func TestThompsonSamplerStrongArm(t *testing.T) {
	ts := NewThompsonSampler()
	// Model "a" has strong history (alpha=50, beta=2) — should win most of the time.
	ts.UpdateArm("a", "small", 50, 2)
	// Model "b" has weak history (alpha=2, beta=50) — should rarely win.
	ts.UpdateArm("b", "small", 2, 50)

	models := []string{"a", "b"}
	aWins := 0
	n := 500
	for i := 0; i < n; i++ {
		ranked := ts.Sample(models, "small")
		if ranked[0] == "a" {
			aWins++
		}
	}

	// "a" should win >90% of the time.
	if float64(aWins)/float64(n) < 0.9 {
		t.Errorf("expected model 'a' to win >90%%, got %d/%d", aWins, n)
	}
}

func TestThompsonSamplerContextual(t *testing.T) {
	ts := NewThompsonSampler()
	// Model "a" is good for small requests.
	ts.UpdateArm("a", "small", 30, 2)
	ts.UpdateArm("b", "small", 2, 30)
	// Model "b" is good for large requests.
	ts.UpdateArm("a", "large", 2, 30)
	ts.UpdateArm("b", "large", 30, 2)

	models := []string{"a", "b"}
	n := 300

	// For "small" bucket, "a" should dominate.
	aSmall := 0
	for i := 0; i < n; i++ {
		ranked := ts.Sample(models, "small")
		if ranked[0] == "a" {
			aSmall++
		}
	}
	if float64(aSmall)/float64(n) < 0.85 {
		t.Errorf("expected 'a' to dominate small bucket, got %d/%d", aSmall, n)
	}

	// For "large" bucket, "b" should dominate.
	bLarge := 0
	for i := 0; i < n; i++ {
		ranked := ts.Sample(models, "large")
		if ranked[0] == "b" {
			bLarge++
		}
	}
	if float64(bLarge)/float64(n) < 0.85 {
		t.Errorf("expected 'b' to dominate large bucket, got %d/%d", bLarge, n)
	}
}

func TestThompsonSamplerSingleModel(t *testing.T) {
	ts := NewThompsonSampler()
	ranked := ts.Sample([]string{"only"}, "medium")
	if len(ranked) != 1 || ranked[0] != "only" {
		t.Errorf("expected [only], got %v", ranked)
	}
}

func TestRefreshParams(t *testing.T) {
	ts := NewThompsonSampler()

	fetch := func() ([]RewardSummaryRow, error) {
		return []RewardSummaryRow{
			{ModelID: "gpt-4", TokenBucket: "small", Count: 100, Successes: 90, SumReward: 80.0},
			{ModelID: "claude", TokenBucket: "small", Count: 100, Successes: 50, SumReward: 40.0},
		}, nil
	}

	refreshParams(ts, fetch, nil)

	// After refresh, gpt-4 (alpha=81, beta=21) should beat claude (alpha=41, beta=61) most of the time.
	gpt4Wins := 0
	n := 500
	for i := 0; i < n; i++ {
		ranked := ts.Sample([]string{"gpt-4", "claude"}, "small")
		if ranked[0] == "gpt-4" {
			gpt4Wins++
		}
	}
	if float64(gpt4Wins)/float64(n) < 0.85 {
		t.Errorf("expected gpt-4 to dominate after refresh, got %d/%d", gpt4Wins, n)
	}
}

func TestEligibleModelsThompsonMode(t *testing.T) {
	eng := NewEngine(EngineConfig{DefaultMode: "normal"})

	// Create a mock adapter.
	eng.adapters["openai"] = &mockSender{id: "openai"}
	eng.adapters["anthropic"] = &mockSender{id: "anthropic"}

	eng.RegisterModel(Model{ID: "gpt-4", ProviderID: "openai", Weight: 5, MaxContextTokens: 128000, InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true})
	eng.RegisterModel(Model{ID: "claude", ProviderID: "anthropic", Weight: 5, MaxContextTokens: 200000, InputPer1K: 0.003, OutputPer1K: 0.015, Enabled: true})

	// Set up TS with strong preference for claude on small requests.
	sampler := NewThompsonSampler()
	sampler.UpdateArm("claude", "small", 50, 2)
	sampler.UpdateArm("gpt-4", "small", 2, 50)
	eng.SetBanditPolicy(sampler)

	claudeFirst := 0
	n := 200
	for i := 0; i < n; i++ {
		eligible := eng.eligibleModels(500, Policy{Mode: "thompson"})
		if len(eligible) >= 2 && eligible[0].ID == "claude" {
			claudeFirst++
		}
	}

	if float64(claudeFirst)/float64(n) < 0.85 {
		t.Errorf("expected claude first >85%% in thompson mode, got %d/%d", claudeFirst, n)
	}

	// In normal mode, TS should NOT be used — deterministic scoring applies.
	eligible := eng.eligibleModels(500, Policy{Mode: "normal"})
	if len(eligible) < 2 {
		t.Fatalf("expected at least 2 eligible models, got %d", len(eligible))
	}
}
