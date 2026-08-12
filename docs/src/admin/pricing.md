# NeMo Relay Cost Accounting

TokenHub can load [NVIDIA NeMo Relay model-pricing catalog v1](https://docs.nvidia.com/nemo/relay/latest/configure-plugins/model-pricing)
JSON files as a
first-class cost source. Catalog rates participate in preflight budget checks
and routing scores, then actual provider token usage is repriced after each
response. No NeMo Relay process or experimental Go/FFI binding is required.

## Enable Catalog Pricing

Set `TOKENHUB_NEMO_RELAY_PRICING_FILES` to one or more comma-separated catalog
paths. Files are searched in order and the first matching entry wins.

```bash
export TOKENHUB_NEMO_RELAY_PRICING_FILES="/etc/tokenhub/pricing-overrides.json,/etc/tokenhub/pricing.json"
```

An explicitly configured file is required to exist and validate at startup.
TokenHub refuses to start with a malformed catalog so a pricing typo cannot
silently disable budget enforcement.

SIGHUP reloads the configured files. A failed runtime reload keeps the last
known-good catalog snapshot active and records a warning.

For Docker, mount catalogs read-only and use container paths:

```yaml
services:
  tokenhub:
    environment:
      TOKENHUB_NEMO_RELAY_PRICING_FILES: /pricing/catalog.json
    volumes:
      - ./pricing.json:/pricing/catalog.json:ro
```

## Catalog Format

TokenHub accepts the NeMo Relay pricing catalog v1 contract, including model
aliases, per-million token rates, cache rates, and prompt-token threshold
tiers.

```json
{
  "version": 1,
  "entries": [
    {
      "provider": "openai",
      "model_id": "gpt-4o-mini",
      "aliases": ["openai/gpt-4o-mini"],
      "currency": "USD",
      "unit": "per_token",
      "rates": {
        "input_per_million": 0.15,
        "output_per_million": 0.60,
        "cache_read_per_million": 0.075
      },
      "prompt_cache": {
        "read_accounting": "included_in_prompt_tokens"
      },
      "pricing_as_of": "2026-08-01",
      "pricing_source": "internal-pricing-snapshot"
    }
  ]
}
```

`prompt_cache.read_accounting` must be one of:

- `included_in_prompt_tokens`: cached input is already part of the reported
  prompt count. TokenHub subtracts it before applying the uncached input rate.
- `separate`: cached input is reported separately, so the full prompt count
  remains billable at the input rate.

For tiered pricing, use a `rate_schedule` with type
`prompt_token_threshold`. Bounds are inclusive, and the first matching tier's
rates apply to the full request.

## Accounting Precedence

TokenHub resolves USD cost in this order:

1. Provider-reported `usage.cost_usd` or NeMo Relay-normalized `usage.cost`.
2. The first matching NeMo Relay catalog entry.
3. The model's legacy `input_per_1k` and `output_per_1k` rates.
4. The routing preflight estimate when the response contains no usage.

TokenHub's budgets and cost metrics are USD-denominated. It does not convert
currencies, so a non-USD catalog entry or reported cost is not treated as USD.

OpenAI, OpenAI Responses, Anthropic, and NeMo Relay-normalized usage fields are
recognized. Cache reads and writes are recorded separately while
`total_tokens` remains prompt plus completion tokens to avoid double-counting
cache reads that are included in prompt usage.

## Inspect Active Pricing

```bash
curl -H "Authorization: Bearer $TOKENHUB_ADMIN_TOKEN" \
  http://localhost:8080/admin/v1/pricing
```

Or use `tokenhubctl pricing`.

Example:

```json
{
  "enabled": true,
  "type": "nemo_relay",
  "sources": ["/etc/tokenhub/pricing.json"],
  "entries": 42
}
```

Request logs include `cache_read_tokens`, `cache_write_tokens`, `cost_source`,
`pricing_source`, and `pricing_as_of`. Native `/v1/chat` and `/v1/plan`
responses include the normalized `cost` object. OpenAI-compatible and
non-streaming Anthropic-compatible responses expose `X-TokenHub-Cost-USD` and
`X-TokenHub-Cost-Source` headers. Anthropic SSE responses expose the same
values as HTTP trailers after final usage is received.
