# Tokenhub

Tokenhub is a containerized **LLM token interposer** that routes, arbitrates, and orchestrates requests across multiple providers (OpenAI, Anthropic, local vLLM, etc.) to optimize **cost / latency / reliability / context capacity / model “weight.”**

This repo contains:
- Go service with modular `internal/` package layout (chi/v5 router)
- Admin + consumer REST API (versioned `/v1`) with full CRUD
- Secure credential vault (AES-256-GCM, Argon2id, auto-lock timeout)
- Routing engine with weighted model selection, escalation, and failover
- Orchestration engine (adversarial, vote, refine modes) with directive parsing
- Provider adapters for OpenAI, Anthropic, and vLLM with retry/backoff
- SQLite persistence for models, providers, routing config, audit logs, and rewards
- Contextual bandit reward logging for RL-based routing data collection
- Embedded admin UI with provider/vault/routing/health/audit/log panels
- Prometheus metrics, health tracking, embedded TSDB
- Docker + docker-compose + Kubernetes manifests

> Status: **functional**. Core routing, orchestration, persistence, and admin UI are implemented and tested.

## Quick start (dev)

### 1) Run with docker compose
```bash
cp .env.example .env
docker compose up --build
```

- API: http://localhost:8080
- Admin UI: http://localhost:8080/admin
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

## Implementation status

- [x] Provider adapters (OpenAI, Anthropic, vLLM) with HTTP timeouts and retry/backoff
- [x] Vault unlock + AES-256-GCM encrypted key storage with auto-lock
- [x] Routing policies, weighted selection, escalation, and failover
- [x] Orchestration engine (adversarial, vote, refine) with DSL directive parsing
- [x] SQLite persistence (models, providers, routing config, audit, rewards)
- [x] Admin UI (provider/vault/routing/health/audit/log panels)
- [x] Rate limit header tracking and health monitoring
- [x] Contextual bandit reward logging for RL-based routing
- [ ] Load testing and benchmarks
- [ ] Production deployment guide

## License
MIT (see `LICENSE`)
