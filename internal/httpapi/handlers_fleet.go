package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jordanhubbard/tokenhub/internal/fleet_orchestrator"
)

// FleetOrchestratorHealthHandler reports whether the fleet-orchestrator
// Temporal worker pool is connected and polling. Returns 200 with structured
// status when healthy; 503 when the cluster connection is missing or the
// fleet worker isn't registered.
//
// Acceptance criterion of ACC-etxq.
func FleetOrchestratorHealthHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"namespace":              "",
			"chat_task_queue":        d.TemporalTaskQueue,
			"fleet_task_queue":       d.TemporalFleetTaskQueue,
			"expected_fleet_queue":   fleet_orchestrator.TaskQueue,
			"client_connected":       d.TemporalClient != nil,
			"fleet_worker_active":    d.FleetWorkerActive,
			"workflows_registered": []string{
				"BuildArtifactWorkflow",
				"RolloutWorkflow",
				"SoulPersistenceWorkflow",
				"SelfDevSubmitWorkflow",
			},
		}
		// Serve 503 if the fleet worker isn't running so monitors can alert
		// without parsing the body. The body still ships so operators can
		// see why.
		status := http.StatusOK
		if d.TemporalClient == nil || !d.FleetWorkerActive {
			body["healthy"] = false
			body["reason"] = describeUnhealthyReason(d)
			status = http.StatusServiceUnavailable
		} else {
			body["healthy"] = true
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func describeUnhealthyReason(d Dependencies) string {
	switch {
	case d.TemporalClient == nil:
		return "temporal_client_not_initialized"
	case !d.FleetWorkerActive:
		return "fleet_worker_not_registered"
	}
	return "unknown"
}
