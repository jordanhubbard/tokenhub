package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jordanhubbard/tokenhub/internal/router"
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

		decision, resp, err := d.Engine.Orchestrate(r.Context(), req.Request, req.Orchestration)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"negotiated_model": decision.ModelID,
			"routing_reason": decision.Reason,
			"response": resp,
		})
	}
}
