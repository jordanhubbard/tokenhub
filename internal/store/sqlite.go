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
		_ = db.Close()
		return nil, fmt.Errorf("sqlite pragmas: %w", err)
	}
	// SQLite only supports one writer at a time. Limit connections to avoid
	// contention and keep a small idle pool for read concurrency.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	return &SQLiteStore{db: db}, nil
}

// DB returns the underlying sql.DB handle (used by TSDB).
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
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
		`CREATE TABLE IF NOT EXISTS routing_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			default_mode TEXT NOT NULL DEFAULT 'normal',
			default_max_budget_usd REAL NOT NULL DEFAULT 0.05,
			default_max_latency_ms INTEGER NOT NULL DEFAULT 20000
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			action TEXT NOT NULL,
			resource TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON audit_logs(timestamp)`,
		`CREATE TABLE IF NOT EXISTS reward_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			request_id TEXT,
			model_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			mode TEXT,
			estimated_tokens INTEGER,
			token_bucket TEXT,
			latency_budget_ms INTEGER,
			latency_ms REAL,
			cost_usd REAL,
			success INTEGER,
			error_class TEXT,
			reward REAL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reward_logs_ts ON reward_logs(timestamp)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			name TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '["chat","plan"]',
			created_at TEXT NOT NULL,
			last_used_at TEXT,
			expires_at TEXT,
			rotation_days INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix)`,
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
	defer func() { _ = rows.Close() }()

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
	defer func() { _ = rows.Close() }()

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

// Routing Config

func (s *SQLiteStore) SaveRoutingConfig(ctx context.Context, cfg RoutingConfig) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO routing_config (id, default_mode, default_max_budget_usd, default_max_latency_ms)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   default_mode=excluded.default_mode,
		   default_max_budget_usd=excluded.default_max_budget_usd,
		   default_max_latency_ms=excluded.default_max_latency_ms`,
		cfg.DefaultMode, cfg.DefaultMaxBudgetUSD, cfg.DefaultMaxLatencyMs)
	return err
}

func (s *SQLiteStore) LoadRoutingConfig(ctx context.Context) (RoutingConfig, error) {
	var cfg RoutingConfig
	err := s.db.QueryRowContext(ctx,
		`SELECT default_mode, default_max_budget_usd, default_max_latency_ms FROM routing_config WHERE id = 1`).
		Scan(&cfg.DefaultMode, &cfg.DefaultMaxBudgetUSD, &cfg.DefaultMaxLatencyMs)
	if err != nil {
		// Return zero value if no row (not an error).
		return RoutingConfig{}, nil
	}
	return cfg, nil
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
	defer func() { _ = rows.Close() }()

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

// Audit Logs

func (s *SQLiteStore) LogAudit(ctx context.Context, entry AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs (timestamp, action, resource, detail, request_id)
		 VALUES (?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.Action, entry.Resource, entry.Detail, entry.RequestID)
	return err
}

func (s *SQLiteStore) ListAuditLogs(ctx context.Context, limit int, offset int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, action, resource, detail, request_id
		 FROM audit_logs ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var logs []AuditEntry
	for rows.Next() {
		var l AuditEntry
		var ts string
		if err := rows.Scan(&l.ID, &ts, &l.Action, &l.Resource, &l.Detail, &l.RequestID); err != nil {
			return nil, err
		}
		l.Timestamp, _ = time.Parse(time.RFC3339, ts)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// Reward Logs

func (s *SQLiteStore) LogReward(ctx context.Context, entry RewardEntry) error {
	successInt := 0
	if entry.Success {
		successInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reward_logs (timestamp, request_id, model_id, provider_id, mode,
		 estimated_tokens, token_bucket, latency_budget_ms, latency_ms, cost_usd,
		 success, error_class, reward)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.RequestID, entry.ModelID, entry.ProviderID, entry.Mode,
		entry.EstimatedTokens, entry.TokenBucket, entry.LatencyBudgetMs, entry.LatencyMs,
		entry.CostUSD, successInt, entry.ErrorClass, entry.Reward)
	return err
}

// API Keys

func (s *SQLiteStore) CreateAPIKey(ctx context.Context, key APIKeyRecord) error {
	var lastUsed, expires *string
	if key.LastUsedAt != nil {
		t := key.LastUsedAt.UTC().Format(time.RFC3339)
		lastUsed = &t
	}
	if key.ExpiresAt != nil {
		t := key.ExpiresAt.UTC().Format(time.RFC3339)
		expires = &t
	}
	enabledInt := 0
	if key.Enabled {
		enabledInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_hash, key_prefix, name, scopes, created_at, last_used_at, expires_at, rotation_days, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.KeyHash, key.KeyPrefix, key.Name, key.Scopes,
		key.CreatedAt.UTC().Format(time.RFC3339), lastUsed, expires,
		key.RotationDays, enabledInt)
	return err
}

func (s *SQLiteStore) GetAPIKey(ctx context.Context, id string) (*APIKeyRecord, error) {
	var k APIKeyRecord
	var createdAt string
	var lastUsed, expires sql.NullString
	var enabledInt int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, key_hash, key_prefix, name, scopes, created_at, last_used_at, expires_at, rotation_days, enabled
		 FROM api_keys WHERE id = ?`, id).
		Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Scopes,
			&createdAt, &lastUsed, &expires, &k.RotationDays, &enabledInt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339, lastUsed.String)
		k.LastUsedAt = &t
	}
	if expires.Valid {
		t, _ := time.Parse(time.RFC3339, expires.String)
		k.ExpiresAt = &t
	}
	k.Enabled = enabledInt != 0
	return &k, nil
}

func (s *SQLiteStore) GetAPIKeysByPrefix(ctx context.Context, prefix string) ([]APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key_hash, key_prefix, name, scopes, created_at, last_used_at, expires_at, rotation_days, enabled
		 FROM api_keys WHERE key_prefix = ? AND enabled = 1`, prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var keys []APIKeyRecord
	for rows.Next() {
		var k APIKeyRecord
		var createdAt string
		var lastUsed, expires sql.NullString
		var enabledInt int
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Scopes,
			&createdAt, &lastUsed, &expires, &k.RotationDays, &enabledInt); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastUsed.Valid {
			t, _ := time.Parse(time.RFC3339, lastUsed.String)
			k.LastUsedAt = &t
		}
		if expires.Valid {
			t, _ := time.Parse(time.RFC3339, expires.String)
			k.ExpiresAt = &t
		}
		k.Enabled = enabledInt != 0
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *SQLiteStore) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key_hash, key_prefix, name, scopes, created_at, last_used_at, expires_at, rotation_days, enabled
		 FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var keys []APIKeyRecord
	for rows.Next() {
		var k APIKeyRecord
		var createdAt string
		var lastUsed, expires sql.NullString
		var enabledInt int
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Scopes,
			&createdAt, &lastUsed, &expires, &k.RotationDays, &enabledInt); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastUsed.Valid {
			t, _ := time.Parse(time.RFC3339, lastUsed.String)
			k.LastUsedAt = &t
		}
		if expires.Valid {
			t, _ := time.Parse(time.RFC3339, expires.String)
			k.ExpiresAt = &t
		}
		k.Enabled = enabledInt != 0
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *SQLiteStore) UpdateAPIKey(ctx context.Context, key APIKeyRecord) error {
	var lastUsed, expires *string
	if key.LastUsedAt != nil {
		t := key.LastUsedAt.UTC().Format(time.RFC3339)
		lastUsed = &t
	}
	if key.ExpiresAt != nil {
		t := key.ExpiresAt.UTC().Format(time.RFC3339)
		expires = &t
	}
	enabledInt := 0
	if key.Enabled {
		enabledInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET key_hash=?, key_prefix=?, name=?, scopes=?, last_used_at=?, expires_at=?, rotation_days=?, enabled=?
		 WHERE id=?`,
		key.KeyHash, key.KeyPrefix, key.Name, key.Scopes,
		lastUsed, expires, key.RotationDays, enabledInt, key.ID)
	return err
}

func (s *SQLiteStore) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) ListRewards(ctx context.Context, limit int, offset int) ([]RewardEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, request_id, model_id, provider_id, mode,
		 estimated_tokens, token_bucket, latency_budget_ms, latency_ms, cost_usd,
		 success, error_class, reward
		 FROM reward_logs ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var logs []RewardEntry
	for rows.Next() {
		var l RewardEntry
		var ts string
		var successInt int
		if err := rows.Scan(&l.ID, &ts, &l.RequestID, &l.ModelID, &l.ProviderID, &l.Mode,
			&l.EstimatedTokens, &l.TokenBucket, &l.LatencyBudgetMs, &l.LatencyMs,
			&l.CostUSD, &successInt, &l.ErrorClass, &l.Reward); err != nil {
			return nil, err
		}
		l.Timestamp, _ = time.Parse(time.RFC3339, ts)
		l.Success = successInt != 0
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (s *SQLiteStore) GetRewardSummary(ctx context.Context) ([]RewardSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model_id, token_bucket,
		 COUNT(*) as count,
		 SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) as successes,
		 SUM(reward) as sum_reward
		 FROM reward_logs
		 GROUP BY model_id, token_bucket`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var summaries []RewardSummary
	for rows.Next() {
		var s RewardSummary
		if err := rows.Scan(&s.ModelID, &s.TokenBucket, &s.Count, &s.Successes, &s.SumReward); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}
