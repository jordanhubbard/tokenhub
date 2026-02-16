# API Key Management

TokenHub issues its own API keys to client applications. Provider API keys are escrowed in the vault — clients never see them. This provides a clean separation between client authentication and provider credentials.

## Key Properties

| Property | Description |
|----------|-------------|
| **ID** | 16-character hex identifier |
| **Prefix** | First 8 characters of the key for identification |
| **Name** | Human-readable label |
| **Scopes** | JSON array of allowed endpoints (`chat`, `plan`) |
| **Rotation days** | Recommended rotation period (0 = manual only) |
| **Expiration** | Optional automatic expiry |
| **Enabled** | Active/inactive toggle |

## Operations

### Create a Key

```bash
curl -X POST http://localhost:8080/admin/v1/apikeys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "production-backend",
    "scopes": "[\"chat\",\"plan\"]",
    "rotation_days": 90,
    "expires_in": "2160h"
  }'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Human-readable name for the key |
| `scopes` | string | No | JSON array of scopes (default: `["chat","plan"]`) |
| `rotation_days` | int | No | Recommended rotation period in days (default: 0) |
| `expires_in` | string | No | Go duration for expiry (e.g., `720h` for 30 days) |

Response:
```json
{
  "ok": true,
  "key": "tokenhub_a1b2c3d4e5f6789012345678abcdef0123456789abcdef0123456789abcdef01",
  "id": "a1b2c3d4e5f6g7h8",
  "prefix": "tokenhub_a1b2c3d4",
  "warning": "Store this key securely. It will not be shown again."
}
```

> **Important**: The plaintext key is returned only at creation time. Store it securely before closing the response.

### List Keys

```bash
curl http://localhost:8080/admin/v1/apikeys
```

Response:
```json
[
  {
    "id": "a1b2c3d4e5f6g7h8",
    "key_prefix": "tokenhub_a1b2c3d4",
    "name": "production-backend",
    "scopes": "[\"chat\",\"plan\"]",
    "created_at": "2026-02-16T10:00:00Z",
    "last_used_at": "2026-02-16T12:34:56Z",
    "expires_at": "2026-05-16T10:00:00Z",
    "rotation_days": 90,
    "enabled": true
  }
]
```

Plaintext keys are never shown in list responses.

### Rotate a Key

Generate a new key value while keeping the same ID and configuration:

```bash
curl -X POST http://localhost:8080/admin/v1/apikeys/a1b2c3d4e5f6g7h8/rotate
```

Response:
```json
{
  "ok": true,
  "key": "tokenhub_<new-64-hex-chars>",
  "warning": "Store this key securely. It will not be shown again."
}
```

The old key immediately becomes invalid. Distribute the new key to all clients before rotating.

### Update a Key

Modify key metadata without changing the key value:

```bash
curl -X PATCH http://localhost:8080/admin/v1/apikeys/a1b2c3d4e5f6g7h8 \
  -H "Content-Type: application/json" \
  -d '{
    "name": "production-backend-v2",
    "scopes": "[\"chat\"]",
    "enabled": true,
    "rotation_days": 60
  }'
```

All fields are optional — only specified fields are updated.

### Revoke (Delete) a Key

```bash
curl -X DELETE http://localhost:8080/admin/v1/apikeys/a1b2c3d4e5f6g7h8
```

This permanently removes the key. It cannot be recovered.

## Security Details

### Storage

- Keys are hashed with **bcrypt** (cost factor 10) before storage
- To reduce bcrypt overhead per-request, validated keys are cached for **5 minutes**
- The SHA-256 digest of the plaintext is bcrypt-hashed (allowing keys longer than bcrypt's 72-byte limit)

### Validation Flow

1. Extract `Bearer tokenhub_...` from Authorization header
2. Extract the key prefix (first 8 chars after `tokenhub_`)
3. Check the validation cache (5-minute TTL)
4. If not cached: load record by prefix, bcrypt-verify, check enabled + expiry
5. Update `last_used_at` timestamp
6. Verify the key's scopes include the requested endpoint

### Scopes

| Scope | Protects |
|-------|----------|
| `chat` | `POST /v1/chat` |
| `plan` | `POST /v1/plan` |

An empty scopes array `[]` grants access to all endpoints.

## Audit Trail

All key management operations are logged:
- `apikey.create` — New key created
- `apikey.rotate` — Key rotated (new value generated)
- `apikey.update` — Key metadata changed
- `apikey.revoke` — Key deleted

## Best Practices

1. **Name keys descriptively**: Use names like `staging-backend`, `prod-api-v2`, `data-pipeline`
2. **Use minimal scopes**: If a client only needs chat, don't grant plan access
3. **Set rotation schedules**: Configure `rotation_days` as a reminder to rotate
4. **Set expiration for temporary keys**: Use `expires_in` for keys issued to contractors or experiments
5. **Monitor last_used_at**: Keys not used for extended periods may be candidates for revocation
6. **Rotate after incidents**: If a key may have been compromised, rotate immediately
