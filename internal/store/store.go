package store

import (
	"context"
	"time"
)

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

	// Vault persistence
	SaveVaultBlob(ctx context.Context, salt []byte, data map[string]string) error
	LoadVaultBlob(ctx context.Context) (salt []byte, data map[string]string, err error)

	// Routing config persistence
	SaveRoutingConfig(ctx context.Context, cfg RoutingConfig) error
	LoadRoutingConfig(ctx context.Context) (RoutingConfig, error)

	// Audit logging
	LogAudit(ctx context.Context, entry AuditEntry) error
	ListAuditLogs(ctx context.Context, limit int, offset int) ([]AuditEntry, error)

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
}
