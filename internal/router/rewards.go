package router

import "time"

// RewardLog captures the features and outcome of a routing decision for
// contextual bandit reward logging. This is the foundation for RL-based
// routing: data collection begins immediately, and the reward signal can
// later be used to train a policy that replaces the hand-tuned scoring.
type RewardLog struct {
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id"`
	ModelID   string    `json:"model_id"`
	ProviderID string   `json:"provider_id"`
	Mode      string    `json:"mode"`

	// Features (context for the bandit)
	EstimatedTokens int    `json:"estimated_tokens"`
	TokenBucket     string `json:"token_bucket"`     // "small", "medium", "large"
	LatencyBudgetMs int    `json:"latency_budget_ms"`

	// Outcome
	LatencyMs  float64 `json:"latency_ms"`
	CostUSD    float64 `json:"cost_usd"`
	Success    bool    `json:"success"`
	ErrorClass string  `json:"error_class,omitempty"`

	// Computed reward (higher is better, 0-1 normalized)
	Reward float64 `json:"reward"`
}

// ComputeReward calculates a 0-1 normalized reward from the outcome of a
// routing decision. The reward is a weighted combination of cost efficiency,
// latency efficiency, and a success bonus.
//
//   - Returns 0.0 if not successful.
//   - cost_norm   = min(costUSD / 0.1, 1.0)
//   - latency_norm = min(latencyMs / max(latencyBudgetMs, 1000), 1.0)
//   - reward = (1 - cost_norm) * 0.3 + (1 - latency_norm) * 0.3 + success_bonus * 0.4
func ComputeReward(latencyMs, costUSD float64, success bool, latencyBudgetMs int) float64 {
	if !success {
		return 0.0
	}

	costNorm := costUSD / 0.1
	if costNorm > 1.0 {
		costNorm = 1.0
	}

	budget := float64(latencyBudgetMs)
	if budget < 1000 {
		budget = 1000
	}
	latencyNorm := latencyMs / budget
	if latencyNorm > 1.0 {
		latencyNorm = 1.0
	}

	return (1-costNorm)*0.3 + (1-latencyNorm)*0.3 + 0.4
}

// TokenBucketLabel categorizes an estimated token count into a bucket label
// for use as a feature in the contextual bandit.
func TokenBucketLabel(tokens int) string {
	if tokens < 1000 {
		return "small"
	}
	if tokens <= 10000 {
		return "medium"
	}
	return "large"
}
