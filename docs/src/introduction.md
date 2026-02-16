# Introduction

TokenHub is an intelligent LLM routing proxy that sits between your applications and multiple AI providers. It provides a unified API for chat and planning requests while automatically selecting the best model based on cost, latency, capability, and provider health.

## What TokenHub Does

- **Unified API**: Single endpoint for OpenAI, Anthropic, and vLLM models
- **Intelligent Routing**: Multi-objective model selection considering cost, latency, capability weight, and provider health
- **Orchestration**: Multi-model reasoning with adversarial critique, voting, and iterative refinement modes
- **Credential Security**: AES-256-GCM encrypted vault for provider API keys with auto-lock and password rotation
- **Client Key Management**: Issue, rotate, and revoke API keys for your applications
- **Real-Time Monitoring**: Prometheus metrics, time-series database, audit logs, and a built-in admin UI
- **Streaming**: Server-Sent Events (SSE) streaming pass-through to all providers
- **Reinforcement Learning**: Thompson Sampling bandit policy for adaptive model routing

## Architecture at a Glance

```
┌─────────────┐     ┌──────────────────────────────────────────────┐
│  Client App  │────▶│                  TokenHub                    │
│              │◀────│                                              │
└─────────────┘     │  ┌─────────┐  ┌────────┐  ┌──────────────┐  │
                    │  │ Router  │──│ Health  │  │  Admin API   │  │
                    │  │ Engine  │  │ Tracker │  │  + UI (SPA)  │  │
                    │  └────┬────┘  └────────┘  └──────────────┘  │
                    │       │                                      │
                    │  ┌────┴──────────────────────────┐           │
                    │  │        Provider Adapters       │           │
                    │  │  ┌────────┐┌─────────┐┌────┐  │           │
                    │  │  │ OpenAI ││Anthropic││vLLM│  │           │
                    │  │  └────────┘└─────────┘└────┘  │           │
                    │  └───────────────────────────────┘           │
                    │                                              │
                    │  ┌─────────┐ ┌──────┐ ┌──────┐ ┌─────────┐  │
                    │  │ SQLite  │ │ TSDB │ │Vault │ │Temporal │  │
                    │  └─────────┘ └──────┘ └──────┘ └─────────┘  │
                    └──────────────────────────────────────────────┘
```

## Who This Documentation Is For

- **Users / Application Developers**: Learn how to send requests through TokenHub and use features like streaming, directives, and output formatting. Start with the [User Guide](user/overview.md).
- **Administrators**: Configure providers, manage credentials, set routing policies, and monitor the system. Start with the [Administrator Guide](admin/overview.md).
- **Developers / Contributors**: Understand the internals, extend provider support, or contribute to the project. Start with the [Developer Guide](developer/architecture.md).

## Quick Links

| Task | Where to Go |
|------|-------------|
| Send your first request | [Quick Start](quickstart.md) |
| Configure providers | [Provider Management](admin/providers.md) |
| Set up API keys | [API Key Management](admin/api-keys.md) |
| Deploy with Docker | [Docker & Compose](deployment/docker.md) |
| Full API reference | [API Reference](reference/api.md) |
| Monitor the system | [Monitoring](admin/monitoring.md) |
