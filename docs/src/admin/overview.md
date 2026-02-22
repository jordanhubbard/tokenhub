# Administrator Guide Overview

This section covers how to configure, manage, and monitor a TokenHub deployment.

## Administration Model

TokenHub uses a three-tier security model:

1. **Admin token** (`TOKENHUB_ADMIN_TOKEN`): Authenticates access to the admin API (`/admin/v1/*`) and the admin dashboard. The UI requires the token at login; all admin API calls include it as `Authorization: Bearer <token>`. Retrieve it with `tokenhubctl admin-token`.
2. **Vault password**: A separate secret that *encrypts provider API keys at rest*. Even a valid admin token cannot decrypt the vault — the vault must be explicitly unlocked after each restart (or set `TOKENHUB_VAULT_PASSWORD` for auto-unlock).
3. **API keys**: Issued to client applications for `/v1` endpoint access. Managed via the admin API or UI.

In production, always set `TOKENHUB_ADMIN_TOKEN` and restrict network access to `/admin/*` at the firewall, VPN, or reverse proxy level.

## Administration Tools

### Admin UI

The built-in web dashboard at `/admin` provides a graphical interface for all admin operations. See [Admin UI](ui.md).

### tokenhubctl

A command-line tool for scripting and quick administration. Covers all admin API operations. See [tokenhubctl CLI](tokenhubctl.md).

### curl / Admin API

All operations are available via the REST API at `/admin/v1/*`. See [API Reference](../reference/api.md).

## Admin Endpoints

| Category | Endpoints | Purpose |
|----------|-----------|---------|
| Vault | `/admin/v1/vault/*` | Lock, unlock, rotate vault password |
| Providers | `/admin/v1/providers` | Register, edit, and manage LLM providers |
| Models | `/admin/v1/models` | Register, edit, and manage model configurations |
| Discovery | `/admin/v1/providers/{id}/discover` | Discover models from a provider's API |
| Simulation | `/admin/v1/routing/simulate` | What-if routing simulation |
| Routing | `/admin/v1/routing-config` | Set default routing policy |
| API Keys | `/admin/v1/apikeys` | Create, rotate, revoke client API keys |
| Health | `/admin/v1/health` | View provider health status |
| Stats | `/admin/v1/stats` | View aggregated request statistics |
| Logs | `/admin/v1/logs` | View request logs |
| Audit | `/admin/v1/audit` | View audit trail |
| Rewards | `/admin/v1/rewards` | View contextual bandit reward data |
| Engine | `/admin/v1/engine/models` | View runtime model registry and adapter info |
| TSDB | `/admin/v1/tsdb/*` | Query time-series metrics |
| Workflows | `/admin/v1/workflows` | View Temporal workflow executions |
| Events | `/admin/v1/events` | SSE stream of real-time events |

## Sections

- [Vault & Credentials](vault.md) — Encrypted credential storage
- [Provider Management](providers.md) — Configure LLM providers
- [Model Management](models.md) — Configure model registry
- [Routing Configuration](routing.md) — Tune model selection
- [API Key Management](api-keys.md) — Issue and manage client keys
- [Monitoring & Observability](monitoring.md) — Health, metrics, logs, and alerts
- [Admin UI](ui.md) — Built-in dashboard
- [tokenhubctl CLI](tokenhubctl.md) — Command-line administration
