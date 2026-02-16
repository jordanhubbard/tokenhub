# Admin UI

TokenHub includes a built-in single-page admin dashboard accessible at `/admin`. The UI is embedded in the binary — no separate frontend build or deployment is needed.

## Accessing the UI

Navigate to:

```
http://localhost:8080/admin
```

## Dashboard Panels

### Model Selection Graph

An interactive directed acyclic graph (DAG) showing the relationship between providers and models. Built with Cytoscape.js, it updates in real time as models are added, removed, or reconfigured.

- Provider nodes (colored by health state)
- Model nodes (sized by weight)
- Edges showing provider-model associations
- Click nodes for details

### Cost Trend Chart

A D3.js line chart showing cost trends over time, backed by the TSDB:

- Per-model cost breakdown
- Configurable time window
- Hover for exact values

### Model Leaderboard

A ranked table of models by performance:

- Success rate
- Average latency
- Total cost
- Request count

### Provider Health

Real-time provider health display:

- State badges: **Healthy** (green), **Degraded** (yellow), **Down** (red)
- Consecutive error count
- Last success timestamp
- Average latency

### Vault Controls

- **Lock indicator**: Shows whether the vault is locked or unlocked
- **Unlock button**: Enter vault password to unlock
- **Lock button**: Lock the vault immediately
- **Rotate button**: Change the vault password

### Provider Management

CRUD interface for providers:

- Add new provider (type, base URL, credential store, API key)
- Edit existing providers
- Enable/disable toggle
- Delete providers

### Model Management

CRUD interface for models:

- Add new model (ID, provider, weight, context, pricing)
- Weight slider (0-10)
- Enable/disable toggle
- Edit pricing per 1K tokens
- Delete models

### Routing Configuration

Set server-wide routing defaults:

- Default mode selector (cheap, normal, high_confidence, planning, adversarial)
- Budget input (USD)
- Latency input (milliseconds)
- Save button with validation

### API Keys

Key management interface:

- Create new keys (name, scopes, rotation, expiry)
- One-time key display modal with copy button
- Rotate keys (with one-time new key display)
- Enable/disable toggle
- Revoke (delete) keys
- Table showing: name, prefix, scopes, created, last used, expires, rotation days, status

### Audit Log

Paginated audit trail viewer:

- Action type filter
- Timestamp, action, resource ID
- Request ID for correlation

### Request Log

Paginated request history:

- Model, provider, mode columns
- Latency, cost, status code
- Error class (for failed requests)
- Pagination controls

### Workflows (Temporal)

When Temporal is enabled, shows workflow execution history:

- Workflow ID, type, status
- Start time, duration
- Status badges: Running (blue), Completed (green), Failed (red)
- Click to expand activity history

### Events Stream

Live event feed from the SSE endpoint:

- Real-time route success/failure events
- Auto-scrolling event list
- Model, provider, latency, cost for each event

## Static Assets

Static assets (CSS, JavaScript) are served from `/_assets/` to avoid conflicts with the `/admin/v1` API prefix. All assets are embedded in the binary via Go's `embed` package.

## Customization

The admin UI is a single `index.html` file located at `web/index.html` in the source tree. To customize:

1. Edit `web/index.html`
2. Rebuild the binary (`make build`)
3. The updated UI is embedded automatically
