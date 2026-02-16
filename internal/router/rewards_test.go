package router

import (
	"math"
	"testing"
)

func TestComputeRewardFailure(t *testing.T) {
	reward := ComputeReward(100, 0.01, false, 5000)
	if reward != 0.0 {
		t.Errorf("expected 0.0 for failure, got %f", reward)
	}
}

func TestComputeRewardPerfect(t *testing.T) {
	// Zero cost, zero latency, success => max reward = 0.3 + 0.3 + 0.4 = 1.0
	reward := ComputeReward(0, 0, true, 5000)
	if math.Abs(reward-1.0) > 1e-9 {
		t.Errorf("expected 1.0 for perfect outcome, got %f", reward)
	}
}

func TestComputeRewardHighCostHighLatency(t *testing.T) {
	// costUSD = 0.1 => costNorm = 1.0, (1 - 1.0) * 0.3 = 0.0
	// latencyMs = 5000, budget = 5000 => latencyNorm = 1.0, (1 - 1.0) * 0.3 = 0.0
	// reward = 0.0 + 0.0 + 0.4 = 0.4
	reward := ComputeReward(5000, 0.1, true, 5000)
	if math.Abs(reward-0.4) > 1e-9 {
		t.Errorf("expected 0.4, got %f", reward)
	}
}

func TestComputeRewardMidRange(t *testing.T) {
	// costUSD = 0.05 => costNorm = 0.5, (1 - 0.5) * 0.3 = 0.15
	// latencyMs = 2500, budget = 5000 => latencyNorm = 0.5, (1 - 0.5) * 0.3 = 0.15
	// reward = 0.15 + 0.15 + 0.4 = 0.7
	reward := ComputeReward(2500, 0.05, true, 5000)
	if math.Abs(reward-0.7) > 1e-9 {
		t.Errorf("expected 0.7, got %f", reward)
	}
}

func TestComputeRewardCostClamped(t *testing.T) {
	// costUSD = 1.0 >> 0.1 => costNorm clamped to 1.0
	reward := ComputeReward(0, 1.0, true, 5000)
	// (1-1.0)*0.3 + (1-0)*0.3 + 0.4 = 0 + 0.3 + 0.4 = 0.7
	if math.Abs(reward-0.7) > 1e-9 {
		t.Errorf("expected 0.7 with clamped cost, got %f", reward)
	}
}

func TestComputeRewardLatencyClamped(t *testing.T) {
	// latencyMs = 20000, budget = 5000 => latencyNorm clamped to 1.0
	reward := ComputeReward(20000, 0, true, 5000)
	// (1-0)*0.3 + (1-1)*0.3 + 0.4 = 0.3 + 0 + 0.4 = 0.7
	if math.Abs(reward-0.7) > 1e-9 {
		t.Errorf("expected 0.7 with clamped latency, got %f", reward)
	}
}

func TestComputeRewardZeroBudget(t *testing.T) {
	// latencyBudgetMs = 0 => budget floor = 1000
	// latencyMs = 500 => latencyNorm = 500/1000 = 0.5
	reward := ComputeReward(500, 0, true, 0)
	// (1-0)*0.3 + (1-0.5)*0.3 + 0.4 = 0.3 + 0.15 + 0.4 = 0.85
	if math.Abs(reward-0.85) > 1e-9 {
		t.Errorf("expected 0.85 with zero budget, got %f", reward)
	}
}

func TestTokenBucketLabelSmall(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{0, "small"},
		{1, "small"},
		{999, "small"},
	}
	for _, tc := range tests {
		got := TokenBucketLabel(tc.tokens)
		if got != tc.want {
			t.Errorf("TokenBucketLabel(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestTokenBucketLabelMedium(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{1000, "medium"},
		{5000, "medium"},
		{10000, "medium"},
	}
	for _, tc := range tests {
		got := TokenBucketLabel(tc.tokens)
		if got != tc.want {
			t.Errorf("TokenBucketLabel(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestTokenBucketLabelLarge(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{10001, "large"},
		{50000, "large"},
		{100000, "large"},
	}
	for _, tc := range tests {
		got := TokenBucketLabel(tc.tokens)
		if got != tc.want {
			t.Errorf("TokenBucketLabel(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}
