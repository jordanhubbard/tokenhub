# Prometheus Metrics

TokenHub exports Prometheus metrics at the `/metrics` endpoint.

## Available Metrics

### tokenhub_requests_total

**Type**: Counter

Total number of requests processed.

**Labels**:
| Label | Values | Description |
|-------|--------|-------------|
| `mode` | cheap, normal, high_confidence, planning, adversarial, thompson | Routing mode used |
| `model` | gpt-4, claude-opus, etc. | Model that handled the request |
| `provider` | openai, anthropic, vllm | Provider adapter |
| `status` | ok, error | Request outcome |

**Examples**:
```promql
# Total successful requests
tokenhub_requests_total{status="ok"}

# Request rate by provider
rate(tokenhub_requests_total[5m])

# Error rate
sum(rate(tokenhub_requests_total{status="error"}[5m]))
  /
sum(rate(tokenhub_requests_total[5m]))
```

---

### tokenhub_request_latency_ms

**Type**: Histogram

Request latency distribution in milliseconds.

**Labels**:
| Label | Values | Description |
|-------|--------|-------------|
| `mode` | cheap, normal, etc. | Routing mode |
| `model` | gpt-4, etc. | Model ID |
| `provider` | openai, etc. | Provider ID |

**Buckets**: 10, 20, 40, 80, 160, 320, 640, 1280, 2560, 5120 ms (exponential, base 2)

**Examples**:
```promql
# Median latency
histogram_quantile(0.5, rate(tokenhub_request_latency_ms_bucket[5m]))

# P95 latency
histogram_quantile(0.95, rate(tokenhub_request_latency_ms_bucket[5m]))

# P99 latency by model
histogram_quantile(0.99, sum(rate(tokenhub_request_latency_ms_bucket[5m])) by (model, le))

# Average latency
rate(tokenhub_request_latency_ms_sum[5m]) / rate(tokenhub_request_latency_ms_count[5m])
```

---

### tokenhub_cost_usd_total

**Type**: Counter

Cumulative estimated cost in USD.

**Labels**:
| Label | Values | Description |
|-------|--------|-------------|
| `model` | gpt-4, etc. | Model ID |
| `provider` | openai, etc. | Provider ID |

**Examples**:
```promql
# Total cost in the last hour
increase(tokenhub_cost_usd_total[1h])

# Cost rate (USD per second)
rate(tokenhub_cost_usd_total[5m])

# Cost per hour by model
rate(tokenhub_cost_usd_total[1h]) * 3600

# Most expensive model
topk(3, sum(rate(tokenhub_cost_usd_total[1h])) by (model))
```

## Grafana Dashboard

### Suggested Panels

| Panel | Query | Visualization |
|-------|-------|---------------|
| Request Rate | `sum(rate(tokenhub_requests_total[5m]))` | Time series |
| Error Rate | Error rate formula above | Gauge (0-100%) |
| P95 Latency | P95 formula above | Time series |
| Cost per Hour | Cost rate * 3600 | Stat |
| Requests by Model | `sum by (model) (rate(tokenhub_requests_total[5m]))` | Pie chart |
| Latency Heatmap | `tokenhub_request_latency_ms_bucket` | Heatmap |

## Scrape Configuration

```yaml
# prometheus.yml
scrape_configs:
  - job_name: tokenhub
    scrape_interval: 15s
    metrics_path: /metrics
    static_configs:
      - targets: ['tokenhub:8080']
```

For Docker Compose, use the service name as the target.
