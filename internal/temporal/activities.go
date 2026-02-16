package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

// Activities holds dependencies for Temporal activity implementations.
type Activities struct {
	Engine   *router.Engine
	Store    store.Store
	Health   *health.Tracker
	Metrics  *metrics.Registry
	EventBus *events.Bus
	Stats    *stats.Collector
	TSDB     *tsdb.Store
}

// SelectModel performs pure model selection via the engine.
func (a *Activities) SelectModel(ctx context.Context, input ChatInput) (router.Decision, error) {
	decision, _, err := a.Engine.SelectModel(ctx, input.Request, input.Policy)
	if err != nil {
		return router.Decision{}, fmt.Errorf("select model: %w", err)
	}
	return decision, nil
}

// SendToProvider calls a single provider adapter and records health/metrics.
func (a *Activities) SendToProvider(ctx context.Context, input SendInput) (SendOutput, error) {
	adapter := a.Engine.GetAdapter(input.ProviderID)
	if adapter == nil {
		return SendOutput{}, fmt.Errorf("no adapter for provider %q", input.ProviderID)
	}

	m, ok := a.Engine.GetModel(input.ModelID)
	if !ok {
		return SendOutput{}, fmt.Errorf("model %q not found", input.ModelID)
	}

	start := time.Now()
	resp, err := adapter.Send(ctx, input.ModelID, input.Request)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		if a.Health != nil {
			a.Health.RecordError(input.ProviderID, err.Error())
		}
		classified := adapter.ClassifyError(err)
		errClass := ""
		if classified != nil {
			errClass = string(classified.Class)
		}
		return SendOutput{
			LatencyMs:  latencyMs,
			ErrorClass: errClass,
		}, err
	}

	if a.Health != nil {
		a.Health.RecordSuccess(input.ProviderID, float64(latencyMs))
	}

	tokens := input.Request.EstimatedInputTokens
	if tokens == 0 {
		for _, msg := range input.Request.Messages {
			tokens += len(msg.Content) / 4
		}
	}
	estCost := (float64(tokens)/1000.0)*m.InputPer1K + (512.0/1000.0)*m.OutputPer1K

	return SendOutput{
		Response:      json.RawMessage(resp),
		LatencyMs:     latencyMs,
		EstimatedCost: estCost,
	}, nil
}

// ClassifyAndEscalate classifies an error and finds a fallback model.
func (a *Activities) ClassifyAndEscalate(ctx context.Context, input EscalateInput) (EscalateOutput, error) {
	m, ok := a.Engine.GetModel(input.CurrentModelID)
	if !ok {
		return EscalateOutput{}, nil
	}

	larger := a.Engine.FindLargerContextModel(m, input.TokensNeeded*2)
	if larger != nil {
		return EscalateOutput{
			NextModelID: larger.ID,
			ShouldRetry: true,
		}, nil
	}

	return EscalateOutput{ShouldRetry: false}, nil
}

// LogResult persists observability data: request logs, reward logs, metrics, events, stats, TSDB.
func (a *Activities) LogResult(ctx context.Context, input LogInput) error {
	now := time.Now().UTC()

	statusCode := 200
	if !input.Success {
		statusCode = 502
	}

	if a.Store != nil {
		_ = a.Store.LogRequest(ctx, store.RequestLog{
			Timestamp:        now,
			ModelID:          input.ModelID,
			ProviderID:       input.ProviderID,
			Mode:             input.Mode,
			EstimatedCostUSD: input.CostUSD,
			LatencyMs:        input.LatencyMs,
			StatusCode:       statusCode,
			ErrorClass:       input.ErrorClass,
			RequestID:        input.RequestID,
		})

		tokens := 0 // not available at this point
		_ = a.Store.LogReward(ctx, store.RewardEntry{
			Timestamp:       now,
			RequestID:       input.RequestID,
			ModelID:         input.ModelID,
			ProviderID:      input.ProviderID,
			Mode:            input.Mode,
			EstimatedTokens: tokens,
			TokenBucket:     router.TokenBucketLabel(tokens),
			LatencyMs:       float64(input.LatencyMs),
			CostUSD:         input.CostUSD,
			Success:         input.Success,
			ErrorClass:      input.ErrorClass,
			Reward:          router.ComputeReward(float64(input.LatencyMs), input.CostUSD, input.Success, 0),
		})
	}

	if a.Metrics != nil {
		status := "ok"
		if !input.Success {
			status = "error"
		}
		a.Metrics.RequestsTotal.WithLabelValues(input.Mode, input.ModelID, input.ProviderID, status).Inc()
		if input.Success {
			a.Metrics.RequestLatency.WithLabelValues(input.Mode, input.ModelID, input.ProviderID).Observe(float64(input.LatencyMs))
			a.Metrics.CostUSD.WithLabelValues(input.ModelID, input.ProviderID).Add(input.CostUSD)
		}
	}

	if a.EventBus != nil {
		if input.Success {
			a.EventBus.Publish(events.Event{
				Type:       events.EventRouteSuccess,
				ModelID:    input.ModelID,
				ProviderID: input.ProviderID,
				LatencyMs:  float64(input.LatencyMs),
				CostUSD:    input.CostUSD,
			})
		} else {
			a.EventBus.Publish(events.Event{
				Type:       events.EventRouteError,
				ModelID:    input.ModelID,
				ProviderID: input.ProviderID,
				LatencyMs:  float64(input.LatencyMs),
				ErrorClass: input.ErrorClass,
			})
		}
	}

	if a.Stats != nil {
		a.Stats.Record(stats.Snapshot{
			ModelID:    input.ModelID,
			ProviderID: input.ProviderID,
			LatencyMs:  float64(input.LatencyMs),
			CostUSD:    input.CostUSD,
			Success:    input.Success,
		})
	}

	if a.TSDB != nil && input.Success {
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.LatencyMs)})
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: input.CostUSD})
	}

	return nil
}
