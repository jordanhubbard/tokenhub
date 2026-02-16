# In-Band Directives

TokenHub supports embedding routing directives directly in message content. This allows clients to override routing policy without changing the request structure, which is useful when working through intermediary systems that pass messages through unchanged.

## Single-Line Directive

Embed a directive anywhere in a message's content using the `@@tokenhub` prefix:

```
@@tokenhub mode=cheap budget=0.01 latency=5000 min_weight=5
```

Example in a full request:

```json
{
  "request": {
    "messages": [
      {
        "role": "user",
        "content": "@@tokenhub mode=cheap budget=0.005\nSummarize this document..."
      }
    ]
  }
}
```

## Block Directive

For complex directives (especially those containing JSON schemas), use the block format:

```
@@tokenhub
mode=high_confidence
budget=0.10
latency=30000
min_weight=8
output_schema={"type":"object","properties":{"answer":{"type":"string"},"confidence":{"type":"number"}}}
@@end
```

The block starts with `@@tokenhub` on its own line and ends with `@@end`.

## Supported Keys

| Key | Type | Maps To | Description |
|-----|------|---------|-------------|
| `mode` | string | `policy.mode` | Routing mode (cheap, normal, high_confidence, planning, adversarial) |
| `budget` | float | `policy.max_budget_usd` | Maximum cost in USD |
| `latency` | int | `policy.max_latency_ms` | Maximum latency in milliseconds |
| `min_weight` | int | `policy.min_weight` | Minimum model capability weight |
| `output_schema` | JSON | `request.output_schema` | JSON Schema for structured output |

## Processing Rules

1. **Scanning**: TokenHub scans all messages for directives. The **last** directive found takes precedence.
2. **Stripping**: Directives are removed from message content before forwarding to the provider. The LLM never sees `@@tokenhub` text.
3. **Override**: Directive values override both server defaults and request-level policy fields.
4. **Partial override**: You can set only the fields you want to override. Unspecified fields retain their values from the request policy or server defaults.

## Examples

### Cost-optimize a specific request

```
@@tokenhub mode=cheap budget=0.001
What is 2 + 2?
```

### Force high-quality response

```
@@tokenhub mode=high_confidence min_weight=9
Write a detailed analysis of the economic implications of quantum computing.
```

### Structured output via directive

```
@@tokenhub
output_schema={"type":"object","properties":{"name":{"type":"string"},"population":{"type":"integer"}}}
@@end
What is the most populous city in Japan?
```
