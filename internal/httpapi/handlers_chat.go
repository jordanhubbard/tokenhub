package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

type ChatRequest struct {
	// Side-channel negotiation
	Capabilities map[string]any `json:"capabilities,omitempty"`
	Policy       *PolicyHint    `json:"policy,omitempty"`

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
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
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

		decision, resp, err := d.Engine.RouteAndSend(r.Context(), req.Request, policy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		_ = json.NewEncoder(w).Encode(ChatResponse{
			NegotiatedModel: decision.ModelID,
			EstimatedCostUSD: decision.EstimatedCostUSD,
			RoutingReason: decision.Reason,
			Response: resp,
		})
	}
}
