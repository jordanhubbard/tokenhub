package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.temporal.io/sdk/client"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
)

// WorkflowsListHandler handles GET /admin/v1/workflows?limit=50&status=RUNNING
func WorkflowsListHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.TemporalClient == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflows": []any{},
				"temporal_enabled": false,
			})
			return
		}

		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := parseIntParam(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}

		// Build query string for Temporal visibility.
		query := ""
		if status := r.URL.Query().Get("status"); status != "" {
			query = "ExecutionStatus = '" + status + "'"
		}

		resp, err := d.TemporalClient.ListWorkflow(r.Context(), &workflowservice.ListWorkflowExecutionsRequest{
			PageSize: int32(limit),
			Query:    query,
		})
		if err != nil {
			http.Error(w, "temporal query error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var workflows []map[string]any
		for _, exec := range resp.Executions {
			wf := map[string]any{
				"workflow_id": exec.Execution.WorkflowId,
				"run_id":      exec.Execution.RunId,
				"type":        exec.Type.Name,
				"status":      exec.Status.String(),
				"start_time":  exec.StartTime.AsTime().Format(time.RFC3339),
			}
			if exec.CloseTime != nil {
				wf["close_time"] = exec.CloseTime.AsTime().Format(time.RFC3339)
				wf["duration_ms"] = exec.CloseTime.AsTime().Sub(exec.StartTime.AsTime()).Milliseconds()
			}
			workflows = append(workflows, wf)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"workflows":        workflows,
			"temporal_enabled": true,
		})
	}
}

// WorkflowDescribeHandler handles GET /admin/v1/workflows/{id}
func WorkflowDescribeHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.TemporalClient == nil {
			http.Error(w, "temporal not enabled", http.StatusServiceUnavailable)
			return
		}

		workflowID := chi.URLParam(r, "id")
		if workflowID == "" {
			http.Error(w, "workflow id required", http.StatusBadRequest)
			return
		}

		desc, err := d.TemporalClient.DescribeWorkflowExecution(r.Context(), workflowID, "")
		if err != nil {
			http.Error(w, "describe error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		info := desc.WorkflowExecutionInfo
		result := map[string]any{
			"workflow_id": info.Execution.WorkflowId,
			"run_id":      info.Execution.RunId,
			"type":        info.Type.Name,
			"status":      info.Status.String(),
			"start_time":  info.StartTime.AsTime().Format(time.RFC3339),
		}
		if info.CloseTime != nil {
			result["close_time"] = info.CloseTime.AsTime().Format(time.RFC3339)
			result["duration_ms"] = info.CloseTime.AsTime().Sub(info.StartTime.AsTime()).Milliseconds()
		}

		_ = json.NewEncoder(w).Encode(result)
	}
}

// WorkflowHistoryHandler handles GET /admin/v1/workflows/{id}/history
func WorkflowHistoryHandler(d Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.TemporalClient == nil {
			http.Error(w, "temporal not enabled", http.StatusServiceUnavailable)
			return
		}

		workflowID := chi.URLParam(r, "id")
		if workflowID == "" {
			http.Error(w, "workflow id required", http.StatusBadRequest)
			return
		}

		iter := d.TemporalClient.GetWorkflowHistory(r.Context(), workflowID, "",
			false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

		var events []map[string]any
		for iter.HasNext() {
			event, err := iter.Next()
			if err != nil {
				http.Error(w, "history error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			events = append(events, map[string]any{
				"event_id":   event.EventId,
				"event_type": event.EventType.String(),
				"timestamp":  event.EventTime.AsTime().Format(time.RFC3339),
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"workflow_id": workflowID,
			"events":     events,
		})
	}
}

// Ensure temporal imports are used.
var _ client.Client
var _ = enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT
