package httpapi

import (
	"encoding/json"
	"net/http"
)

// StatsResponse is returned by the /admin/v1/stats endpoint.
type StatsResponse struct {
	Global     any `json:"global"`
	ByModel    any `json:"by_model"`
	ByProvider any `json:"by_provider"`
	ByAPIKey   any `json:"by_api_key"`
	ByMode     any `json:"by_mode"`
}

// StatsHandler returns aggregated dashboard stats from the stats collector.
func StatsHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if d.Stats == nil {
			_ = json.NewEncoder(w).Encode(StatsResponse{
				Global:     []any{},
				ByModel:    map[string]any{},
				ByProvider: map[string]any{},
				ByAPIKey:   map[string]any{},
				ByMode:     map[string]any{},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(StatsResponse{
			Global:     d.Stats.Global(),
			ByModel:    d.Stats.Summary(),
			ByProvider: d.Stats.SummaryByProvider(),
			ByAPIKey:   d.Stats.SummaryByAPIKey(),
			ByMode:     d.Stats.SummaryByMode(),
		})
	}
}
