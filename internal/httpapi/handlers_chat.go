package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	temporalpkg "github.com/jordanhubbard/tokenhub/internal/temporal"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

type ChatRequest struct {
	// Side-channel negotiation
	Capabilities map[string]any `json:"capabilities,omitempty"`
	Policy       *PolicyHint    `json:"policy,omitempty"`

	// Output format shaping
	OutputFormat *router.OutputFormat `json:"output_format,omitempty"`

	// Main request payload (provider-agnostic envelope)
	Request router.Request `json:"request"`
}

type PolicyHint struct {
	Mode          string  `json:"mode,omitempty"`
	MaxBudgetUSD  float64 `json:"max_budget_usd,omitempty"`
	MaxLatencyMs  int     `json:"max_latency_ms,omitempty"`
	MinWeight     int     `json:"min_weight,omitempty"`
}

type ChatResponse struct {
	NegotiatedModel string          `json:"negotiated_model"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	RoutingReason   string          `json:"routing_reason"`
	Response        json.RawMessage `json:"response"`
}

func ChatHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Validate messages.
		if len(req.Request.Messages) == 0 {
			http.Error(w, "messages required", http.StatusBadRequest)
			return
		}

		// Validate policy hints if provided.
		if req.Policy != nil {
			if req.Policy.MaxBudgetUSD < 0 || req.Policy.MaxBudgetUSD > 100.0 {
				http.Error(w, "max_budget_usd must be between 0 and 100", http.StatusBadRequest)
				return
			}
			if req.Policy.MaxLatencyMs < 0 || req.Policy.MaxLatencyMs > 300000 {
				http.Error(w, "max_latency_ms must be between 0 and 300000", http.StatusBadRequest)
				return
			}
			if req.Policy.MinWeight < 0 || req.Policy.MinWeight > 10 {
				http.Error(w, "min_weight must be between 0 and 10", http.StatusBadRequest)
				return
			}
		}

		var policy router.Policy
		if req.Policy != nil {
			policy = router.Policy{
				Mode:         req.Policy.Mode,
				MaxBudgetUSD: req.Policy.MaxBudgetUSD,
				MaxLatencyMs: req.Policy.MaxLatencyMs,
				MinWeight:    req.Policy.MinWeight,
			}
		}

		// Parse @@tokenhub in-band directives from message content.
		if dirPolicy := router.ParseDirectives(req.Request.Messages); dirPolicy != nil {
			// In-band directives override side-channel policy (more specific).
			if dirPolicy.Mode != "" {
				policy.Mode = dirPolicy.Mode
			}
			if dirPolicy.MaxBudgetUSD > 0 {
				policy.MaxBudgetUSD = dirPolicy.MaxBudgetUSD
			}
			if dirPolicy.MaxLatencyMs > 0 {
				policy.MaxLatencyMs = dirPolicy.MaxLatencyMs
			}
			if dirPolicy.MinWeight > 0 {
				policy.MinWeight = dirPolicy.MinWeight
			}
			// Strip directives before forwarding to providers.
			req.Request.Messages = router.StripDirectives(req.Request.Messages)
		}

		// Estimate tokens for reward logging.
		estimatedTokens := req.Request.EstimatedInputTokens
		if estimatedTokens == 0 {
			for _, msg := range req.Request.Messages {
				estimatedTokens += len(msg.Content) / 4
			}
		}
		latencyBudgetMs := policy.MaxLatencyMs

		// Determine API key ID for workflow attribution.
		apiKeyID := ""
		if rec := apikey.FromContext(r.Context()); rec != nil {
			apiKeyID = rec.ID
		}

		var decision router.Decision
		var resp json.RawMessage
		var err error
		temporalHandledLogging := false

		if d.TemporalClient != nil {
			// Dispatch via Temporal workflow.
			requestID := middleware.GetReqID(r.Context())
			input := temporalpkg.ChatInput{
				RequestID: requestID,
				APIKeyID:  apiKeyID,
				Request:   req.Request,
				Policy:    policy,
			}
			workflowID := fmt.Sprintf("chat-%s", requestID)
			run, terr := d.TemporalClient.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: d.TemporalTaskQueue,
			}, temporalpkg.ChatWorkflow, input)
			if terr != nil {
				// Temporal unavailable — fall back to direct path.
				decision, resp, err = d.Engine.RouteAndSend(r.Context(), req.Request, policy)
			} else {
				var output temporalpkg.ChatOutput
				if terr = run.Get(r.Context(), &output); terr != nil {
					decision, resp, err = d.Engine.RouteAndSend(r.Context(), req.Request, policy)
				} else if output.Error != "" {
					err = fmt.Errorf("%s", output.Error)
					decision = output.Decision
					temporalHandledLogging = true // LogResult activity already ran
				} else {
					decision = output.Decision
					resp = output.Response
					temporalHandledLogging = true // LogResult activity already ran
				}
			}
		} else {
			// Direct engine call (fallback path).
			decision, resp, err = d.Engine.RouteAndSend(r.Context(), req.Request, policy)
		}
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			// Record metrics for failed requests (skip if Temporal already logged).
			if !temporalHandledLogging {
				if d.Metrics != nil {
					d.Metrics.RequestsTotal.WithLabelValues(policy.Mode, "", "", "error").Inc()
				}
				if d.Store != nil {
					_ = d.Store.LogRequest(r.Context(), store.RequestLog{
						Timestamp:  time.Now().UTC(),
						Mode:       policy.Mode,
						LatencyMs:  latencyMs,
						StatusCode: http.StatusBadGateway,
						ErrorClass: "routing_failure",
						RequestID:  middleware.GetReqID(r.Context()),
					})
				}
				if d.Store != nil {
					_ = d.Store.LogReward(r.Context(), store.RewardEntry{
						Timestamp:       time.Now().UTC(),
						RequestID:       middleware.GetReqID(r.Context()),
						Mode:            policy.Mode,
						EstimatedTokens: estimatedTokens,
						TokenBucket:     router.TokenBucketLabel(estimatedTokens),
						LatencyBudgetMs: latencyBudgetMs,
						LatencyMs:       float64(latencyMs),
						CostUSD:         0,
						Success:         false,
						ErrorClass:      "routing_failure",
						Reward:          router.ComputeReward(float64(latencyMs), 0, false, latencyBudgetMs),
					})
				}
				if d.EventBus != nil {
					d.EventBus.Publish(events.Event{
						Type:       events.EventRouteError,
						LatencyMs:  float64(latencyMs),
						ErrorClass: "routing_failure",
						ErrorMsg:   err.Error(),
					})
				}
				if d.Stats != nil {
					d.Stats.Record(stats.Snapshot{
						LatencyMs: float64(latencyMs),
						Success:   false,
					})
				}
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Record metrics for successful requests (skip if Temporal already logged).
		if !temporalHandledLogging {
			if d.Metrics != nil {
				d.Metrics.RequestsTotal.WithLabelValues(policy.Mode, decision.ModelID, decision.ProviderID, "ok").Inc()
				d.Metrics.RequestLatency.WithLabelValues(policy.Mode, decision.ModelID, decision.ProviderID).Observe(float64(latencyMs))
				d.Metrics.CostUSD.WithLabelValues(decision.ModelID, decision.ProviderID).Add(decision.EstimatedCostUSD)
			}
			if d.Store != nil {
				_ = d.Store.LogRequest(r.Context(), store.RequestLog{
					Timestamp:        time.Now().UTC(),
					ModelID:          decision.ModelID,
					ProviderID:       decision.ProviderID,
					Mode:             policy.Mode,
					EstimatedCostUSD: decision.EstimatedCostUSD,
					LatencyMs:        latencyMs,
					StatusCode:       http.StatusOK,
					RequestID:        middleware.GetReqID(r.Context()),
				})
			}
			if d.Store != nil {
				_ = d.Store.LogReward(r.Context(), store.RewardEntry{
					Timestamp:       time.Now().UTC(),
					RequestID:       middleware.GetReqID(r.Context()),
					ModelID:         decision.ModelID,
					ProviderID:      decision.ProviderID,
					Mode:            policy.Mode,
					EstimatedTokens: estimatedTokens,
					TokenBucket:     router.TokenBucketLabel(estimatedTokens),
					LatencyBudgetMs: latencyBudgetMs,
					LatencyMs:       float64(latencyMs),
					CostUSD:         decision.EstimatedCostUSD,
					Success:         true,
					Reward:          router.ComputeReward(float64(latencyMs), decision.EstimatedCostUSD, true, latencyBudgetMs),
				})
			}
			if d.EventBus != nil {
				d.EventBus.Publish(events.Event{
					Type:       events.EventRouteSuccess,
					ModelID:    decision.ModelID,
					ProviderID: decision.ProviderID,
					LatencyMs:  float64(latencyMs),
					CostUSD:    decision.EstimatedCostUSD,
					Reason:     decision.Reason,
				})
			}
			if d.Stats != nil {
				d.Stats.Record(stats.Snapshot{
					ModelID:    decision.ModelID,
					ProviderID: decision.ProviderID,
					LatencyMs:  float64(latencyMs),
					CostUSD:    decision.EstimatedCostUSD,
					Success:    true,
				})
			}
			if d.TSDB != nil {
				now := time.Now().UTC()
				d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: decision.ModelID, ProviderID: decision.ProviderID, Value: float64(latencyMs)})
				d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: decision.ModelID, ProviderID: decision.ProviderID, Value: decision.EstimatedCostUSD})
			}
		}

		// Apply output format shaping if requested.
		if req.OutputFormat != nil {
			resp = router.ShapeOutput(resp, *req.OutputFormat)
		}

		_ = json.NewEncoder(w).Encode(ChatResponse{
			NegotiatedModel:  decision.ModelID,
			EstimatedCostUSD: decision.EstimatedCostUSD,
			RoutingReason:    decision.Reason,
			Response:         resp,
		})
	}
}
