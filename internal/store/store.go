package store

import (
	"context"
	"time"
)

// APIKeyRecord is the persisted form of a client API key.
type APIKeyRecord struct {
	ID               string     `json:"id"`
	KeyHash          string     `json:"-"`                     // bcrypt hash, never serialized
	KeyPrefix        string     `json:"key_prefix"`            // first 8 chars for identification
	Name             string     `json:"name"`
	Scopes           string     `json:"scopes"`                // JSON array stored as text
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RotationDays     int        `json:"rotation_days"`          // 0 = manual rotation only
	MonthlyBudgetUSD float64    `json:"monthly_budget_usd"`     // 0 = unlimited
	Enabled          bool       `json:"enabled"`
}

// Store defines the persistence interface for tokenhub.
type Store interface {
	// Models
	ListModels(ctx context.Context) ([]ModelRecord, error)
	GetModel(ctx context.Context, id string) (*ModelRecord, error)
	UpsertModel(ctx context.Context, m ModelRecord) error
	DeleteModel(ctx context.Context, id string) error

	// Providers
	ListProviders(ctx context.Context) ([]ProviderRecord, error)
	UpsertProvider(ctx context.Context, p ProviderRecord) error
	DeleteProvider(ctx context.Context, id string) error

	// Request log (for audit and dashboard)
	LogRequest(ctx context.Context, entry RequestLog) error
	ListRequestLogs(ctx context.Context, limit int, offset int) ([]RequestLog, error)
	GetMonthlySpend(ctx context.Context, apiKeyID string) (float64, error)

	// Vault persistence
	SaveVaultBlob(ctx context.Context, salt []byte, data map[string]string) error
	LoadVaultBlob(ctx context.Context) (salt []byte, data map[string]string, err error)

	// Routing config persistence
	SaveRoutingConfig(ctx context.Context, cfg RoutingConfig) error
	LoadRoutingConfig(ctx context.Context) (RoutingConfig, error)

	// Audit logging
	LogAudit(ctx context.Context, entry AuditEntry) error
	ListAuditLogs(ctx context.Context, limit int, offset int) ([]AuditEntry, error)

	// Reward logging (contextual bandit data collection)
	LogReward(ctx context.Context, entry RewardEntry) error
	ListRewards(ctx context.Context, limit int, offset int) ([]RewardEntry, error)
	GetRewardSummary(ctx context.Context) ([]RewardSummary, error)

	// API key management
	CreateAPIKey(ctx context.Context, key APIKeyRecord) error
	GetAPIKey(ctx context.Context, id string) (*APIKeyRecord, error)
	GetAPIKeysByPrefix(ctx context.Context, prefix string) ([]APIKeyRecord, error)
	ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error)
	ListExpiredRotationKeys(ctx context.Context) ([]APIKeyRecord, error)
	UpdateAPIKey(ctx context.Context, key APIKeyRecord) error
	DeleteAPIKey(ctx context.Context, id string) error

	// Log retention
	PruneOldLogs(ctx context.Context, retention time.Duration) (int64, error)

	// Schema lifecycle
	Migrate(ctx context.Context) error
	Close() error
}

// ModelRecord is the persisted form of a model configuration.
type ModelRecord struct {
	ID               string  `json:"id"`
	ProviderID       string  `json:"provider_id"`
	Weight           int     `json:"weight"`
	MaxContextTokens int     `json:"max_context_tokens"`
	InputPer1K       float64 `json:"input_per_1k"`
	OutputPer1K      float64 `json:"output_per_1k"`
	Enabled          bool    `json:"enabled"`
}

// ProviderRecord is the persisted form of a provider configuration.
type ProviderRecord struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // openai, anthropic, vllm
	Enabled   bool   `json:"enabled"`
	BaseURL   string `json:"base_url"`
	CredStore string `json:"cred_store"` // env, vault, none
}

// RoutingConfig holds persisted routing policy defaults.
type RoutingConfig struct {
	DefaultMode         string  `json:"default_mode"`
	DefaultMaxBudgetUSD float64 `json:"default_max_budget_usd"`
	DefaultMaxLatencyMs int     `json:"default_max_latency_ms"`
}

// AuditEntry captures an admin mutation for audit trail.
type AuditEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`              // e.g. "model.upsert", "provider.delete", "vault.unlock"
	Resource  string    `json:"resource"`             // e.g. "gpt-4", "openai"
	Detail    string    `json:"detail,omitempty"`     // optional JSON with change details
	RequestID string    `json:"request_id,omitempty"` // correlates to HTTP request ID
}

// RequestLog captures a single routed request for audit/dashboard.
type RequestLog struct {
	ID               int64     `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	ModelID          string    `json:"model_id"`
	ProviderID       string    `json:"provider_id"`
	Mode             string    `json:"mode"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	LatencyMs        int64     `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	ErrorClass       string    `json:"error_class,omitempty"`
	RequestID        string    `json:"request_id,omitempty"`
	APIKeyID         string    `json:"api_key_id,omitempty"`
}

// RewardSummary aggregates reward data per model per token bucket for
// Thompson Sampling parameter estimation.
type RewardSummary struct {
	ModelID     string  `json:"model_id"`
	TokenBucket string  `json:"token_bucket"`
	Count       int     `json:"count"`
	Successes   int     `json:"successes"`
	SumReward   float64 `json:"sum_reward"`
}

// RewardEntry captures the features and outcome of a routing decision
// for contextual bandit reward logging (RL-based routing data collection).
type RewardEntry struct {
	ID              int64     `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	RequestID       string    `json:"request_id,omitempty"`
	ModelID         string    `json:"model_id"`
	ProviderID      string    `json:"provider_id"`
	Mode            string    `json:"mode"`
	EstimatedTokens int       `json:"estimated_tokens"`
	TokenBucket     string    `json:"token_bucket"`
	LatencyBudgetMs int       `json:"latency_budget_ms"`
	LatencyMs       float64   `json:"latency_ms"`
	CostUSD         float64   `json:"cost_usd"`
	Success         bool      `json:"success"`
	ErrorClass      string    `json:"error_class,omitempty"`
	Reward          float64   `json:"reward"`
}
