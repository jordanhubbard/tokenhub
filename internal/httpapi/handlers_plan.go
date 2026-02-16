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
	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	temporalpkg "github.com/jordanhubbard/tokenhub/internal/temporal"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

type PlanRequest struct {
	Request       router.Request                `json:"request"`
	Orchestration router.OrchestrationDirective `json:"orchestration"`
}

type PlanResponse struct {
	NegotiatedModel  string          `json:"negotiated_model"`
	EstimatedCostUSD float64         `json:"estimated_cost_usd"`
	RoutingReason    string          `json:"routing_reason"`
	Response         json.RawMessage `json:"response"`
}

func PlanHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var req PlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Validate messages.
		if len(req.Request.Messages) == 0 {
			http.Error(w, "messages required", http.StatusBadRequest)
			return
		}

		// Validate orchestration iterations.
		if req.Orchestration.Iterations < 0 || req.Orchestration.Iterations > 10 {
			http.Error(w, "iterations must be between 0 and 10", http.StatusBadRequest)
			return
		}

		// Validate orchestration mode.
		switch req.Orchestration.Mode {
		case "", "planning", "adversarial", "vote", "refine":
			// valid
		default:
			http.Error(w, "unknown orchestration mode", http.StatusBadRequest)
			return
		}

		// Determine API key ID for workflow attribution.
		apiKeyID := ""
		if rec := apikey.FromContext(r.Context()); rec != nil {
			apiKeyID = rec.ID
		}

		// Inject request ID into context for provider tracing.
		reqCtx := providers.WithRequestID(r.Context(), middleware.GetReqID(r.Context()))

		var decision router.Decision
		var resp json.RawMessage
		var err error
		temporalHandledLogging := false

		if d.TemporalClient != nil {
			// Dispatch via Temporal orchestration workflow.
			requestID := middleware.GetReqID(r.Context())
			input := temporalpkg.OrchestrationInput{
				RequestID: requestID,
				APIKeyID:  apiKeyID,
				Request:   req.Request,
				Directive: req.Orchestration,
			}
			workflowID := fmt.Sprintf("plan-%s", requestID)
			run, terr := d.TemporalClient.ExecuteWorkflow(reqCtx, client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: d.TemporalTaskQueue,
			}, temporalpkg.OrchestrationWorkflow, input)
			if terr != nil {
				// Temporal unavailable — fall back to direct path.
				decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
			} else {
				var output temporalpkg.ChatOutput
				if terr = run.Get(reqCtx, &output); terr != nil {
					decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
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
			decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
		}
		latencyMs := time.Since(start).Milliseconds()

		mode := req.Orchestration.Mode

		if err != nil {
			// Record metrics for failed requests (skip if Temporal already logged).
			if !temporalHandledLogging {
				if d.Metrics != nil {
					d.Metrics.RequestsTotal.WithLabelValues(mode, "", "", "error").Inc()
				}
				if d.Store != nil {
					_ = d.Store.LogRequest(r.Context(), store.RequestLog{
						Timestamp:  time.Now().UTC(),
						Mode:       mode,
						LatencyMs:  latencyMs,
						StatusCode: http.StatusBadGateway,
						ErrorClass: "routing_failure",
						RequestID:  middleware.GetReqID(r.Context()),
					})
				}
				if d.Store != nil {
					_ = d.Store.LogReward(r.Context(), store.RewardEntry{
						Timestamp:   time.Now().UTC(),
						RequestID:   middleware.GetReqID(r.Context()),
						Mode:        mode,
						LatencyMs:   float64(latencyMs),
						CostUSD:     0,
						Success:     false,
						ErrorClass:  "routing_failure",
						Reward:      router.ComputeReward(float64(latencyMs), 0, false, 0),
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
				d.Metrics.RequestsTotal.WithLabelValues(mode, decision.ModelID, decision.ProviderID, "ok").Inc()
				d.Metrics.RequestLatency.WithLabelValues(mode, decision.ModelID, decision.ProviderID).Observe(float64(latencyMs))
				d.Metrics.CostUSD.WithLabelValues(decision.ModelID, decision.ProviderID).Add(decision.EstimatedCostUSD)
			}
			if d.Store != nil {
				_ = d.Store.LogRequest(r.Context(), store.RequestLog{
					Timestamp:        time.Now().UTC(),
					ModelID:          decision.ModelID,
					ProviderID:       decision.ProviderID,
					Mode:             mode,
					EstimatedCostUSD: decision.EstimatedCostUSD,
					LatencyMs:        latencyMs,
					StatusCode:       http.StatusOK,
					RequestID:        middleware.GetReqID(r.Context()),
				})
			}
			if d.Store != nil {
				_ = d.Store.LogReward(r.Context(), store.RewardEntry{
					Timestamp:  time.Now().UTC(),
					RequestID:  middleware.GetReqID(r.Context()),
					ModelID:    decision.ModelID,
					ProviderID: decision.ProviderID,
					Mode:       mode,
					LatencyMs:  float64(latencyMs),
					CostUSD:    decision.EstimatedCostUSD,
					Success:    true,
					Reward:     router.ComputeReward(float64(latencyMs), decision.EstimatedCostUSD, true, 0),
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

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PlanResponse{
			NegotiatedModel:  decision.ModelID,
			EstimatedCostUSD: decision.EstimatedCostUSD,
			RoutingReason:    decision.Reason,
			Response:         resp,
		})
	}
}
