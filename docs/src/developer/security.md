# Security Model

TokenHub implements security at multiple layers: credential encryption, client authentication, input validation, and audit logging.

## Credential Security

### Vault Encryption

Provider API keys are encrypted using AES-256-GCM:

1. Admin provides a vault password
2. Password + random salt → Argon2id key derivation → 256-bit encryption key
3. Each value is encrypted with a unique nonce
4. Encrypted values are stored in SQLite

**Argon2id Parameters** (per OWASP recommendations):
- Time: 3 iterations
- Memory: 64 MB
- Threads: 4
- Salt: 16 random bytes

### Key Material Handling

- Encryption keys exist only in memory while the vault is unlocked
- Auto-lock clears the key after 30 minutes of inactivity
- Vault salt is persisted in the database for key re-derivation
- Password rotation re-encrypts all values atomically

## Client Authentication

### API Key Security

- Keys are hashed with bcrypt (cost 10) before storage
- SHA-256 pre-hash allows keys longer than bcrypt's 72-byte input limit
- 5-minute validation cache reduces bcrypt overhead
- Plaintext is shown only once at creation/rotation

### Key Validation Flow

```
Request → Extract Bearer token → Check cache (5min TTL)
  ├── Cache hit → Check scopes → Allow/Deny
  └── Cache miss → Load by prefix → bcrypt verify → Check enabled → Check expiry
       ├── Valid → Update cache + last_used_at → Check scopes → Allow/Deny
       └── Invalid → 401 Unauthorized
```

## Input Validation

All API inputs are validated before processing:

### Chat/Plan Endpoints
- Messages array: required, non-empty
- `max_budget_usd`: 0-100 range
- `max_latency_ms`: 0-300000 range
- `min_weight`: 0-10 range
- Orchestration `iterations`: 0-10 range
- Orchestration `mode`: must be a known value

### Admin Endpoints
- Routing config mode: must be a known value
- Routing config budget/latency: same ranges as consumer API
- Model weight: reasonable range
- API key name: required

## Request Isolation

- Each request gets its own context with a unique request ID
- Provider API keys are never exposed to clients
- Client API key records are attached to context but not serialized in responses
- Request parameters are validated before forwarding to providers

## Audit Trail

All administrative mutations are logged:

```go
type AuditEntry struct {
    Timestamp time.Time
    Action    string  // e.g., "vault.unlock", "model.patch"
    Resource  string  // Resource identifier
    RequestID string  // For correlation
}
```

Auditable actions:
- Vault operations (lock, unlock, rotate)
- Provider CRUD
- Model CRUD
- API key lifecycle (create, rotate, update, revoke)
- Routing configuration changes

## Network Security

TokenHub itself does not implement TLS. In production:

1. **Use a reverse proxy** (nginx, Caddy, Traefik) for TLS termination
2. **Restrict admin endpoints** to internal networks or VPN
3. **Use CORS** appropriately (currently allows all origins for development)

## Recommendations

1. **Vault password**: Use a strong, unique password (16+ characters)
2. **API key rotation**: Rotate keys every 90 days (configurable via `rotation_days`)
3. **Network segmentation**: Keep admin endpoints behind a VPN or firewall
4. **TLS everywhere**: Terminate TLS at a reverse proxy in front of TokenHub
5. **Database backups**: SQLite file contains encrypted credentials and configuration
6. **Monitor audit logs**: Set up alerting on unexpected admin actions
