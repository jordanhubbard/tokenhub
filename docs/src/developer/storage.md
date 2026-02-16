# Storage Layer

TokenHub uses SQLite for persistence, providing a zero-dependency embedded database. The storage layer is defined by the `store.Store` interface and implemented by `store.SQLiteStore`.

## Interface

The `Store` interface (`internal/store/store.go`) provides methods for all persistence needs:

### Models
```go
UpsertModel(ctx, Model) error
GetModel(ctx, id) (*Model, error)
ListModels(ctx) ([]Model, error)
DeleteModel(ctx, id) error
```

### Providers
```go
UpsertProvider(ctx, Provider) error
ListProviders(ctx) ([]Provider, error)
DeleteProvider(ctx, id) error
```

### Request Logs
```go
LogRequest(ctx, RequestLog) error
ListRequestLogs(ctx, limit, offset) ([]RequestLog, error)
```

### Audit Logs
```go
LogAudit(ctx, AuditEntry) error
ListAuditLogs(ctx, limit, offset) ([]AuditEntry, error)
```

### Reward Entries
```go
LogReward(ctx, RewardEntry) error
ListRewardEntries(ctx, limit, offset) ([]RewardEntry, error)
GetRewardSummary(ctx) ([]RewardSummary, error)
```

### API Keys
```go
CreateAPIKey(ctx, APIKeyRecord) error
GetAPIKey(ctx, id) (*APIKeyRecord, error)
ListAPIKeys(ctx) ([]APIKeyRecord, error)
UpdateAPIKey(ctx, APIKeyRecord) error
DeleteAPIKey(ctx, id) error
```

### Vault Blob
```go
SaveVaultBlob(ctx, salt, data) error
LoadVaultBlob(ctx) (salt, data, error)
```

### Routing Configuration
```go
SaveRoutingConfig(ctx, RoutingConfig) error
LoadRoutingConfig(ctx) (RoutingConfig, error)
```

## Schema

The database schema is created and migrated in `sqlite.go`'s `Migrate()` method:

### `models`
```sql
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    weight INTEGER NOT NULL DEFAULT 5,
    max_context_tokens INTEGER NOT NULL DEFAULT 4096,
    input_per_1k REAL NOT NULL DEFAULT 0,
    output_per_1k REAL NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1
);
```

### `providers`
```sql
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    base_url TEXT NOT NULL DEFAULT '',
    cred_store TEXT NOT NULL DEFAULT 'env'
);
```

### `request_logs`
```sql
CREATE TABLE IF NOT EXISTS request_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    estimated_cost_usd REAL NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    error_class TEXT NOT NULL DEFAULT ''
);
```

### `audit_logs`
```sql
CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    action TEXT NOT NULL,
    resource TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT ''
);
```

### `reward_entries`
```sql
CREATE TABLE IF NOT EXISTS reward_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    estimated_tokens INTEGER NOT NULL DEFAULT 0,
    token_bucket TEXT NOT NULL DEFAULT '',
    latency_budget_ms REAL NOT NULL DEFAULT 0,
    latency_ms REAL NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    success INTEGER NOT NULL DEFAULT 0,
    error_class TEXT NOT NULL DEFAULT '',
    reward REAL NOT NULL DEFAULT 0
);
```

### `api_keys`
```sql
CREATE TABLE IF NOT EXISTS api_keys (
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
);
```

### `vault_blob`
```sql
CREATE TABLE IF NOT EXISTS vault_blob (
    id TEXT PRIMARY KEY DEFAULT 'singleton',
    salt TEXT,
    data_json TEXT
);
```

### `routing_config`
```sql
CREATE TABLE IF NOT EXISTS routing_config (
    id TEXT PRIMARY KEY DEFAULT 'default',
    default_mode TEXT NOT NULL DEFAULT '',
    default_max_budget_usd REAL NOT NULL DEFAULT 0,
    default_max_latency_ms INTEGER NOT NULL DEFAULT 0
);
```

## SQLite Configuration

The default DSN includes pragmas for performance:

```
file:/data/tokenhub.sqlite?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
```

- **busy_timeout**: Wait up to 5 seconds for locks instead of failing immediately
- **journal_mode(WAL)**: Write-Ahead Logging for concurrent read/write access

## TSDB

The time-series database (`internal/tsdb/`) uses a separate table in the same SQLite database:

```sql
CREATE TABLE IF NOT EXISTS tsdb_points (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,        -- Unix nanoseconds
    metric TEXT NOT NULL,
    model_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    value REAL NOT NULL
);
```

Features:
- Write buffering (batch size 100)
- Automatic retention pruning (default 7 days)
- Downsampling support (configurable step size in queries)
