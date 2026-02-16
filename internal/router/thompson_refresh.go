package router

import (
	"log/slog"
	"time"
)

// RewardSummaryRow holds aggregated reward data for one (model, bucket) arm.
type RewardSummaryRow struct {
	ModelID     string
	TokenBucket string
	Count       int
	Successes   int
	SumReward   float64
}

// RefreshConfig configures the Thompson Sampling parameter refresh loop.
type RefreshConfig struct {
	Interval time.Duration
}

// DefaultRefreshConfig returns sensible defaults (refresh every 5 minutes).
func DefaultRefreshConfig() RefreshConfig {
	return RefreshConfig{Interval: 5 * time.Minute}
}

// FetchRewardSummaryFunc fetches aggregated reward data. The server wiring
// provides this as a closure over the store.
type FetchRewardSummaryFunc func() ([]RewardSummaryRow, error)

// StartRefreshLoop periodically loads reward stats and updates the sampler's
// Beta distribution parameters. Returns a stop function.
func StartRefreshLoop(cfg RefreshConfig, ts *ThompsonSampler, fetch FetchRewardSummaryFunc, logger *slog.Logger) func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		// Refresh immediately on start.
		refreshParams(ts, fetch, logger)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				refreshParams(ts, fetch, logger)
			case <-stop:
				return
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

func refreshParams(ts *ThompsonSampler, fetch FetchRewardSummaryFunc, logger *slog.Logger) {
	rows, err := fetch()
	if err != nil {
		if logger != nil {
			logger.Warn("thompson sampling: failed to refresh params", slog.String("error", err.Error()))
		}
		return
	}

	for _, r := range rows {
		// Beta distribution: alpha = sum(rewards) + 1, beta = (count - sum(rewards)) + 1
		alpha := r.SumReward + 1.0
		beta := max(float64(r.Count)-r.SumReward+1.0, 1.0)
		ts.UpdateArm(r.ModelID, r.TokenBucket, alpha, beta)
	}

	if len(rows) > 0 && logger != nil {
		logger.Debug("thompson sampling: refreshed params", slog.Int("arms", len(rows)))
	}
}
