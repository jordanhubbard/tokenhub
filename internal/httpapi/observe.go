package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

// tokenUsage holds actual token counts extracted from a provider response.
type tokenUsage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	HasUsage         bool
	Cost             *router.CostEstimate
}

// extractUsage parses token usage from a raw provider response. It supports
// OpenAI, Anthropic, and NeMo Relay normalized formats. Provider-reported
// costs are retained as authoritative when denominated in USD.
func extractUsage(raw json.RawMessage) tokenUsage {
	if len(raw) == 0 {
		return tokenUsage{}
	}
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Usage) == 0 {
		return tokenUsage{}
	}
	var normalized struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		InputTokens      *int `json:"input_tokens"`
		OutputTokens     *int `json:"output_tokens"`

		PromptTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		CacheReadInputTokens *int `json:"cache_read_input_tokens"`
		CacheReadTokens      *int `json:"cache_read_tokens"`
		CacheCreationTokens  *int `json:"cache_creation_input_tokens"`
		CacheWriteTokens     *int `json:"cache_write_tokens"`

		CostUSD *float64 `json:"cost_usd"`
		Cost    *struct {
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
	if err := json.Unmarshal(envelope.Usage, &normalized); err != nil {
		return tokenUsage{}
	}

	usage := tokenUsage{}
	if normalized.PromptTokens != nil || normalized.CompletionTokens != nil {
		usage.InputTokens = nonNegativeInt(normalized.PromptTokens)
		usage.OutputTokens = nonNegativeInt(normalized.CompletionTokens)
		usage.HasUsage = true
	} else if normalized.InputTokens != nil || normalized.OutputTokens != nil {
		usage.InputTokens = nonNegativeInt(normalized.InputTokens)
		usage.OutputTokens = nonNegativeInt(normalized.OutputTokens)
		usage.HasUsage = true
	}

	cacheRead := normalized.CacheReadTokens
	if normalized.CacheReadInputTokens != nil {
		cacheRead = normalized.CacheReadInputTokens
	}
	if normalized.InputTokensDetails != nil && normalized.InputTokensDetails.CachedTokens != nil {
		cacheRead = normalized.InputTokensDetails.CachedTokens
	}
	if normalized.PromptTokensDetails != nil && normalized.PromptTokensDetails.CachedTokens != nil {
		cacheRead = normalized.PromptTokensDetails.CachedTokens
	}
	if cacheRead != nil {
		usage.CacheReadTokens = nonNegativeInt(cacheRead)
		usage.HasUsage = true
	}
	cacheWrite := normalized.CacheWriteTokens
	if normalized.CacheCreationTokens != nil {
		cacheWrite = normalized.CacheCreationTokens
	}
	if cacheWrite != nil {
		usage.CacheWriteTokens = nonNegativeInt(cacheWrite)
		usage.HasUsage = true
	}

	if validCostAmount(normalized.CostUSD) {
		usage.Cost = &router.CostEstimate{
			Total:    *normalized.CostUSD,
			Currency: "USD",
			Source:   "provider_reported",
		}
		usage.HasUsage = true
	} else if cost := normalized.Cost; cost != nil && (cost.Currency == "" || strings.EqualFold(cost.Currency, "USD")) {
		total, ok := normalizedCostTotal(cost.Total, cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite)
		if ok {
			source := cost.Source
			if source == "" {
				source = "provider_reported"
			}
			usage.Cost = &router.CostEstimate{
				Total:           total,
				Currency:        "USD",
				Input:           cost.Input,
				Output:          cost.Output,
				CacheRead:       cost.CacheRead,
				CacheWrite:      cost.CacheWrite,
				Source:          source,
				PricingProvider: cost.PricingProvider,
				PricingModel:    cost.PricingModel,
				PricingAsOf:     cost.PricingAsOf,
				PricingSource:   cost.PricingSource,
			}
			usage.HasUsage = true
		}
	}
	return usage
}

func nonNegativeInt(value *int) int {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func normalizedCostTotal(total *float64, components ...*float64) (float64, bool) {
	if total != nil {
		if !validCostAmount(total) {
			return 0, false
		}
		return *total, true
	}
	value := 0.0
	found := false
	for _, component := range components {
		if component != nil {
			if !validCostAmount(component) {
				return 0, false
			}
			value += *component
			found = true
		}
	}
	return value, found
}

func validCostAmount(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

// computeActualCost gives provider-reported USD cost highest precedence, then
// applies the active NeMo Relay catalog (including cache/tier semantics), and
// finally falls back to the routing pre-flight estimate.
func computeActualCost(usage tokenUsage, estimatedCost float64, eng *router.Engine, modelID string) router.CostEstimate {
	if usage.Cost != nil {
		return *usage.Cost
	}
	if usage.HasUsage {
		if estimate, ok := eng.EstimateModelCost(modelID, router.TokenUsage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		}); ok {
			return estimate
		}
	}
	return router.CostEstimate{
		Total:    estimatedCost,
		Currency: "USD",
		Source:   "preflight_estimate",
	}
}

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
	RequestID  string
	APIKeyID   string
	APIKeyName string

	// Reward logging enrichment.
	EstimatedTokens int
	LatencyBudgetMs int

	// Actual token usage extracted from the provider response.
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	CostSource       string
	PricingSource    string
	PricingAsOf      string

	// HTTPStatus is the HTTP response status code sent to the client.
	// Used to populate tokenhub_request_errors_total. 0 means not set.
	HTTPStatus int

	// AliasFrom is the original alias name when a blind A/B rewrite happened.
	// Empty when no alias was applied. Recorded on the request log row so
	// analysts can group variants under the same experiment.
	AliasFrom string
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
		if !p.Success && p.HTTPStatus > 0 {
			d.Metrics.RequestErrorsByStatus.WithLabelValues(
				p.Mode, p.ModelID, p.ProviderID, fmt.Sprintf("%d", p.HTTPStatus),
			).Inc()
		}
		if p.Success {
			d.Metrics.RequestLatency.WithLabelValues(p.Mode, p.ModelID, p.ProviderID).Observe(float64(p.LatencyMs))
			d.Metrics.CostUSD.WithLabelValues(p.ModelID, p.ProviderID).Add(p.CostUSD)
			if p.InputTokens > 0 {
				d.Metrics.TokensTotal.WithLabelValues(p.ModelID, p.ProviderID, "input").Add(float64(p.InputTokens))
			}
			if p.OutputTokens > 0 {
				d.Metrics.TokensTotal.WithLabelValues(p.ModelID, p.ProviderID, "output").Add(float64(p.OutputTokens))
			}
			if p.CacheReadTokens > 0 {
				d.Metrics.TokensTotal.WithLabelValues(p.ModelID, p.ProviderID, "cache_read").Add(float64(p.CacheReadTokens))
			}
			if p.CacheWriteTokens > 0 {
				d.Metrics.TokensTotal.WithLabelValues(p.ModelID, p.ProviderID, "cache_write").Add(float64(p.CacheWriteTokens))
			}
		}
	}

	// --- Store: request log + reward log ---
	// Writes are dispatched to a dedicated worker goroutine (via StoreWriteQueue)
	// so that SQLite contention does not add to client-visible latency.
	// When StoreWriteQueue is nil (e.g. tests) the writes are synchronous.
	if d.Store != nil {
		statusCode := http.StatusOK
		if !p.Success {
			statusCode = http.StatusBadGateway
		}
		rl := store.RequestLog{
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
			InputTokens:      p.InputTokens,
			OutputTokens:     p.OutputTokens,
			TotalTokens:      p.InputTokens + p.OutputTokens,
			CacheReadTokens:  p.CacheReadTokens,
			CacheWriteTokens: p.CacheWriteTokens,
			CostSource:       p.CostSource,
			PricingSource:    p.PricingSource,
			PricingAsOf:      p.PricingAsOf,
			AliasFrom:        p.AliasFrom,
		}
		re := store.RewardEntry{
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
		}
		if d.StoreWriteQueue != nil {
			select {
			case d.StoreWriteQueue <- func() {
				d.warnOnErr("log_request", d.Store.LogRequest(context.Background(), rl))
				d.warnOnErr("log_reward", d.Store.LogReward(context.Background(), re))
			}:
			default:
				// Queue full: drop the write and record the miss.
				d.warnOnErr("log_request", errors.New("store write queue full"))
			}
		} else {
			d.warnOnErr("log_request", d.Store.LogRequest(p.Ctx, rl))
			d.warnOnErr("log_reward", d.Store.LogReward(p.Ctx, re))
		}
	}

	// --- EventBus ---
	if d.EventBus != nil {
		if p.Success {
			d.EventBus.Publish(events.Event{
				Type:             events.EventRouteSuccess,
				ModelID:          p.ModelID,
				ProviderID:       p.ProviderID,
				AliasFrom:        p.AliasFrom,
				LatencyMs:        float64(p.LatencyMs),
				CostUSD:          p.CostUSD,
				InputTokens:      p.InputTokens,
				OutputTokens:     p.OutputTokens,
				TotalTokens:      p.InputTokens + p.OutputTokens,
				CacheReadTokens:  p.CacheReadTokens,
				CacheWriteTokens: p.CacheWriteTokens,
				CostSource:       p.CostSource,
				PricingSource:    p.PricingSource,
				PricingAsOf:      p.PricingAsOf,
				Reason:           p.Reason,
				Mode:             p.Mode,
				APIKeyName:       p.APIKeyName,
				RequestID:        p.RequestID,
			})
		} else {
			d.EventBus.Publish(events.Event{
				Type:       events.EventRouteError,
				ModelID:    p.ModelID,
				ProviderID: p.ProviderID,
				AliasFrom:  p.AliasFrom,
				LatencyMs:  float64(p.LatencyMs),
				ErrorClass: p.ErrorClass,
				ErrorMsg:   p.ErrorMsg,
				Mode:       p.Mode,
				APIKeyName: p.APIKeyName,
				RequestID:  p.RequestID,
			})
		}
	}

	// --- Stats ---
	if d.Stats != nil {
		d.Stats.Record(stats.Snapshot{
			ModelID:          p.ModelID,
			ProviderID:       p.ProviderID,
			APIKeyID:         p.APIKeyID,
			APIKeyName:       p.APIKeyName,
			Mode:             p.Mode,
			LatencyMs:        float64(p.LatencyMs),
			CostUSD:          p.CostUSD,
			Success:          p.Success,
			InputTokens:      p.InputTokens,
			OutputTokens:     p.OutputTokens,
			CacheReadTokens:  p.CacheReadTokens,
			CacheWriteTokens: p.CacheWriteTokens,
		})
	}

	// --- TSDB ---
	if d.TSDB != nil {
		now := time.Now().UTC()
		d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(p.LatencyMs)})
		if p.Success {
			d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: p.CostUSD})
			if total := p.InputTokens + p.OutputTokens; total > 0 {
				d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "tokens", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(total)})
			}
			if p.CacheReadTokens > 0 {
				d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cache_read_tokens", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(p.CacheReadTokens)})
			}
			if p.CacheWriteTokens > 0 {
				d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cache_write_tokens", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(p.CacheWriteTokens)})
			}
		} else {
			d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "errors", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: 1})
		}
	}

	// --- Budget cache invalidation ---
	// After logging costs, invalidate the budget cache for this API key so
	// the next budget check reflects the updated spend immediately instead
	// of relying on the 30-second TTL.
	if d.BudgetChecker != nil && p.APIKeyID != "" {
		d.BudgetChecker.InvalidateCache(p.APIKeyID)
	}
}
