package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

// tokenUsage holds actual token counts extracted from a provider response.
type tokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// extractUsage parses token usage from a raw provider response. It supports
// both OpenAI format (usage.prompt_tokens/completion_tokens) and Anthropic
// format (usage.input_tokens/output_tokens).
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
	// Try OpenAI format first.
	var oai struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	if json.Unmarshal(envelope.Usage, &oai) == nil && (oai.PromptTokens > 0 || oai.CompletionTokens > 0) {
		return tokenUsage{
			InputTokens:  oai.PromptTokens,
			OutputTokens: oai.CompletionTokens,
		}
	}
	// Try Anthropic format.
	var ant struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if json.Unmarshal(envelope.Usage, &ant) == nil && (ant.InputTokens > 0 || ant.OutputTokens > 0) {
		return tokenUsage{
			InputTokens:  ant.InputTokens,
			OutputTokens: ant.OutputTokens,
		}
	}
	return tokenUsage{}
}

// computeActualCost calculates cost from actual token counts and per-1k rates.
// Falls back to the pre-flight estimated cost when actual tokens are zero
// (e.g. streaming responses that don't include a usage block).
func computeActualCost(usage tokenUsage, estimatedCost float64, eng *router.Engine, modelID string) float64 {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return estimatedCost
	}
	if m, ok := eng.GetModel(modelID); ok {
		return (float64(usage.InputTokens)/1000)*m.InputPer1K +
			(float64(usage.OutputTokens)/1000)*m.OutputPer1K
	}
	return estimatedCost
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
	RequestID string
	APIKeyID  string

	// Reward logging enrichment.
	EstimatedTokens int
	LatencyBudgetMs int

	// Actual token usage extracted from the provider response.
	InputTokens  int
	OutputTokens int

	// HTTPStatus is the HTTP response status code sent to the client.
	// Used to populate tokenhub_request_errors_total. 0 means not set.
	HTTPStatus int
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
				Type:         events.EventRouteSuccess,
				ModelID:      p.ModelID,
				ProviderID:   p.ProviderID,
				LatencyMs:    float64(p.LatencyMs),
				CostUSD:      p.CostUSD,
				InputTokens:  p.InputTokens,
				OutputTokens: p.OutputTokens,
				TotalTokens:  p.InputTokens + p.OutputTokens,
				Reason:       p.Reason,
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
			ModelID:      p.ModelID,
			ProviderID:   p.ProviderID,
			LatencyMs:    float64(p.LatencyMs),
			CostUSD:      p.CostUSD,
			Success:      p.Success,
			InputTokens:  p.InputTokens,
			OutputTokens: p.OutputTokens,
		})
	}

	// --- TSDB (only on success) ---
	if d.TSDB != nil && p.Success {
		now := time.Now().UTC()
		d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(p.LatencyMs)})
		d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: p.CostUSD})
		if total := p.InputTokens + p.OutputTokens; total > 0 {
			d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "tokens", ModelID: p.ModelID, ProviderID: p.ProviderID, Value: float64(total)})
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
