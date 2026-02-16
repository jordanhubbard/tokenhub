# Chat API

The chat endpoint provides single-turn or multi-turn completions with automatic model routing.

**Endpoint**: `POST /v1/chat`

## Request Format

```json
{
  "request": {
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain quantum computing in simple terms."}
    ],
    "model_hint": "gpt-4",
    "estimated_input_tokens": 500,
    "parameters": {
      "temperature": 0.7,
      "max_tokens": 1024,
      "top_p": 0.9
    },
    "stream": false,
    "meta": {
      "user_id": "u123",
      "session": "abc"
    }
  },
  "capabilities": {
    "planning": true
  },
  "policy": {
    "mode": "normal",
    "max_budget_usd": 0.05,
    "max_latency_ms": 15000,
    "min_weight": 5
  },
  "output_format": {
    "type": "json",
    "schema": "{\"type\":\"object\",\"properties\":{\"answer\":{\"type\":\"string\"}}}",
    "max_tokens": 500,
    "strip_think": true
  }
}
```

## Request Fields

### `request` (required)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `messages` | array | Yes | Array of `{role, content}` message objects |
| `model_hint` | string | No | Preferred model ID; tried first before scoring |
| `estimated_input_tokens` | int | No | Token count hint for routing decisions |
| `parameters` | object | No | Provider parameters forwarded as-is (temperature, max_tokens, top_p, etc.) |
| `stream` | bool | No | Enable SSE streaming response |
| `meta` | object | No | Arbitrary metadata for logging and tracing |
| `output_schema` | JSON | No | JSON Schema for structured output validation |

### `policy` (optional)

Controls model selection behavior. All fields are optional and fall back to server defaults.

| Field | Type | Default | Range | Description |
|-------|------|---------|-------|-------------|
| `mode` | string | `normal` | See below | Routing mode |
| `max_budget_usd` | float | 0.05 | 0-100 | Maximum cost per request |
| `max_latency_ms` | int | 20000 | 0-300000 | Maximum acceptable latency |
| `min_weight` | int | 0 | 0-10 | Minimum model capability weight |

**Routing modes**:

| Mode | Cost Weight | Latency Weight | Failure Weight | Capability Weight |
|------|-------------|----------------|----------------|-------------------|
| `cheap` | 0.7 | 0.1 | 0.1 | 0.1 |
| `normal` | 0.25 | 0.25 | 0.25 | 0.25 |
| `high_confidence` | 0.05 | 0.1 | 0.15 | 0.7 |
| `planning` | 0.1 | 0.1 | 0.2 | 0.6 |
| `thompson` | N/A | N/A | N/A | N/A |

The `thompson` mode uses reinforcement learning (Thompson Sampling with Beta distributions) to adaptively select models based on historical reward data.

### `capabilities` (optional)

| Field | Type | Description |
|-------|------|-------------|
| `planning` | bool | Indicates request needs planning capability |

Capabilities influence which routing mode profile is used when no explicit mode is set.

### `output_format` (optional)

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Output format: `json`, `markdown`, `text`, `xml` |
| `schema` | string | JSON Schema string for validating structured output |
| `max_tokens` | int | Maximum output tokens to request from provider |
| `strip_think` | bool | Remove `<think>...</think>` blocks from response |

## Response Format

```json
{
  "negotiated_model": "gpt-4",
  "estimated_cost_usd": 0.0023,
  "routing_reason": "routed-weight-8",
  "response": {
    "id": "chatcmpl-...",
    "choices": [{
      "message": {
        "role": "assistant",
        "content": "Quantum computing uses..."
      }
    }],
    "usage": {
      "prompt_tokens": 45,
      "completion_tokens": 128,
      "total_tokens": 173
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `negotiated_model` | The model ID that was selected |
| `estimated_cost_usd` | Estimated cost based on model pricing and token counts |
| `routing_reason` | Why this model was chosen (see Routing Reasons) |
| `response` | Raw JSON response from the selected provider |

### Routing Reasons

| Reason | Description |
|--------|-------------|
| `routed-weight-N` | Selected by scoring; N is the model's weight |
| `model-hint` | Client's model hint was used |
| `escalated-context-overflow` | Escalated to a model with a larger context window |
| `retried-transient` | Retried after a transient provider error |

## Error Responses

| Status | Body | Cause |
|--------|------|-------|
| 400 | `"bad json"` | Malformed request body |
| 400 | `"messages required"` | Empty messages array |
| 400 | `"max_budget_usd must be between 0 and 100"` | Policy validation failure |
| 401 | `"missing or invalid api key"` | Missing or invalid Authorization header |
| 403 | `"scope not allowed"` | API key lacks `chat` scope |
| 502 | Error message | All models failed or no eligible models |

## Examples

### Minimal Request

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_..." \
  -d '{
    "request": {
      "messages": [{"role": "user", "content": "Hello!"}]
    }
  }'
```

### Cost-Optimized Request

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_..." \
  -d '{
    "request": {
      "messages": [{"role": "user", "content": "Summarize this text..."}]
    },
    "policy": {
      "mode": "cheap",
      "max_budget_usd": 0.001
    }
  }'
```

### Request with Model Hint

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_..." \
  -d '{
    "request": {
      "messages": [{"role": "user", "content": "Write a poem about the ocean."}],
      "model_hint": "claude-opus",
      "parameters": {
        "temperature": 0.9,
        "max_tokens": 2048
      }
    }
  }'
```

### Structured JSON Output

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer tokenhub_..." \
  -d '{
    "request": {
      "messages": [{"role": "user", "content": "List 3 programming languages with their year of creation"}]
    },
    "output_format": {
      "type": "json",
      "schema": "{\"type\":\"array\",\"items\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"year\":{\"type\":\"integer\"}}}}"
    }
  }'
```
