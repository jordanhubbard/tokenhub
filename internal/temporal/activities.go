package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
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

	_, ok := a.Engine.GetModel(input.ModelID)
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
	estimate, ok := a.Engine.EstimateModelCost(input.ModelID, router.TokenUsage{
		PromptTokens:     tokens,
		CompletionTokens: 512,
	})
	if !ok {
		estimate = router.CostEstimate{Total: 0, Currency: "USD", Source: "preflight_estimate"}
	} else {
		estimate.Source = "preflight_estimate"
	}

	// Extract actual token usage from the provider response.
	usage := extractProviderUsage(resp)
	if usage.cost != nil {
		estimate = *usage.cost
	} else if usage.hasUsage {
		if actual, found := a.Engine.EstimateModelCost(input.ModelID, router.TokenUsage{
			PromptTokens:     usage.input,
			CompletionTokens: usage.output,
			CacheReadTokens:  usage.cacheRead,
			CacheWriteTokens: usage.cacheWrite,
		}); found {
			estimate = actual
		}
	}

	if a.EventBus != nil {
		a.EventBus.Publish(events.Event{
			Type:         events.EventActivityCompleted,
			ActivityType: "SendToProvider",
			ModelID:      input.ModelID,
			ProviderID:   input.ProviderID,
			LatencyMs:    float64(latencyMs),
			CostUSD:      estimate.Total,
		})
	}

	return SendOutput{
		Response:         json.RawMessage(resp),
		LatencyMs:        latencyMs,
		EstimatedCost:    estimate.Total,
		InputTokens:      usage.input,
		OutputTokens:     usage.output,
		CacheReadTokens:  usage.cacheRead,
		CacheWriteTokens: usage.cacheWrite,
		CostSource:       estimate.Source,
		PricingSource:    estimate.PricingSource,
		PricingAsOf:      estimate.PricingAsOf,
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
			CostSource:       input.CostSource,
			PricingSource:    input.PricingSource,
			PricingAsOf:      input.PricingAsOf,
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
			CacheReadTokens:  input.CacheReadTokens,
			CacheWriteTokens: input.CacheWriteTokens,
			CostSource:       input.CostSource,
			PricingSource:    input.PricingSource,
			PricingAsOf:      input.PricingAsOf,
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
			if input.CacheReadTokens > 0 {
				a.Metrics.TokensTotal.WithLabelValues(input.ModelID, input.ProviderID, "cache_read").Add(float64(input.CacheReadTokens))
			}
			if input.CacheWriteTokens > 0 {
				a.Metrics.TokensTotal.WithLabelValues(input.ModelID, input.ProviderID, "cache_write").Add(float64(input.CacheWriteTokens))
			}
		}
	}

	if a.EventBus != nil {
		if input.Success {
			a.EventBus.Publish(events.Event{
				Type:             events.EventRouteSuccess,
				ModelID:          input.ModelID,
				ProviderID:       input.ProviderID,
				LatencyMs:        float64(input.LatencyMs),
				CostUSD:          input.CostUSD,
				InputTokens:      input.InputTokens,
				OutputTokens:     input.OutputTokens,
				TotalTokens:      input.InputTokens + input.OutputTokens,
				CacheReadTokens:  input.CacheReadTokens,
				CacheWriteTokens: input.CacheWriteTokens,
				CostSource:       input.CostSource,
				PricingSource:    input.PricingSource,
				PricingAsOf:      input.PricingAsOf,
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
			ModelID:          input.ModelID,
			ProviderID:       input.ProviderID,
			LatencyMs:        float64(input.LatencyMs),
			CostUSD:          input.CostUSD,
			Success:          input.Success,
			InputTokens:      input.InputTokens,
			OutputTokens:     input.OutputTokens,
			CacheReadTokens:  input.CacheReadTokens,
			CacheWriteTokens: input.CacheWriteTokens,
		})
	}

	if a.TSDB != nil && input.Success {
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.LatencyMs)})
		a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: input.CostUSD})
		if total := input.InputTokens + input.OutputTokens; total > 0 {
			a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "tokens", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(total)})
		}
		if input.CacheReadTokens > 0 {
			a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cache_read_tokens", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.CacheReadTokens)})
		}
		if input.CacheWriteTokens > 0 {
			a.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cache_write_tokens", ModelID: input.ModelID, ProviderID: input.ProviderID, Value: float64(input.CacheWriteTokens)})
		}
	}

	return nil
}

type providerUsage struct {
	input, output         int
	cacheRead, cacheWrite int
	hasUsage              bool
	cost                  *router.CostEstimate
}

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
	var rawUsage struct {
		PromptTokens        *int `json:"prompt_tokens"`
		CompletionTokens    *int `json:"completion_tokens"`
		InputTokens         *int `json:"input_tokens"`
		OutputTokens        *int `json:"output_tokens"`
		PromptTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		CacheReadInputTokens *int     `json:"cache_read_input_tokens"`
		CacheReadTokens      *int     `json:"cache_read_tokens"`
		CacheCreationTokens  *int     `json:"cache_creation_input_tokens"`
		CacheWriteTokens     *int     `json:"cache_write_tokens"`
		CostUSD              *float64 `json:"cost_usd"`
		Cost                 *struct {
			Total           *float64 `json:"total"`
			Currency        string   `json:"currency"`
			Input           *float64 `json:"input"`
			Output          *float64 `json:"output"`
			CacheRead       *float64 `json:"cache_read"`
			CacheWrite      *float64 `json:"cache_write"`
			Source          string   `json:"source"`
			PricingProvider string   `json:"pricing_provider"`
			PricingModel    string   `json:"pricing_model"`
			PricingAsOf     string   `json:"pricing_as_of"`
			PricingSource   string   `json:"pricing_source"`
		} `json:"cost"`
	}
	if json.Unmarshal(envelope.Usage, &rawUsage) != nil {
		return providerUsage{}
	}
	usage := providerUsage{}
	if rawUsage.PromptTokens != nil || rawUsage.CompletionTokens != nil {
		usage.input = temporalNonNegativeInt(rawUsage.PromptTokens)
		usage.output = temporalNonNegativeInt(rawUsage.CompletionTokens)
		usage.hasUsage = true
	} else if rawUsage.InputTokens != nil || rawUsage.OutputTokens != nil {
		usage.input = temporalNonNegativeInt(rawUsage.InputTokens)
		usage.output = temporalNonNegativeInt(rawUsage.OutputTokens)
		usage.hasUsage = true
	}
	cacheRead := rawUsage.CacheReadTokens
	if rawUsage.CacheReadInputTokens != nil {
		cacheRead = rawUsage.CacheReadInputTokens
	}
	if rawUsage.InputTokensDetails != nil && rawUsage.InputTokensDetails.CachedTokens != nil {
		cacheRead = rawUsage.InputTokensDetails.CachedTokens
	}
	if rawUsage.PromptTokensDetails != nil && rawUsage.PromptTokensDetails.CachedTokens != nil {
		cacheRead = rawUsage.PromptTokensDetails.CachedTokens
	}
	if cacheRead != nil {
		usage.cacheRead = temporalNonNegativeInt(cacheRead)
		usage.hasUsage = true
	}
	cacheWrite := rawUsage.CacheWriteTokens
	if rawUsage.CacheCreationTokens != nil {
		cacheWrite = rawUsage.CacheCreationTokens
	}
	if cacheWrite != nil {
		usage.cacheWrite = temporalNonNegativeInt(cacheWrite)
		usage.hasUsage = true
	}
	if temporalValidCost(rawUsage.CostUSD) {
		usage.cost = &router.CostEstimate{Total: *rawUsage.CostUSD, Currency: "USD", Source: "provider_reported"}
	} else if cost := rawUsage.Cost; cost != nil && (cost.Currency == "" || strings.EqualFold(cost.Currency, "USD")) {
		if total, ok := temporalCostTotal(cost.Total, cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite); ok {
			source := cost.Source
			if source == "" {
				source = "provider_reported"
			}
			usage.cost = &router.CostEstimate{
				Total: total, Currency: "USD", Source: source,
				Input: cost.Input, Output: cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite,
				PricingProvider: cost.PricingProvider, PricingModel: cost.PricingModel,
				PricingAsOf: cost.PricingAsOf, PricingSource: cost.PricingSource,
			}
		}
	}
	return usage
}

func temporalNonNegativeInt(value *int) int {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func temporalValidCost(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func temporalCostTotal(total *float64, components ...*float64) (float64, bool) {
	if total != nil {
		if !temporalValidCost(total) {
			return 0, false
		}
		return *total, true
	}
	value := 0.0
	found := false
	for _, component := range components {
		if component != nil {
			if !temporalValidCost(component) {
				return 0, false
			}
			value += *component
			found = true
		}
	}
	return value, found
}
