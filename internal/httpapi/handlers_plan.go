package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"go.temporal.io/sdk/client"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/router"
	temporalpkg "github.com/jordanhubbard/tokenhub/internal/temporal"
)

type PlanRequest struct {
	Request router.Request `json:"request"`
	Orchestration router.OrchestrationDirective `json:"orchestration"`
}

func PlanHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		var decision router.Decision
		var resp json.RawMessage
		var err error

		if d.TemporalClient != nil {
			// Dispatch via Temporal orchestration workflow.
			requestID := middleware.GetReqID(r.Context())
			apiKeyID := ""
			if rec := apikey.FromContext(r.Context()); rec != nil {
				apiKeyID = rec.ID
			}
			input := temporalpkg.OrchestrationInput{
				RequestID: requestID,
				APIKeyID:  apiKeyID,
				Request:   req.Request,
				Directive: req.Orchestration,
			}
			workflowID := fmt.Sprintf("plan-%s", requestID)
			run, terr := d.TemporalClient.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: d.TemporalTaskQueue,
			}, temporalpkg.OrchestrationWorkflow, input)
			if terr != nil {
				// Temporal unavailable — fall back to direct path.
				decision, resp, err = d.Engine.Orchestrate(r.Context(), req.Request, req.Orchestration)
			} else {
				var output temporalpkg.ChatOutput
				if terr = run.Get(r.Context(), &output); terr != nil {
					decision, resp, err = d.Engine.Orchestrate(r.Context(), req.Request, req.Orchestration)
				} else if output.Error != "" {
					err = fmt.Errorf("%s", output.Error)
				} else {
					decision = output.Decision
					resp = output.Response
				}
			}
		} else {
			decision, resp, err = d.Engine.Orchestrate(r.Context(), req.Request, req.Orchestration)
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"negotiated_model": decision.ModelID,
			"routing_reason":   decision.Reason,
			"response":         resp,
		})
	}
}
