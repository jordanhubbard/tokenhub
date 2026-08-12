package httpapi

import (
	"encoding/json"
	"net/http"
)

// PricingStatusHandler reports the active cost estimator and its loaded
// sources. Paths are configuration metadata; pricing catalogs contain no API
// keys or other provider credentials.
func PricingStatusHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.Engine.CostEstimatorStatus())
	}
}
