# Security & Vault Requirements

## Threat model (initial)

We assume:
- Tokenhub host may be exposed to the internet
- Attackers may gain access to logs
- Attackers may read the DB at rest (snapshot, backup, stolen volume)
- You want to ensure provider API keys are not accessible without admin password

We do NOT attempt to defend against:
- fully compromised host at runtime with root access (they can scrape memory)
- malicious admin

## Requirements

### 1) Escrow credential encryption
- Credentials stored encrypted-at-rest in DB
- Use AES-256-GCM for encryption
- Use Argon2id for key derivation
  - store salt and KDF parameters in DB (non-secret)
- Master password never stored
- Vault key only exists in memory while unlocked
- Support explicit relock

### 2) UI unlock flow
- Admin enters password in UI
- UI sends password only over TLS to tokenhub
- Tokenhub derives key and unlocks vault
- Tokenhub returns success, UI retains no password beyond current session

### 3) Redaction
- No request body logs by default
- If request logging enabled: redact any known secret patterns and explicit fields
- Never log Authorization headers
- Never echo env var values in diagnostics

### 4) Environment mode
- Environment variables are treated as already-secret
- Tokenhub must not print them, even in debug

## Nice-to-haves
- Optional integration with external secret stores (k8s secrets, AWS Secrets Manager, Vault)
- Audit logging (admin actions)
- mTLS / OAuth

