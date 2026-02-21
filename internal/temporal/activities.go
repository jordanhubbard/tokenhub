package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/activity"

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
	if a.EventBus != nil {
		a.EventBus.Publish(events.Event{
			Type:         events.EventActivityCompleted,
			ActivityType: "SelectModel",
			ModelID:      decision.ModelID,
			ProviderID:   decision.ProviderID,
			RequestID:    input.RequestID,
		})
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
	activity.RecordHeartbeat(ctx, "sending")
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
		if a.EventBus != nil {
			a.EventBus.Publish(events.Event{
				Type:         events.EventActivityCompleted,
				ActivityType: "SendToProvider",
				ModelID:      input.ModelID,
				ProviderID:   input.ProviderID,
				LatencyMs:    float64(latencyMs),
				ErrorMsg:     err.Error(),
			})
		}
		return SendOutput{
			LatencyMs:  latencyMs,
			ErrorClass: errClass,
		}, err
	}

	if a.Health != nil {
		a.Health.RecordSuccess(input.ProviderID, float64(latencyMs))
	}

	if resp == nil {
		return SendOutput{
			Response:  json.RawMessage("null"),
			LatencyMs: latencyMs,
		}, nil
	}

	tokens := router.EstimateTokens(input.Request)
	estCost := (float64(tokens)/1000.0)*m.InputPer1K + (512.0/1000.0)*m.OutputPer1K

	// Extract actual token usage from the provider response.
	var inTok, outTok int
	if usage := extractProviderUsage(resp); usage.input > 0 || usage.output > 0 {
		inTok = usage.input
		outTok = usage.output
		estCost = (float64(inTok)/1000.0)*m.InputPer1K + (float64(outTok)/1000.0)*m.OutputPer1K
	}

	if a.EventBus != nil {
		a.EventBus.Publish(events.Event{
			Type:         events.EventActivityCompleted,
			ActivityType: "SendToProvider",
			ModelID:      input.ModelID,
			ProviderID:   input.ProviderID,
			LatencyMs:    float64(latencyMs),
			CostUSD:      estCost,
		})
	}

	return SendOutput{
		Response:      json.RawMessage(resp),
		LatencyMs:     latencyMs,
		EstimatedCost: estCost,
		InputTokens:   inTok,
		OutputTokens:  outTok,
	}, nil
}

// ResolveModel looks up a model's provider ID.
func (a *Activities) ResolveModel(ctx context.Context, modelID string) (string, error) {
	m, ok := a.Engine.GetModel(modelID)
	if !ok {
		return "", fmt.Errorf("model %q not found", modelID)
	}
	return m.ProviderID, nil
}

// ClassifyAndEscalate classifies an error and finds a fallback model.
func (a *Activities) ClassifyAndEscalate(ctx context.Context, input EscalateInput) (EscalateOutput, error) {
	m, ok := a.Engine.GetModel(input.CurrentModelID)
	if !ok {
		return EscalateOutput{}, nil
	}

	larger := a.Engine.FindLargerContextModel(m, input.TokensNeeded*2)
	if larger != nil {
		if a.EventBus != nil {
			a.EventBus.Publish(events.Event{
				Type:    events.EventEscalation,
				ModelID: input.CurrentModelID,
				Reason:  "escalating to " + larger.ID,
			})
		}
		return EscalateOutput{
			NextModelID: larger.ID,
			ShouldRetry: true,
		}, nil
	}

	return EscalateOutput{ShouldRetry: false}, nil
}

// StreamSelectModel performs model selection for streaming requests via Temporal for visibility.
// It returns the routing decision and emits a workflow_started event on the EventBus.
func (a *Activities) StreamSelectModel(ctx context.Context, input ChatInput) (router.Decision, error) {
	decision, _, err := a.Engine.SelectModel(ctx, input.Request, input.Policy)
	if err != nil {
		return router.Decision{}, fmt.Errorf("stream select model: %w", err)
	}

	if a.EventBus != nil {
		a.EventBus.Publish(events.Event{
			Type:       events.EventStreamStarted,
			ModelID:    decision.ModelID,
			ProviderID: decision.ProviderID,
			Reason:     fmt.Sprintf("stream-select:%s", input.RequestID),
		})
	}

	return decision, nil
}

// StreamLogResult logs the result of a completed streaming request.
// It records the same observability data as LogResult plus streaming-specific metrics.
func (a *Activities) StreamLogResult(ctx context.Context, input StreamLogInput) error {
	now := time.Now().UTC()

	statusCode := 200
	if !input.Success {
		statusCode = 502
	}

	if a.Store != nil {
		if err := a.Store.LogRequest(ctx, store.RequestLog{
			Timestamp:        now,
			ModelID:          input.ModelID,
			ProviderID:       input.ProviderID,
			Mode:             input.Mode,
			EstimatedCostUSD: input.CostUSD,
			LatencyMs:        input.LatencyMs,
			StatusCode:       statusCode,
			ErrorClass:       input.ErrorClass,
			RequestID:        input.RequestID,
		}); err != nil {
			slog.Warn("log_request failed", slog.String("error", err.Error()), slog.String("request_id", input.RequestID))
		}

		tokens := 0
		if err := a.Store.LogReward(ctx, store.RewardEntry{
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
		}); err != nil {
			slog.Warn("log_reward failed", slog.String("error", err.Error()), slog.String("request_id", input.RequestID))
		}
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
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "stream_bytes", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.BytesStreamed)})
	}

	return nil
}

// LogResult persists observability data: request logs, reward logs, metrics, events, stats, TSDB.
func (a *Activities) LogResult(ctx context.Context, input LogInput) error {
	now := time.Now().UTC()

	statusCode := 200
	if !input.Success {
		statusCode = 502
	}

	if a.Store != nil {
		if err := a.Store.LogRequest(ctx, store.RequestLog{
			Timestamp:        now,
			ModelID:          input.ModelID,
			ProviderID:       input.ProviderID,
			Mode:             input.Mode,
			EstimatedCostUSD: input.CostUSD,
			LatencyMs:        input.LatencyMs,
			StatusCode:       statusCode,
			ErrorClass:       input.ErrorClass,
			RequestID:        input.RequestID,
			InputTokens:      input.InputTokens,
			OutputTokens:     input.OutputTokens,
			TotalTokens:      input.InputTokens + input.OutputTokens,
		}); err != nil {
			slog.Warn("log_request failed", slog.String("error", err.Error()), slog.String("request_id", input.RequestID))
		}

		tokens := input.InputTokens + input.OutputTokens
		if err := a.Store.LogReward(ctx, store.RewardEntry{
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
		}); err != nil {
			slog.Warn("log_reward failed", slog.String("error", err.Error()), slog.String("request_id", input.RequestID))
		}
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
			if input.InputTokens > 0 {
				a.Metrics.TokensTotal.WithLabelValues(input.ModelID, input.ProviderID, "input").Add(float64(input.InputTokens))
			}
			if input.OutputTokens > 0 {
				a.Metrics.TokensTotal.WithLabelValues(input.ModelID, input.ProviderID, "output").Add(float64(input.OutputTokens))
			}
		}
	}

	if a.EventBus != nil {
		if input.Success {
			a.EventBus.Publish(events.Event{
				Type:         events.EventRouteSuccess,
				ModelID:      input.ModelID,
				ProviderID:   input.ProviderID,
				LatencyMs:    float64(input.LatencyMs),
				CostUSD:      input.CostUSD,
				InputTokens:  input.InputTokens,
				OutputTokens: input.OutputTokens,
				TotalTokens:  input.InputTokens + input.OutputTokens,
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
			ModelID:      input.ModelID,
			ProviderID:   input.ProviderID,
			LatencyMs:    float64(input.LatencyMs),
			CostUSD:      input.CostUSD,
			Success:      input.Success,
			InputTokens:  input.InputTokens,
			OutputTokens: input.OutputTokens,
		})
	}

	if a.TSDB != nil && input.Success {
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.LatencyMs)})
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: input.CostUSD})
		if total := input.InputTokens + input.OutputTokens; total > 0 {
			a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "tokens", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(total)})
		}
	}

	return nil
}

type providerUsage struct{ input, output int }

// extractProviderUsage parses token counts from a raw provider response,
// supporting both OpenAI and Anthropic response formats. It requires at
// least one token count to be non-zero to avoid false positives when the
// JSON structure matches but the fields aren't populated.
func extractProviderUsage(raw json.RawMessage) providerUsage {
	if len(raw) == 0 {
		return providerUsage{}
	}
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Usage) == 0 {
		return providerUsage{}
	}
	var oai struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	if json.Unmarshal(envelope.Usage, &oai) == nil && (oai.PromptTokens > 0 || oai.CompletionTokens > 0) {
		return providerUsage{input: oai.PromptTokens, output: oai.CompletionTokens}
	}
	var ant struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if json.Unmarshal(envelope.Usage, &ant) == nil && (ant.InputTokens > 0 || ant.OutputTokens > 0) {
		return providerUsage{input: ant.InputTokens, output: ant.OutputTokens}
	}
	return providerUsage{}
}
