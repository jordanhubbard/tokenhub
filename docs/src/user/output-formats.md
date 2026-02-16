# Output Formats

TokenHub can shape provider responses into specific output formats. This is useful for applications that need structured data from LLM responses.

## Configuration

Set the `output_format` field in your chat request:

```json
{
  "output_format": {
    "type": "json",
    "schema": "{\"type\":\"object\",\"properties\":{\"answer\":{\"type\":\"string\"}}}",
    "max_tokens": 500,
    "strip_think": true
  }
}
```

## Format Types

### JSON

Validates the response against a JSON Schema. If the provider's output doesn't match the schema, TokenHub returns a validation error.

```json
{
  "output_format": {
    "type": "json",
    "schema": "{\"type\":\"array\",\"items\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"value\":{\"type\":\"number\"}}}}"
  }
}
```

The schema is passed as a string (not a nested object) to allow maximum flexibility.

### Markdown

Requests the provider to format its response as Markdown:

```json
{
  "output_format": {
    "type": "markdown"
  }
}
```

### Text

Plain text output with optional truncation:

```json
{
  "output_format": {
    "type": "text",
    "max_tokens": 200
  }
}
```

### XML

Requests XML-formatted output:

```json
{
  "output_format": {
    "type": "xml"
  }
}
```

## Output Format Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Output format: `json`, `markdown`, `text`, `xml` |
| `schema` | string | JSON Schema for validation (only with `type: "json"`) |
| `max_tokens` | int | Maximum output tokens to request from the provider |
| `strip_think` | bool | Remove `<think>...</think>` reasoning blocks from the response |

## Think Block Stripping

Some models (particularly those with chain-of-thought reasoning) wrap their internal reasoning in `<think>...</think>` tags. Setting `strip_think: true` removes these blocks from the final response:

**Before stripping**:
```
<think>
The user wants to know the capital of France. This is a straightforward factual question.
</think>
The capital of France is Paris.
```

**After stripping**:
```
The capital of France is Paris.
```

## JSON Schema Validation

When `type: "json"` is specified with a `schema`, TokenHub:

1. Sends the request to the provider (with a system message hint to produce JSON)
2. Parses the provider's response as JSON
3. Validates against the provided JSON Schema
4. Returns the validated JSON in the response

If validation fails, the error is returned in the response body with a 502 status.
