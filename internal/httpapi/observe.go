package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

// jsonError writes a JSON-encoded error response with the given status code.
// Response body format: {"error": "<msg>"}
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// observeParams captures all the fields required to log a request result
// across the Store, Metrics, EventBus, Stats, and TSDB subsystems.
type observeParams struct {
	// Context for store operations.
	Ctx context.Context

	// Routing decision fields.
	ModelID    string
	ProviderID string
	Mode       string
	CostUSD    float64
	LatencyMs  int64
	Success    bool
	ErrorClass string
	ErrorMsg   string
	Reason     string

	// Request identification.
	RequestID string
	APIKeyID  string

	// Reward logging enrichment.
	EstimatedTokens int
	LatencyBudgetMs int
}

// recordObservability writes a completed request result to all configured
// observability sinks (Store, Metrics, EventBus, Stats, TSDB). It
// consolidates the duplicated recording blocks from the chat and plan
// handlers into a single call site.
//
// The caller is responsible for determining success/failure and populating
// the observeParams accordingly. All nil-safe: each subsystem is skipped
// when the corresponding dependency is nil.
func recordObservability(d Dependencies, p observeParams) {
	// --- Prometheus metrics ---
	if d.Metrics != nil {
		status := "ok"
		if !p.Success {
			status = "error"
		}
		d.Metrics.RequestsTotal.WithLabelValues(p.Mode, p.ModelID, p.ProviderID, status).Inc()
		if p.Success {
			d.Metrics.RequestLatency.WithLabelValues(p.Mode, p.ModelID, p.ProviderID).Observe(float64(p.LatencyMs))
			d.Metrics.CostUSD.WithLabelValues(p.ModelID, p.ProviderID).Add(p.CostUSD)
		}
	}

	// --- Store: request log + reward log ---
	if d.Store != nil {
		statusCode := http.StatusOK
		if !p.Success {
			statusCode = http.StatusBadGateway
		}
		warnOnErr("log_request", d.Store.LogRequest(p.Ctx, store.RequestLog{
			Timestamp:        time.Now().UTC(),
			ModelID:          p.ModelID,
			ProviderID:       p.ProviderID,
			Mode:             p.Mode,
			EstimatedCostUSD: p.CostUSD,
			LatencyMs:        p.LatencyMs,
			StatusCode:       statusCode,
			ErrorClass:       p.ErrorClass,
			RequestID:        p.RequestID,
			APIKeyID:         p.APIKeyID,
		}))
		warnOnErr("log_reward", d.Store.LogReward(p.Ctx, store.RewardEntry{
			Timestamp:       time.Now().UTC(),
			RequestID:       p.RequestID,
			ModelID:         p.ModelID,
			ProviderID:      p.ProviderID,
			Mode:            p.Mode,
			EstimatedTokens: p.EstimatedTokens,
			TokenBucket:     router.TokenBucketLabel(p.EstimatedTokens),
			LatencyBudgetMs: p.LatencyBudgetMs,
			LatencyMs:       float64(p.LatencyMs),
			CostUSD:         p.CostUSD,
			Success:         p.Success,
			ErrorClass:      p.ErrorClass,
			Reward:          router.ComputeReward(float64(p.LatencyMs), p.CostUSD, p.Success, p.LatencyBudgetMs),
		}))
	}

	// --- EventBus ---
	if d.EventBus != nil {
		if p.Success {
			d.EventBus.Publish(events.Event{
				Type:       events.EventRouteSuccess,
				ModelID:    p.ModelID,
				ProviderID: p.ProviderID,
				LatencyMs:  float64(p.LatencyMs),
				CostUSD:    p.CostUSD,
				Reason:     p.Reason,
			})
		} else {
			d.EventBus.Publish(events.Event{
				Type:       events.EventRouteError,
				ModelID:    p.ModelID,
				ProviderID: p.ProviderID,
				LatencyMs:  float64(p.LatencyMs),
				ErrorClass: p.ErrorClass,
				ErrorMsg:   p.ErrorMsg,
			})
		}
	}

	// --- Stats ---
	if d.Stats != nil {
		d.Stats.Record(stats.Snapshot{
			ModelID:    p.ModelID,
			ProviderID: p.ProviderID,
			LatencyMs:  float64(p.LatencyMs),
			CostUSD:    p.CostUSD,
			Success:    p.Success,
		})
	}

	// --- TSDB (only on success) ---
	if d.TSDB != nil && p.Success {
		now := time.Now().UTC()
		d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(p.LatencyMs)})
		d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: p.CostUSD})
	}
}
