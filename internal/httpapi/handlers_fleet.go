package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

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

// FleetRolloutStartHandler kicks off a RolloutWorkflow execution against the
// fleet-orchestrator task queue. Body: fleet_orchestrator.RolloutInput.
// Returns: {workflow_id, run_id}.
//
// Idempotent on caller-supplied `workflow_id` if present; otherwise the
// handler generates one.
func FleetRolloutStartHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if d.TemporalClient == nil || !d.FleetWorkerActive {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "fleet_worker_not_active",
			})
			return
		}
		var body struct {
			WorkflowID string                       `json:"workflow_id"`
			Input      fleet_orchestrator.RolloutInput `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		if body.WorkflowID == "" {
			body.WorkflowID = fmt.Sprintf("rollout-%s-%s-%s",
				body.Input.Component, body.Input.Version, uuid.NewString()[:8])
		}
		opts := client.StartWorkflowOptions{
			ID:                       body.WorkflowID,
			TaskQueue:                fleet_orchestrator.TaskQueue,
			WorkflowExecutionTimeout: 1 * time.Hour,
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		run, err := d.TemporalClient.ExecuteWorkflow(ctx, opts,
			"RolloutWorkflow", body.Input)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "execute_workflow_failed", "detail": err.Error(),
			})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workflow_id": run.GetID(),
			"run_id":      run.GetRunID(),
		})
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
