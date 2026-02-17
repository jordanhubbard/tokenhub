# Production Checklist

Use this checklist when deploying TokenHub to production.

## Pre-Deployment

- [ ] **Set a strong vault password** (16+ characters, mixed case, numbers, symbols)
- [ ] **Configure at least one provider** via environment variable
- [ ] **Set appropriate routing defaults** for your use case
- [ ] **Create API keys** for all client applications
- [ ] **Configure TSDB retention** appropriate for your storage budget

## Security Hardening

- [ ] **Set `TOKENHUB_ADMIN_TOKEN`**: Required to protect `/admin/v1/*` endpoints with Bearer token auth
- [ ] **Set `TOKENHUB_CORS_ORIGINS`**: Restrict CORS to your domain(s) (e.g., `https://app.example.com`)
- [ ] **Rate limiting**: Review `TOKENHUB_RATE_LIMIT_RPS` (default: 60/s) and `TOKENHUB_RATE_LIMIT_BURST` (default: 120) for your traffic patterns

## Network Security

- [ ] **TLS termination**: Place TokenHub behind a reverse proxy (nginx, Caddy, Traefik) with TLS
- [ ] **Firewall rules**: Only allow inbound traffic on the configured listen port

### Example nginx Configuration

```nginx
server {
    listen 443 ssl;
    server_name tokenhub.example.com;

    ssl_certificate     /etc/letsencrypt/live/tokenhub.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tokenhub.example.com/privkey.pem;

    # Consumer API - publicly accessible with API key auth
    location /v1/ {
        proxy_pass http://tokenhub:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Request-ID $request_id;

        # SSE streaming support
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 300s;
    }

    # Health check
    location /healthz {
        proxy_pass http://tokenhub:8080;
    }

    # Metrics (restrict to monitoring network)
    location /metrics {
        allow 10.0.0.0/8;
        deny all;
        proxy_pass http://tokenhub:8080;
    }

    # Admin endpoints (restrict to admin VPN)
    location /admin {
        allow 10.100.0.0/16;
        deny all;
        proxy_pass http://tokenhub:8080;
    }
}
```

## Database

- [ ] **Mount a persistent volume** for the SQLite database
- [ ] **Set WAL mode**: Include `_pragma=journal_mode(WAL)` in the DSN
- [ ] **Set busy timeout**: Include `_pragma=busy_timeout(5000)` in the DSN
- [ ] **Schedule backups**: Periodically copy the SQLite file (safe with WAL mode)

### Backup Script

```bash
#!/bin/bash
# Safe SQLite backup using the .backup command
sqlite3 /data/tokenhub.sqlite ".backup /backups/tokenhub-$(date +%Y%m%d-%H%M%S).sqlite"
```

## Monitoring

- [ ] **Prometheus scraping**: Configure Prometheus to scrape `/metrics`
- [ ] **Set up alerts** based on the [recommended alerting rules](../admin/monitoring.md#recommended-alerting-rules)
- [ ] **Log aggregation**: Forward structured JSON logs to your log management system
- [ ] **Monitor TSDB size**: Set appropriate retention to prevent unbounded growth

### Key Metrics to Watch

| Metric | Alert Threshold | Severity |
|--------|----------------|----------|
| Error rate | > 5% over 5 min | Warning |
| P95 latency | > 10s | Warning |
| Provider down | > 2 min | Critical |
| Cost spike | > 2x weekly average | Warning |
| Vault locked | During business hours | Critical |
| Disk usage | > 80% | Warning |

## Graceful Shutdown

TokenHub handles `SIGINT` and `SIGTERM` for graceful shutdown:

1. Stop accepting new connections
2. Drain in-flight requests (30-second timeout)
3. Stop background goroutines (prober, Thompson Sampling refresh, TSDB prune)
4. Stop Temporal worker (if enabled)
5. Close database connection

In Kubernetes, set `terminationGracePeriodSeconds: 35` to allow the full drain.

## Scaling Considerations

TokenHub is a single-process application with SQLite. For higher throughput:

- **Horizontal**: Run multiple instances with separate SQLite databases (no shared state; each instance routes independently)
- **Temporal**: Enable Temporal for durable workflow execution across restarts
- **Read replicas**: Not applicable (SQLite is embedded)
- **Connection pooling**: SQLite WAL mode supports concurrent reads natively

For very high throughput (>1000 req/s), consider migrating the store to PostgreSQL (implement the `Store` interface for a new backend).

## Environment Variables Summary

See [Configuration Reference](configuration.md) for the complete list of all environment variables and their defaults.
