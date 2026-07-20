# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The canonical release version lives in [`VERSION`](VERSION). Build mode in
[`scripts/release.sh`](scripts/release.sh) validates a matching changelog
entry for the current version, and release mode updates `VERSION` and this
changelog together.

## [Unreleased]

## [0.3.0] - 2026-07-20

### Added

- Reference-style release automation in `scripts/release.sh`, including version bumps, changelog refresh, quality gates, release commits, and git tags while preserving the existing binary build mode used by CI.
- Governance and contribution metadata for external contributors.
- GitHub Actions workflows for CI, linting, CodeQL, release automation, and scheduled vulnerability scanning.

## [0.1.0] - 2026-04-25

### Added

- Initial koios daemon release with a single WebSocket JSON-RPC control plane for peer-scoped sessions.
- Cobra-based operator CLI for health checks, agent execution, state inspection, and configuration bootstrap.
- Release and versioning scripts built around the repository `VERSION` file.

[Unreleased]: https://github.com/ffimnsr/koios/releases/tag/v0.3.0
[0.3.0]: https://github.com/ffimnsr/koios/releases/tag/v0.3.0
[0.1.0]: https://github.com/ffimnsr/koios/releases/tag/v0.1.0
