# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
