# Administrator Guide Overview

This section covers how to configure, manage, and monitor a TokenHub deployment.

## Administration Model

TokenHub uses a two-tier security model:

1. **Vault password**: Protects encrypted provider credentials. Required to unlock the vault after restart.
2. **API keys**: Issued to client applications for `/v1` endpoint access. Managed via admin API.

Admin endpoints (`/admin/v1/*`) are not protected by API key authentication. In production, restrict access to admin endpoints at the network level (firewall, VPN, reverse proxy).

## Admin Endpoints

| Category | Endpoints | Purpose |
|----------|-----------|---------|
| Vault | `/admin/v1/vault/*` | Lock, unlock, rotate vault password |
| Providers | `/admin/v1/providers` | Register and manage LLM providers |
| Models | `/admin/v1/models` | Register and manage model configurations |
| Routing | `/admin/v1/routing-config` | Set default routing policy |
| API Keys | `/admin/v1/apikeys` | Create, rotate, revoke client API keys |
| Health | `/admin/v1/health` | View provider health status |
| Stats | `/admin/v1/stats` | View aggregated request statistics |
| Logs | `/admin/v1/logs` | View request logs |
| Audit | `/admin/v1/audit` | View audit trail |
| Rewards | `/admin/v1/rewards` | View contextual bandit reward data |
| Engine | `/admin/v1/engine/models` | View runtime model registry |
| TSDB | `/admin/v1/tsdb/*` | Query time-series metrics |
| Workflows | `/admin/v1/workflows` | View Temporal workflow executions |
| Events | `/admin/v1/events` | SSE stream of real-time events |

## Admin UI

TokenHub includes a built-in single-page admin dashboard at `/admin`. It provides a graphical interface for all admin operations including:

- Model selection visualization (interactive graph)
- Cost trend charts
- Provider health monitoring
- All CRUD operations (providers, models, keys, routing)
- Request and audit log viewing
- Real-time event stream

See [Admin UI](ui.md) for details.

## Sections

- [Vault & Credentials](vault.md) — Encrypted credential storage
- [Provider Management](providers.md) — Configure LLM providers
- [Model Management](models.md) — Configure model registry
- [Routing Configuration](routing.md) — Tune model selection
- [API Key Management](api-keys.md) — Issue and manage client keys
- [Monitoring & Observability](monitoring.md) — Health, metrics, logs, and alerts
- [Admin UI](ui.md) — Built-in dashboard
