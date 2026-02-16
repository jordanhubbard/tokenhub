# Authentication

All requests to TokenHub's consumer API (`/v1/*`) require authentication via API keys.

## API Key Format

TokenHub API keys follow this format:

```
tokenhub_<64 hex characters>
```

Example: `tokenhub_a1b2c3d4e5f6789012345678abcdef0123456789abcdef0123456789abcdef01`

## Using API Keys

Include the key in the `Authorization` header as a Bearer token:

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Authorization: Bearer tokenhub_a1b2c3d4..." \
  -H "Content-Type: application/json" \
  -d '{"request": {"messages": [{"role": "user", "content": "Hello"}]}}'
```

## Scopes

Each API key has scopes that control which endpoints it can access:

| Scope | Endpoint | Description |
|-------|----------|-------------|
| `chat` | `POST /v1/chat` | Chat completion requests |
| `plan` | `POST /v1/plan` | Orchestrated planning requests |

A key with scopes `["chat", "plan"]` can access both endpoints. A key with only `["chat"]` receives a `403 Forbidden` when calling `/v1/plan`.

If scopes are empty (`[]`), the key has access to all endpoints.

## Error Responses

| Status | Message | Cause |
|--------|---------|-------|
| 401 | `"missing or invalid api key"` | No Authorization header, invalid format, wrong key, expired, or disabled |
| 403 | `"scope not allowed"` | Valid key but lacks the required scope |

## Key Lifecycle

1. **Created** by an administrator via the admin API or UI
2. **Distributed** to the client application (plaintext shown only once at creation)
3. **Used** by the client for all `/v1` requests
4. **Rotated** periodically (manually or on a configured schedule)
5. **Revoked** when no longer needed

Keys can be configured with:
- **Expiration**: Automatic expiry after a set duration
- **Rotation schedule**: Recommended rotation period in days
- **Enable/disable**: Temporarily deactivate without deleting

## Security Properties

- **Plaintext is never stored**: Only a bcrypt hash is persisted
- **Shown once**: The plaintext key is returned only at creation and rotation
- **Provider isolation**: Clients authenticate with TokenHub keys. Provider API keys are stored encrypted in the vault and never exposed.
- **Validation cache**: A 5-minute TTL cache reduces bcrypt overhead without compromising security

See [API Key Management](../admin/api-keys.md) for the administrator's guide to creating and managing keys.
