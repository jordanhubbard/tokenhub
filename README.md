# Tokenhub

Tokenhub is a containerized **LLM token interposer** that routes, arbitrates, and orchestrates requests across multiple providers (OpenAI, Anthropic, local vLLM, etc.) to optimize **cost / latency / reliability / context capacity / model “weight.”**

This repo contains:
- Go service skeleton (modular packages)
- Admin + consumer REST API design (versioned `/v1`)
- Secure credential vault (escrow) + environment-based credentials
- Routing engine + orchestration engine scaffolding
- Metrics scaffolding (Prometheus-compatible)
- Docker + docker-compose + basic Kubernetes manifests
- PRD + architecture + scheduling/RL design + orchestration DSL design

> Status: **scaffold**. The bones are here; now you can unleash a coding model to implement the missing pieces.

## Quick start (dev)

### 1) Run with docker compose
```bash
cp .env.example .env
docker compose up --build
```

- API: http://localhost:8080
- Admin UI placeholder: http://localhost:8080/admin (returns stub JSON for now)
- Metrics: http://localhost:8080/metrics
- Health: http://localhost:8080/healthz

### 2) Run locally
```bash
go mod download
go run ./cmd/tokenhub
```

## Configuration

Tokenhub reads config from (in precedence order):
1. Environment variables (`TOKENHUB_*`)
2. Config file (`config/config.yaml` by default)

See: `config/config.example.yaml` and `.env.example`.

## Docs
- PRD: `docs/PRD.md`
- Architecture: `docs/ARCHITECTURE.md`
- Routing & policy: `docs/ROUTING.md`
- Adversarial scheduling + orchestration DSL: `docs/ORCHESTRATION_DSL.md`
- RL / bandit router design: `docs/RL_ROUTER.md`
- API: `docs/API.md`
- Threat model & vault: `docs/SECURITY.md`
- Provider adapters: `docs/PROVIDERS.md`

## “Release the hounds” checklist

- [ ] Implement provider adapters (OpenAI/Anthropic/vLLM)
- [ ] Implement vault unlock + encrypted key storage
- [ ] Implement routing policies + fallbacks
- [ ] Implement orchestration engine using DSL directives
- [ ] Add persistence (SQLite default; Postgres optional)
- [ ] Add UI (or wire to your preferred admin UI stack)
- [ ] Load test + rate limit + timeouts
- [ ] Deploy (k8s or Railway)

## License
MIT (see `LICENSE`)
