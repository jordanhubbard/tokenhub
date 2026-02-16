package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using modernc.org/sqlite (pure-Go, no CGO).
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLite opens or creates a SQLite database at the given DSN.
func NewSQLite(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Enable WAL mode and set busy timeout.
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragmas: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Migrate(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			weight INTEGER NOT NULL DEFAULT 1,
			max_context_tokens INTEGER NOT NULL DEFAULT 4096,
			input_per_1k REAL NOT NULL DEFAULT 0,
			output_per_1k REAL NOT NULL DEFAULT 0,
			enabled BOOLEAN NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS providers (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT 1,
			base_url TEXT NOT NULL DEFAULT '',
			cred_store TEXT NOT NULL DEFAULT 'env'
		)`,
		`CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			model_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			mode TEXT NOT NULL DEFAULT '',
			estimated_cost_usd REAL NOT NULL DEFAULT 0,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 200,
			error_class TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_timestamp ON request_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model_id)`,
		`CREATE TABLE IF NOT EXISTS vault_blob (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			salt BLOB NOT NULL,
			data TEXT NOT NULL DEFAULT '{}'
		)`,
	}
	for _, q := range queries {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Models

func (s *SQLiteStore) ListModels(ctx context.Context) ([]ModelRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, provider_id, weight, max_context_tokens, input_per_1k, output_per_1k, enabled FROM models`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []ModelRecord
	for rows.Next() {
		var m ModelRecord
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.Weight, &m.MaxContextTokens, &m.InputPer1K, &m.OutputPer1K, &m.Enabled); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func (s *SQLiteStore) GetModel(ctx context.Context, id string) (*ModelRecord, error) {
	var m ModelRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT id, provider_id, weight, max_context_tokens, input_per_1k, output_per_1k, enabled FROM models WHERE id = ?`, id).
		Scan(&m.ID, &m.ProviderID, &m.Weight, &m.MaxContextTokens, &m.InputPer1K, &m.OutputPer1K, &m.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *SQLiteStore) UpsertModel(ctx context.Context, m ModelRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO models (id, provider_id, weight, max_context_tokens, input_per_1k, output_per_1k, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   provider_id=excluded.provider_id,
		   weight=excluded.weight,
		   max_context_tokens=excluded.max_context_tokens,
		   input_per_1k=excluded.input_per_1k,
		   output_per_1k=excluded.output_per_1k,
		   enabled=excluded.enabled`,
		m.ID, m.ProviderID, m.Weight, m.MaxContextTokens, m.InputPer1K, m.OutputPer1K, m.Enabled)
	return err
}

func (s *SQLiteStore) DeleteModel(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	return err
}

// Providers

func (s *SQLiteStore) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, enabled, base_url, cred_store FROM providers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []ProviderRecord
	for rows.Next() {
		var p ProviderRecord
		if err := rows.Scan(&p.ID, &p.Type, &p.Enabled, &p.BaseURL, &p.CredStore); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *SQLiteStore) UpsertProvider(ctx context.Context, p ProviderRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO providers (id, type, enabled, base_url, cred_store)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   type=excluded.type,
		   enabled=excluded.enabled,
		   base_url=excluded.base_url,
		   cred_store=excluded.cred_store`,
		p.ID, p.Type, p.Enabled, p.BaseURL, p.CredStore)
	return err
}

func (s *SQLiteStore) DeleteProvider(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
	return err
}

// Request Logs

func (s *SQLiteStore) LogRequest(ctx context.Context, entry RequestLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_logs (timestamp, model_id, provider_id, mode, estimated_cost_usd, latency_ms, status_code, error_class, request_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.ModelID, entry.ProviderID, entry.Mode,
		entry.EstimatedCostUSD, entry.LatencyMs, entry.StatusCode, entry.ErrorClass, entry.RequestID)
	return err
}

// Vault persistence

func (s *SQLiteStore) SaveVaultBlob(ctx context.Context, salt []byte, data map[string]string) error {
	j, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal vault data: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO vault_blob (id, salt, data) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET salt=excluded.salt, data=excluded.data`,
		salt, string(j))
	return err
}

func (s *SQLiteStore) LoadVaultBlob(ctx context.Context) ([]byte, map[string]string, error) {
	var salt []byte
	var dataStr string
	err := s.db.QueryRowContext(ctx, `SELECT salt, data FROM vault_blob WHERE id = 1`).Scan(&salt, &dataStr)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, nil, fmt.Errorf("unmarshal vault data: %w", err)
	}
	return salt, data, nil
}

func (s *SQLiteStore) ListRequestLogs(ctx context.Context, limit int, offset int) ([]RequestLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, model_id, provider_id, mode, estimated_cost_usd, latency_ms, status_code, error_class, request_id
		 FROM request_logs ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RequestLog
	for rows.Next() {
		var l RequestLog
		var ts string
		if err := rows.Scan(&l.ID, &ts, &l.ModelID, &l.ProviderID, &l.Mode,
			&l.EstimatedCostUSD, &l.LatencyMs, &l.StatusCode, &l.ErrorClass, &l.RequestID); err != nil {
			return nil, err
		}
		l.Timestamp, _ = time.Parse(time.RFC3339, ts)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
