# Admin UI

TokenHub includes a built-in single-page admin dashboard accessible at `/admin`. The UI is embedded in the binary — no separate frontend build or deployment is needed.

## Accessing the UI

Navigate to:

```
http://localhost:8080/admin
```

The root URL (`http://localhost:8080/`) automatically redirects to `/admin/`.

## Cache Busting

The admin HTML is served with `Cache-Control: no-cache, must-revalidate` and an `ETag` derived from the content hash. Static assets under `/_assets/` are served with `immutable` cache headers and versioned URLs (`?v=<hash>`), ensuring browsers always get fresh assets after a rebuild without manual cache clearing.

## Dashboard Panels

### Vault Controls

The vault panel adapts to three states:

- **First-Time Setup**: When the vault has never been initialized, the UI displays a clear "Set a Vault Password" prompt with password requirements (minimum 8 characters) and a confirmation field.
- **Locked**: When the vault has been initialized but is locked, the UI shows an "Enter Vault Password" prompt to unlock.
- **Unlocked**: Shows the unlocked status with Lock and Rotate password buttons.

### Provider Management

Full CRUD interface for providers:

- **Setup Wizard**: Multi-step guided onboarding for new providers — select type (OpenAI/Anthropic/vLLM), enter base URL and API key, test the connection, then discover and register available models.
- **Provider Table**: Shows all providers from both the persistent store and runtime engine (env vars, credentials file). Runtime-only providers are indicated with a badge. Base URLs are derived from adapter health endpoints when not stored.
- **Edit Modal**: Click "Edit" on any provider to change type, base URL, API key, or enabled state.
- **Discover**: Query a provider's API to find available models and register them.
- **Delete**: Remove a provider from the store.

### Model Management

Full CRUD interface for models:

- **Add Model Form**: Create a new model with provider, weight, context window, and pricing.
- **Model Table**: Shows all models from both the store and engine, with their provider, weight, context, pricing, and enabled state.
- **Edit Modal**: Click "Edit" on any model to change weight, max context tokens, pricing, or enabled state.
- **Weight Slider**: Quick inline weight adjustment (0-10).
- **Enable/Disable Toggle**: Click the status icon to toggle a model.
- **Delete**: Remove a model from the store and engine.

### Model Selection Graph

An interactive directed acyclic graph (DAG) showing the relationship between providers and models. Built with Cytoscape.js, it is populated on page load with all known providers and models and updates in real time as routing events arrive.

- Provider nodes (colored by health state)
- Model nodes (sized by weight)
- Edges colored by latency: green (<1s), yellow (1-3s), red (>3s)
- Edge thickness based on request volume
- Node size and border based on throughput and latency

### Cost and Latency Charts

Multi-series D3.js line charts showing cost and latency trends over time:

- Per-model breakdown
- Configurable time window
- Hover for exact values

### What-If Simulator

Test routing decisions without sending a live request:

- Select routing mode, token count, max budget, min weight, and model hint
- See the winning model, eligible candidates, and the routing reason
- Useful for understanding how parameter changes affect model selection

### SSE Decision Feed

Live event stream showing every routing decision in real time:

- Model, provider, latency, cost, and reason for each event
- Error events with error classification
- Auto-scrolling event list

### Routing Configuration

Set server-wide routing defaults:

- Default mode selector (cheap, normal, high_confidence, planning, adversarial)
- Budget input (USD)
- Latency input (milliseconds)
- Save button with validation

### Provider Health

Real-time provider health display:

- State badges: **Healthy** (green), **Degraded** (yellow), **Down** (red)
- Consecutive error count
- Last success timestamp
- Average latency

### API Keys

Key management interface:

- Create new keys (name, scopes, rotation, expiry)
- One-time key display modal with copy button
- Rotate keys (with one-time new key display)
- Enable/disable toggle
- Revoke (delete) keys
- Table showing: name, prefix, scopes, created, last used, expires, rotation days, status

### Request Log

Paginated request history:

- Model, provider, mode columns
- Latency, cost, status code
- Error class (for failed requests)
- Pagination controls

### Audit Log

Paginated audit trail viewer:

- Action type filter
- Timestamp, action, resource ID
- Request ID for correlation

### Model Leaderboard

A ranked table of models by performance:

- Success rate
- Average latency
- Total cost
- Request count

### Rewards

Contextual bandit reward data for Thompson Sampling analysis.

### Workflows (Temporal)

When Temporal is enabled, shows workflow execution history:

- Workflow ID, type, status
- Start time, duration
- Status badges: Running (blue), Completed (green), Failed (red)
- Click to expand activity history

## Static Assets

Static assets (Cytoscape.js, D3.js) are served from `/_assets/` to avoid conflicts with the `/admin/v1` API prefix. All assets are embedded in the binary via Go's `embed` package and served with `immutable` cache headers.

## Customization

The admin UI is a single `index.html` file located at `web/index.html` in the source tree. To customize:

1. Edit `web/index.html`
2. Rebuild the binary (`make build`) or Docker image (`make package`)
3. The updated UI is embedded automatically with fresh cache-busting hashes
