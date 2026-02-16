# Vault & Credentials

TokenHub includes an AES-256-GCM encrypted vault for storing provider API keys securely. Provider credentials are encrypted at rest and only decrypted in memory when the vault is unlocked.

## How It Works

1. An administrator sets a **vault password** when first configuring TokenHub
2. The password is run through **Argon2id** key derivation (OWASP-recommended parameters) to produce an encryption key
3. Provider API keys are encrypted with **AES-256-GCM** and stored in SQLite
4. A random **salt** is generated per vault instance and persisted alongside the encrypted data
5. After server restart, the vault must be **unlocked** with the same password before provider requests can be made

## Vault States

| State | Description |
|-------|-------------|
| **Locked** | Credentials encrypted; provider requests will fail |
| **Unlocked** | Credentials decrypted in memory; requests are served normally |

## Operations

### Unlock the Vault

```bash
curl -X POST http://localhost:8080/admin/v1/vault/unlock \
  -H "Content-Type: application/json" \
  -d '{"admin_password": "your-secure-password"}'
```

Response:
```json
{"ok": true}
```

### Lock the Vault

```bash
curl -X POST http://localhost:8080/admin/v1/vault/lock
```

Response:
```json
{"ok": true, "already_locked": false}
```

### Rotate the Vault Password

Re-encrypts all stored credentials with a new password:

```bash
curl -X POST http://localhost:8080/admin/v1/vault/rotate \
  -H "Content-Type: application/json" \
  -d '{
    "old_password": "current-password",
    "new_password": "new-secure-password"
  }'
```

This operation is atomic — all credentials are re-encrypted in a single transaction.

## Auto-Lock

The vault automatically locks after 30 minutes of inactivity. Every successful credential access resets the timer.

When the vault auto-locks:
- In-flight requests that have already retrieved credentials continue normally
- New requests will fail with a provider error until the vault is unlocked again
- An audit log entry is recorded

## Credential Storage

When you register a provider with `cred_store: "vault"`, TokenHub stores the API key encrypted in the vault under the key `provider:{provider_id}:api_key`.

The credential lifecycle:
1. Admin provides API key when creating/updating a provider
2. Key is encrypted and stored in the vault
3. Key is also persisted (encrypted) in the database for recovery after restart
4. When the vault is unlocked, the salt and encrypted blob are loaded from the database
5. Keys are decrypted only in memory

## Security Parameters

| Parameter | Value |
|-----------|-------|
| Encryption | AES-256-GCM |
| Key derivation | Argon2id |
| Argon2id time | 3 iterations |
| Argon2id memory | 64 MB |
| Argon2id threads | 4 |
| Salt | 16 bytes, random per vault |
| Auto-lock timeout | 30 minutes |

## Best Practices

1. **Use a strong vault password**: At least 16 characters with mixed case, numbers, and symbols
2. **Rotate regularly**: Use the rotate endpoint to change the vault password periodically
3. **Monitor auto-lock**: Set up alerts if the vault locks unexpectedly during business hours
4. **Backup the database**: The vault salt and encrypted blob are stored in SQLite. Back up the database file to ensure credential recovery
5. **Network isolation**: Restrict access to vault admin endpoints to trusted networks
