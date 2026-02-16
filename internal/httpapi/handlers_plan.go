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
