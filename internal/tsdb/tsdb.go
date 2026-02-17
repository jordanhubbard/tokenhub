package tsdb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Point is a single time-series data point.
type Point struct {
	Timestamp  time.Time `json:"timestamp"`
	Metric     string    `json:"metric"`
	ModelID    string    `json:"model_id,omitempty"`
	ProviderID string    `json:"provider_id,omitempty"`
	Value      float64   `json:"value"`
}

// Series represents a named time series with its data points.
type Series struct {
	Metric     string    `json:"metric"`
	ModelID    string    `json:"model_id,omitempty"`
	ProviderID string    `json:"provider_id,omitempty"`
	Points     []DataPt  `json:"points"`
}

// DataPt is a timestamp+value pair for JSON output.
type DataPt struct {
	T     time.Time `json:"t"`
	Value float64   `json:"v"`
}

// QueryParams controls which data is returned.
type QueryParams struct {
	Metric     string
	ModelID    string
	ProviderID string
	Start      time.Time
	End        time.Time
	StepMs     int64 // downsample to this bucket size (0 = raw)
}

// Store is a lightweight embedded time-series database backed by SQLite.
type Store struct {
	db  *sql.DB
	mu  sync.Mutex

	// Retention: auto-delete points older than this.
	retention time.Duration

	// Write buffer for batching inserts.
	buf    []Point
	bufMax int
}

// New creates a TSDB store using the given SQLite DB handle.
func New(db *sql.DB) (*Store, error) {
	s := &Store{
		db:        db,
		retention: 7 * 24 * time.Hour, // 7 day default
		bufMax:    100,
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetRetention sets the data retention period.
func (s *Store) SetRetention(d time.Duration) {
	s.retention = d
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS tsdb_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			metric TEXT NOT NULL,
			model_id TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			value REAL NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tsdb_ts ON tsdb_points(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_tsdb_metric ON tsdb_points(metric, ts)`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("tsdb migrate: %w", err)
		}
	}
	return nil
}

// Write stores a single data point.
func (s *Store) Write(p Point) {
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now().UTC()
	}
	s.mu.Lock()
	s.buf = append(s.buf, p)
	if len(s.buf) >= s.bufMax {
		buf := s.buf
		s.buf = nil
		s.mu.Unlock()
		s.flush(buf)
		return
	}
	s.mu.Unlock()
}

// Flush forces all buffered points to disk.
func (s *Store) Flush() {
	s.mu.Lock()
	buf := s.buf
	s.buf = nil
	s.mu.Unlock()
	if len(buf) > 0 {
		s.flush(buf)
	}
}

func (s *Store) flush(points []Point) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO tsdb_points (ts, metric, model_id, provider_id, value) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer func() { _ = stmt.Close() }()

	for _, p := range points {
		_, _ = stmt.Exec(p.Timestamp.UnixMilli(), p.Metric, p.ModelID, p.ProviderID, p.Value)
	}
	_ = tx.Commit()
}

// Query returns time-series data matching the given parameters.
func (s *Store) Query(ctx context.Context, q QueryParams) ([]Series, error) {
	s.Flush() // ensure buffered data is visible

	where := "WHERE metric = ?"
	args := []any{q.Metric}

	if q.ModelID != "" {
		where += " AND model_id = ?"
		args = append(args, q.ModelID)
	}
	if q.ProviderID != "" {
		where += " AND provider_id = ?"
		args = append(args, q.ProviderID)
	}
	if !q.Start.IsZero() {
		where += " AND ts >= ?"
		args = append(args, q.Start.UnixMilli())
	}
	if !q.End.IsZero() {
		where += " AND ts <= ?"
		args = append(args, q.End.UnixMilli())
	}

	var query string
	if q.StepMs > 0 {
		// Downsample: bucket by step, average values.
		query = fmt.Sprintf(
			`SELECT (ts / %d) * %d AS bucket, model_id, provider_id, AVG(value)
			 FROM tsdb_points %s
			 GROUP BY bucket, model_id, provider_id
			 ORDER BY bucket ASC`, q.StepMs, q.StepMs, where)
	} else {
		query = fmt.Sprintf(
			`SELECT ts, model_id, provider_id, value
			 FROM tsdb_points %s
			 ORDER BY ts ASC`, where)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Group into series by model+provider combo.
	type seriesKey struct{ model, provider string }
	grouped := make(map[seriesKey][]DataPt)
	var order []seriesKey

	for rows.Next() {
		var tsMs int64
		var modelID, providerID string
		var value float64
		if err := rows.Scan(&tsMs, &modelID, &providerID, &value); err != nil {
			return nil, err
		}
		k := seriesKey{modelID, providerID}
		if _, exists := grouped[k]; !exists {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], DataPt{
			T:     time.UnixMilli(tsMs),
			Value: value,
		})
	}

	var result []Series
	for _, k := range order {
		result = append(result, Series{
			Metric:     q.Metric,
			ModelID:    k.model,
			ProviderID: k.provider,
			Points:     grouped[k],
		})
	}
	return result, rows.Err()
}

// Prune removes data points older than the retention period.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	s.Flush() // ensure buffered data is visible
	cutoff := time.Now().Add(-s.retention).UnixMilli()
	result, err := s.db.ExecContext(ctx, `DELETE FROM tsdb_points WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Metrics returns the list of distinct metric names.
func (s *Store) Metrics(ctx context.Context) ([]string, error) {
	s.Flush() // ensure buffered data is visible
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT metric FROM tsdb_points ORDER BY metric`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var metrics []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}
