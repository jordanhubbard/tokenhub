package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/tsdb"
)

// TSDBQueryHandler handles GET /admin/v1/tsdb/query?metric=...&model=...&provider=...&start=...&end=...&step=...
func TSDBQueryHandler(ts *tsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ts == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"series": []any{}})
			return
		}

		q := r.URL.Query()
		metric := q.Get("metric")
		if metric == "" {
			http.Error(w, "metric parameter required", http.StatusBadRequest)
			return
		}

		params := tsdb.QueryParams{
			Metric:     metric,
			ModelID:    q.Get("model"),
			ProviderID: q.Get("provider"),
		}

		if s := q.Get("start"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				params.Start = t
			} else if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
				params.Start = time.UnixMilli(ms)
			}
		}
		if e := q.Get("end"); e != "" {
			if t, err := time.Parse(time.RFC3339, e); err == nil {
				params.End = t
			} else if ms, err := strconv.ParseInt(e, 10, 64); err == nil {
				params.End = time.UnixMilli(ms)
			}
		}
		if step := q.Get("step"); step != "" {
			if ms, err := strconv.ParseInt(step, 10, 64); err == nil {
				params.StepMs = ms
			}
		}

		series, err := ts.Query(r.Context(), params)
		if err != nil {
			http.Error(w, "query error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"series": series})
	}
}

// TSDBMetricsHandler handles GET /admin/v1/tsdb/metrics - lists available metric names.
func TSDBMetricsHandler(ts *tsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ts == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"metrics": []any{}})
			return
		}

		metrics, err := ts.Metrics(r.Context())
		if err != nil {
			http.Error(w, "error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metrics": metrics})
	}
}
