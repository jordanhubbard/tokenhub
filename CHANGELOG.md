# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.33] - 2026-05-27

### Added
- add automated release workflow
- observability dimensions, bar charts, router refactor, alias routing
- add SSE streaming to /v1/messages passthrough
- resolve upstream model ID via suffix match; rewrite body before forwarding
- implement AnthropicRawSender for backends supporting both formats
- add Anthropic /v1/messages passthrough endpoint
- add Gemma 4 output token rewriting (thinking blocks + tool calls)
- add tool call pass-through and per-model tool name rewriting
- add generic vault secrets CRUD endpoints + tokenhubctl secret subcommand
- add embeddings scope to routeToScope + expose context_length in /v1/models
- parallel hedging, max_tokens shrink on context overflow, TCP safety

### Fixed
- fail down wildcard models on budget exhaustion
- preserve OpenAI tool call metadata
- align /v1 chat route and output schema enforcement
- translate Anthropic tool_use blocks to OpenAI format and sanitize tooluse_ IDs
- accept x-api-key header alongside Authorization: Bearer
- add /v1/messages to scope check (maps to chat scope)
- unregister engine models when provider is deleted
- resolve golangci-lint errcheck, gosec, staticcheck issues
- add scope + strip double /v1 from proxy URL
- deterministic hint routing + provider prefix stripping
- make /v1/models public for OpenAI-compatible client discovery
- make start self-bootstraps image; restore DNS; trim Makefile
- disabling a provider now actually removes it from the router
- per-provider timeout + context overflow classification + larger latency budget
- shield clients from transient provider failures with last-resort fallback

### Added
- Multi-platform release process with automated binary builds for macOS (Intel/ARM64) and Linux (x86-64/ARM64)
- GitHub Actions workflow for building and uploading release artifacts
- Automated changelog generation from conventional commits

### Changed
- Enhanced release.sh script with CHANGELOG.md integration

---

## [0.1.0] - 2026-05-26

### Added
- Initial TokenHub release
- LLM routing proxy with multi-provider support
- Provider credential management
- Admin UI for configuration
- Docker container support

[Unreleased]: https://github.com/jordanhubbard/tokenhub/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jordanhubbard/tokenhub/releases/tag/v0.1.0
