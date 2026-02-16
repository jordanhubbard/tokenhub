# User Guide Overview

This section is for application developers integrating with TokenHub. TokenHub exposes two main endpoints:

| Endpoint | Purpose |
|----------|---------|
| `POST /v1/chat` | Single-turn or multi-turn chat completion |
| `POST /v1/plan` | Multi-model orchestrated reasoning |

Both endpoints accept a unified request format and return the provider's response along with routing metadata (which model was chosen, estimated cost, and routing reason).

## Key Concepts

### Routing Policies

Every request can include a **policy** that guides model selection:

- **`cheap`** — Minimize cost (prefer smaller, cheaper models)
- **`normal`** — Balance cost, latency, capability, and reliability
- **`high_confidence`** — Prefer the most capable models regardless of cost
- **`planning`** — Optimized for planning and reasoning tasks
- **`thompson`** — Adaptive selection using reinforcement learning

If no policy is specified, the server's default routing mode applies.

### Model Selection

TokenHub maintains a registry of models from all configured providers. Each model has:

- **Weight** (0-10): Higher weight = more capable
- **Context window**: Maximum tokens the model can process
- **Pricing**: Cost per 1,000 input and output tokens
- **Health status**: Based on recent success/failure rates

The routing engine scores all eligible models and selects the best match for your request.

### Authentication

All `/v1` requests require an API key in the `Authorization` header:

```
Authorization: Bearer tokenhub_<key>
```

API keys are created and managed by administrators. Each key has scopes controlling which endpoints it can access (`chat`, `plan`, or both).

### Provider Transparency

You interact only with TokenHub. The underlying provider (OpenAI, Anthropic, vLLM) is selected automatically and its API key is never exposed. The response includes which model and provider were used in the `negotiated_model` field.

## Sections

- [Chat API](chat-api.md) — Detailed guide to `/v1/chat`
- [Plan API](plan-api.md) — Multi-model orchestration via `/v1/plan`
- [Streaming](streaming.md) — Server-Sent Events streaming
- [Directives](directives.md) — In-band routing overrides embedded in messages
- [Output Formats](output-formats.md) — JSON Schema validation, Markdown, XML output shaping
- [Authentication](authentication.md) — API key usage and scopes
