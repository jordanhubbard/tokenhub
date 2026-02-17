package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// maxStreamBytes limits streaming response size to prevent memory exhaustion (100 MB).
const maxStreamBytes = 100 * 1024 * 1024

// warnOnErr logs a warning if a background store operation fails.
// Used for audit logs, request logs, and reward logs that should not block
// the response but whose failures must be visible.
func warnOnErr(op string, err error) {
	if err != nil {
		slog.Warn("store operation failed", slog.String("op", op), slog.String("error", err.Error()))
	}
}

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
	Mode         string  `json:"mode,omitempty"`
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`
	MaxLatencyMs int     `json:"max_latency_ms,omitempty"`
	MinWeight    int     `json:"min_weight,omitempty"`
}

type ChatResponse struct {
	NegotiatedModel  string          `json:"negotiated_model"`
	EstimatedCostUSD float64         `json:"estimated_cost_usd"`
	RoutingReason    string          `json:"routing_reason"`
	Response         json.RawMessage `json:"response"`
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

		// Inject request ID into context for provider tracing.
		reqCtx := providers.WithRequestID(r.Context(), middleware.GetReqID(r.Context()))

		// Handle streaming requests.
		if req.Request.Stream {
			decision, body, serr := d.Engine.RouteAndStream(reqCtx, req.Request, policy)
			if serr != nil {
				http.Error(w, serr.Error(), http.StatusBadGateway)
				return
			}
			defer func() { _ = body.Close() }()

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Negotiated-Model", decision.ModelID)
			w.WriteHeader(http.StatusOK)

			flusher, _ := w.(http.Flusher)
			buf := make([]byte, 32*1024)
			var totalBytes int64
			streamSuccess := true
			reqID := middleware.GetReqID(r.Context())
			for {
				n, readErr := body.Read(buf)
				if n > 0 {
					totalBytes += int64(n)
					if totalBytes > maxStreamBytes {
						slog.Warn("stream: max size exceeded, terminating",
							slog.String("request_id", reqID),
							slog.String("model", decision.ModelID),
							slog.Int64("bytes", totalBytes))
						streamSuccess = false
						break
					}
					if _, writeErr := w.Write(buf[:n]); writeErr != nil {
						slog.Warn("stream: write error",
							slog.String("request_id", reqID),
							slog.String("error", writeErr.Error()))
						streamSuccess = false
						break
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						slog.Warn("stream: read error",
							slog.String("request_id", reqID),
							slog.String("model", decision.ModelID),
							slog.String("error", readErr.Error()))
						streamSuccess = false
					}
					break
				}
			}

			// After streaming completes, fire a Temporal workflow to log the result
			// for visibility. The actual SSE byte streaming stays direct; Temporal
			// is used only for the logging/observability bookend.
			streamLatencyMs := time.Since(start).Milliseconds()
			if d.TemporalClient != nil {
				logInput := temporalpkg.StreamLogInput{
					LogInput: temporalpkg.LogInput{
						RequestID:  reqID,
						ModelID:    decision.ModelID,
						ProviderID: decision.ProviderID,
						Mode:       policy.Mode,
						LatencyMs:  streamLatencyMs,
						CostUSD:    decision.EstimatedCostUSD,
						Success:    streamSuccess,
					},
					BytesStreamed: totalBytes,
				}
				wfID := fmt.Sprintf("stream-log-%s", reqID)
				_, err := d.TemporalClient.ExecuteWorkflow(
					context.Background(),
					client.StartWorkflowOptions{
						ID:        wfID,
						TaskQueue: d.TemporalTaskQueue,
					},
					temporalpkg.StreamLogWorkflow,
					logInput,
				)
				if err != nil {
					slog.Warn("stream: failed to start log workflow",
						slog.String("request_id", reqID),
						slog.String("error", err.Error()))
				}
			} else {
				// Direct logging path when Temporal is not available.
				if d.Metrics != nil {
					status := "ok"
					if !streamSuccess {
						status = "error"
					}
					d.Metrics.RequestsTotal.WithLabelValues(policy.Mode, decision.ModelID, decision.ProviderID, status).Inc()
					if streamSuccess {
						d.Metrics.RequestLatency.WithLabelValues(policy.Mode, decision.ModelID, decision.ProviderID).Observe(float64(streamLatencyMs))
						d.Metrics.CostUSD.WithLabelValues(decision.ModelID, decision.ProviderID).Add(decision.EstimatedCostUSD)
					}
				}
				if d.Store != nil {
					statusCode := http.StatusOK
					errClass := ""
					if !streamSuccess {
						statusCode = http.StatusBadGateway
						errClass = "stream_error"
					}
					warnOnErr("log_request", d.Store.LogRequest(r.Context(), store.RequestLog{
						Timestamp:        time.Now().UTC(),
						ModelID:          decision.ModelID,
						ProviderID:       decision.ProviderID,
						Mode:             policy.Mode,
						EstimatedCostUSD: decision.EstimatedCostUSD,
						LatencyMs:        streamLatencyMs,
						StatusCode:       statusCode,
						ErrorClass:       errClass,
						RequestID:        reqID,
						APIKeyID:         apiKeyID,
					}))
					warnOnErr("log_reward", d.Store.LogReward(r.Context(), store.RewardEntry{
						Timestamp:       time.Now().UTC(),
						RequestID:       reqID,
						ModelID:         decision.ModelID,
						ProviderID:      decision.ProviderID,
						Mode:            policy.Mode,
						EstimatedTokens: estimatedTokens,
						TokenBucket:     router.TokenBucketLabel(estimatedTokens),
						LatencyBudgetMs: latencyBudgetMs,
						LatencyMs:       float64(streamLatencyMs),
						CostUSD:         decision.EstimatedCostUSD,
						Success:         streamSuccess,
						Reward:          router.ComputeReward(float64(streamLatencyMs), decision.EstimatedCostUSD, streamSuccess, latencyBudgetMs),
					}))
				}
				if d.EventBus != nil {
					if streamSuccess {
						d.EventBus.Publish(events.Event{
							Type:       events.EventRouteSuccess,
							ModelID:    decision.ModelID,
							ProviderID: decision.ProviderID,
							LatencyMs:  float64(streamLatencyMs),
							CostUSD:    decision.EstimatedCostUSD,
						})
					} else {
						d.EventBus.Publish(events.Event{
							Type:       events.EventRouteError,
							ModelID:    decision.ModelID,
							ProviderID: decision.ProviderID,
							LatencyMs:  float64(streamLatencyMs),
							ErrorClass: "stream_error",
						})
					}
				}
				if d.Stats != nil {
					d.Stats.Record(stats.Snapshot{
						ModelID:    decision.ModelID,
						ProviderID: decision.ProviderID,
						LatencyMs:  float64(streamLatencyMs),
						CostUSD:    decision.EstimatedCostUSD,
						Success:    streamSuccess,
					})
				}
				if d.TSDB != nil && streamSuccess {
					now := time.Now().UTC()
					d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "latency", ModelID: decision.ModelID, ProviderID: decision.ProviderID, Value: float64(streamLatencyMs)})
					d.TSDB.Write(tsdb.Point{Timestamp: now, Metric: "cost", ModelID: decision.ModelID, ProviderID: decision.ProviderID, Value: decision.EstimatedCostUSD})
				}
			}
			return
		}

		var decision router.Decision
		var resp json.RawMessage
		var err error
		temporalHandledLogging := false

		if d.TemporalClient != nil && d.CircuitBreaker != nil && d.CircuitBreaker.Allow() {
			// Dispatch via Temporal workflow (circuit closed or half-open probe).
			requestID := middleware.GetReqID(r.Context())
			input := temporalpkg.ChatInput{
				RequestID: requestID,
				APIKeyID:  apiKeyID,
				Request:   req.Request,
				Policy:    policy,
			}
			workflowID := fmt.Sprintf("chat-%s", requestID)
			run, terr := d.TemporalClient.ExecuteWorkflow(reqCtx, client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: d.TemporalTaskQueue,
			}, temporalpkg.ChatWorkflow, input)
			if terr != nil {
				// Temporal unavailable — record failure and fall back.
				d.CircuitBreaker.RecordFailure()
				if d.Metrics != nil {
					d.Metrics.TemporalFallbackTotal.Inc()
				}
				decision, resp, err = d.Engine.RouteAndSend(reqCtx, req.Request, policy)
			} else {
				if d.EventBus != nil {
					d.EventBus.Publish(events.Event{
						Type:         events.EventWorkflowStarted,
						WorkflowID:   workflowID,
						WorkflowType: "ChatWorkflow",
						RequestID:    requestID,
					})
				}
				var output temporalpkg.ChatOutput
				if terr = run.Get(reqCtx, &output); terr != nil {
					d.CircuitBreaker.RecordFailure()
					if d.Metrics != nil {
						d.Metrics.TemporalFallbackTotal.Inc()
					}
					decision, resp, err = d.Engine.RouteAndSend(reqCtx, req.Request, policy)
				} else if output.Error != "" {
					d.CircuitBreaker.RecordSuccess()
					err = fmt.Errorf("%s", output.Error)
					decision = output.Decision
					temporalHandledLogging = true // LogResult activity already ran
					if d.EventBus != nil {
						d.EventBus.Publish(events.Event{
							Type:         events.EventWorkflowFailed,
							WorkflowID:   workflowID,
							WorkflowType: "ChatWorkflow",
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
							WorkflowType: "ChatWorkflow",
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
			decision, resp, err = d.Engine.RouteAndSend(reqCtx, req.Request, policy)
		}
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			// Record metrics for failed requests (skip if Temporal already logged).
			if !temporalHandledLogging {
				if d.Metrics != nil {
					d.Metrics.RequestsTotal.WithLabelValues(policy.Mode, "", "", "error").Inc()
				}
				if d.Store != nil {
					warnOnErr("log_request", d.Store.LogRequest(r.Context(), store.RequestLog{
						Timestamp:  time.Now().UTC(),
						Mode:       policy.Mode,
						LatencyMs:  latencyMs,
						StatusCode: http.StatusBadGateway,
						ErrorClass: "routing_failure",
						RequestID:  middleware.GetReqID(r.Context()),
						APIKeyID:   apiKeyID,
					}))
				}
				if d.Store != nil {
					warnOnErr("log_reward", d.Store.LogReward(r.Context(), store.RewardEntry{
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
					}))
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
				warnOnErr("log_request", d.Store.LogRequest(r.Context(), store.RequestLog{
					Timestamp:        time.Now().UTC(),
					ModelID:          decision.ModelID,
					ProviderID:       decision.ProviderID,
					Mode:             policy.Mode,
					EstimatedCostUSD: decision.EstimatedCostUSD,
					LatencyMs:        latencyMs,
					StatusCode:       http.StatusOK,
					RequestID:        middleware.GetReqID(r.Context()),
					APIKeyID:         apiKeyID,
				}))
			}
			if d.Store != nil {
				warnOnErr("log_reward", d.Store.LogReward(r.Context(), store.RewardEntry{
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
				}))
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
