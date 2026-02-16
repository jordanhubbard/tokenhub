# Tokenhub PRD

This is the full PRD captured from the conversation, expanded into implementation-oriented requirements.

## 1) Problem

You have multiple LLM “token providers”:
- Hosted APIs (OpenAI, Anthropic)
- Self-hosted vLLM endpoints (commodity GPUs; slower but always on)

Each provider/model differs in:
- Context window
- Cognitive capability (“weight”)
- Reliability
- Rate limits/throttling
- Cost (USD)

You want a service called **Tokenhub** that sits between **consumers** and **providers** and makes routing decisions to optimize cost/latency/reliability/weight/context.

Additionally you want “adversarial scheduling” modes where models cooperate/compete (plan + critique + refine).

## 2) Goals

- Unified interposer API (clients do not talk to providers directly)
- Dynamic routing based on:
  - Model weight threshold
  - Context window constraints
  - Estimated USD budget
  - Rate limit state
  - Provider health
  - Latency constraints
- Adversarial orchestration modes:
  - planner + critic
  - multi-round refinement
  - voting
- Secure credential management:
  - Escrow keys stored encrypted at rest; vault unlock requires admin password entered in UI (never persisted)
  - Environment-based keys for external secret managers and PaaS deployment

## 3) Non-goals (initial)

- Hosting/training models
- Unified billing across providers (beyond reporting)
- Full UI suite (a minimal admin UI is fine initially)

## 4) Primary user flows

### 4.1 Admin: register providers
- Add provider (openai/anthropic/vllm)
- Choose credential source:
  - escrow (store encrypted)
  - env (reference env var name)
  - none (local unauthenticated)
- Enable/disable provider
- Configure base URL / endpoints

### 4.2 Admin: register models
- Link model to provider
- Set:
  - weight score
  - max context tokens
  - pricing input/output per 1k
  - expected latency (optional)
  - enable/disable

### 4.3 Consumer: send chat request
- Submit standard request envelope
- Optional policy hints (mode/budget/latency/min_weight)
- Optional orchestration directives

Tokenhub:
- estimates tokens
- chooses provider/model
- performs request
- handles failures/fallback
- returns response + decision metadata

## 5) Key requirements

- MUST run as docker/k8s service
- MUST never log plaintext credentials
- MUST support rate limit handling + routing away from throttled providers
- MUST fail over on provider errors
- MUST strip orchestration directives from forwarded content (no leaking control-plane data)

## 6) Deliverables
- API service (Go recommended)
- Admin UI (minimal)
- Config file + env support
- Provider adapters and routing
- Orchestration engine

See additional docs for implementation details.
