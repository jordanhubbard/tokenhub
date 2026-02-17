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
	temporalpkg "github.com/jordanhubbard/tokenhub/internal/temporal"
)

type PlanRequest struct {
	// Side-channel negotiation
	Capabilities map[string]any `json:"capabilities,omitempty"`

	// Output format shaping
	OutputFormat *router.OutputFormat `json:"output_format,omitempty"`

	// Main request payload
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
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}

		// Validate messages.
		if len(req.Request.Messages) == 0 {
			jsonError(w, "messages required", http.StatusBadRequest)
			return
		}

		// Validate orchestration iterations.
		if req.Orchestration.Iterations < 0 || req.Orchestration.Iterations > 10 {
			jsonError(w, "iterations must be between 0 and 10", http.StatusBadRequest)
			return
		}

		// Validate orchestration mode.
		switch req.Orchestration.Mode {
		case "", "planning", "adversarial", "vote", "refine":
			// valid
		default:
			jsonError(w, "unknown orchestration mode", http.StatusBadRequest)
			return
		}

		// Determine API key ID for workflow attribution.
		apiKeyID := ""
		if rec := apikey.FromContext(r.Context()); rec != nil {
			apiKeyID = rec.ID
		}

		// Estimate tokens for reward logging (same heuristic as ChatHandler).
		estimatedTokens := req.Request.EstimatedInputTokens
		if estimatedTokens == 0 {
			for _, msg := range req.Request.Messages {
				estimatedTokens += len(msg.Content) / 4
			}
		}

		// Inject request ID into context for provider tracing.
		reqCtx := providers.WithRequestID(r.Context(), middleware.GetReqID(r.Context()))

		var decision router.Decision
		var resp json.RawMessage
		var err error
		temporalHandledLogging := false

		if d.TemporalClient != nil && d.CircuitBreaker != nil && d.CircuitBreaker.Allow() {
			// Dispatch via Temporal orchestration workflow (circuit closed or half-open probe).
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
				// Temporal unavailable — record failure and fall back.
				d.CircuitBreaker.RecordFailure()
				if d.Metrics != nil {
					d.Metrics.TemporalFallbackTotal.Inc()
				}
				decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
			} else {
				if d.EventBus != nil {
					d.EventBus.Publish(events.Event{
						Type:         events.EventWorkflowStarted,
						WorkflowID:   workflowID,
						WorkflowType: "OrchestrationWorkflow",
						RequestID:    requestID,
					})
				}
				var output temporalpkg.ChatOutput
				if terr = run.Get(reqCtx, &output); terr != nil {
					d.CircuitBreaker.RecordFailure()
					if d.Metrics != nil {
						d.Metrics.TemporalFallbackTotal.Inc()
					}
					decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
				} else if output.Error != "" {
					d.CircuitBreaker.RecordSuccess()
					err = fmt.Errorf("%s", output.Error)
					decision = output.Decision
					temporalHandledLogging = true // LogResult activity already ran
					if d.EventBus != nil {
						d.EventBus.Publish(events.Event{
							Type:         events.EventWorkflowFailed,
							WorkflowID:   workflowID,
							WorkflowType: "OrchestrationWorkflow",
							ErrorMsg:     output.Error,
						})
					}
				} else {
					d.CircuitBreaker.RecordSuccess()
					decision = output.Decision
					resp = output.Response
					temporalHandledLogging = true // LogResult activity already ran
					if d.EventBus != nil {
						d.EventBus.Publish(events.Event{
							Type:         events.EventWorkflowCompleted,
							WorkflowID:   workflowID,
							WorkflowType: "OrchestrationWorkflow",
							ModelID:      decision.ModelID,
							ProviderID:   decision.ProviderID,
							LatencyMs:    float64(output.LatencyMs),
							CostUSD:      decision.EstimatedCostUSD,
						})
					}
				}
			}
		} else {
			// Direct engine call (circuit open or Temporal disabled).
			if d.TemporalClient != nil && d.CircuitBreaker != nil {
				// Circuit is open — count the fallback.
				if d.Metrics != nil {
					d.Metrics.TemporalFallbackTotal.Inc()
				}
			}
			decision, resp, err = d.Engine.Orchestrate(reqCtx, req.Request, req.Orchestration)
		}
		latencyMs := time.Since(start).Milliseconds()

		mode := req.Orchestration.Mode

		if err != nil {
			// Record metrics for failed requests (skip if Temporal already logged).
			if !temporalHandledLogging {
				recordObservability(d, observeParams{
					Ctx:             r.Context(),
					Mode:            mode,
					LatencyMs:       latencyMs,
					Success:         false,
					ErrorClass:      "routing_failure",
					ErrorMsg:        err.Error(),
					RequestID:       middleware.GetReqID(r.Context()),
					APIKeyID:        apiKeyID,
					EstimatedTokens: estimatedTokens,
				})
			}
			jsonError(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Record metrics for successful requests (skip if Temporal already logged).
		if !temporalHandledLogging {
			recordObservability(d, observeParams{
				Ctx:             r.Context(),
				ModelID:         decision.ModelID,
				ProviderID:      decision.ProviderID,
				Mode:            mode,
				CostUSD:         decision.EstimatedCostUSD,
				LatencyMs:       latencyMs,
				Success:         true,
				Reason:          decision.Reason,
				RequestID:       middleware.GetReqID(r.Context()),
				APIKeyID:        apiKeyID,
				EstimatedTokens: estimatedTokens,
			})
		}

		// Apply output format shaping if requested.
		if req.OutputFormat != nil {
			resp = router.ShapeOutput(resp, *req.OutputFormat)
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
