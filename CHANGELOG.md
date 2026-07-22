# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The canonical release version lives in [`VERSION`](VERSION). Build mode in
[`scripts/release.sh`](scripts/release.sh) validates a matching changelog
entry for the current version, and release mode updates `VERSION` and this
changelog together.

## [Unreleased]

## [0.7.13] - 2026-07-23

### Added

- update koios fingerprint secret to bind to blob (`164a343`)

## [0.7.12] - 2026-07-23

### Fixed

- update and fix type for gemini (`81663cc`)


### Tests

- update test on runtime for existing pruning (`933a4b3`)
- update failing test (`94ba722`)

## [0.7.11] - 2026-07-22

### Added

- update tool registry setup (`c65f6cd`)
- fix all web fetch and web search tools (`fa6197a`)

## [0.7.10] - 2026-07-21

### Added

- update max steps so calls won't stop (`a2c204d`)

## [0.7.9] - 2026-07-21

### Added

- add reasoning (`a610d57`)


### Maintenance

- update task images (`7544e06`)

## [0.7.8] - 2026-07-21

### Added

- update the koios to support context compaction (`5410fbe`)
- update the tasks and fix the test on race (`46f82de`)

## [0.7.7] - 2026-07-21

### Added

- update the koios to support context compaction (`5410fbe`)

## [0.7.6] - 2026-07-21

### Added

- add skill based system (`1136e46`)
- update chat tools to have search and list (`7db040e`)
- update tasks for development tools (`c28df00`)


### Fixed

- update all lint errors (`a66bdff`)
- update the static check call warning (`84f5a72`)

## [0.7.5] - 2026-07-20

### Added

- add mcp stderr output (`19d4122`)

## [0.7.4] - 2026-07-20

### Added

- update the koios to inject peer id on mcp tool calls (`35408aa`)
- update the koios default image so it can be extended (`d2c6589`)

## [0.7.3] - 2026-07-20

### Added

- update containerfile and readme (`779786f`)

## [0.7.2] - 2026-07-20

### Maintenance

- update release workflow for image builder (`d1cf6ef`)

## [0.7.1] - 2026-07-20

### Maintenance

- update release workflow (`71e829a`)

## [0.7.0] - 2026-07-20

### Added

- initial commit engine koios (`937c477`)
- update the handler and tools capability (`a63e2a0`)
- add the vault for mk env (`55bf794`)
- update heartbeat config (`ae1650b`)
- update the parts of compactor and subagent (`049f452`)
- add mcp and privacy redaction functions (`bed180c`)
- move multiline strings to templates (`3cee1a9`)
- add bootstrap templates (`2903f40`)
- update the agent to use irc like tui (`00de2d0`)
- add workflow engine (`b684a32`)
- add new file tools for llm to easily work (`637dcc6`)
- add a proper orchestrator (`3cc3222`)
- add slash commands and activate run ledger (`221208c`)
- add sandboxed code execution and streaming controls (`18dd844`)
- expand doctor diagnostics and repairs (`f1a6afe`)
- update mk vault (`5aad2b0`)
- add personal planning and memory workflows (`7c21cbb`)
- add bookmarks, memory provenance, and profiles (`a1c5057`)
- add repository governance docs, release notes, and GitHub Actions baseline (`6f749f3`)
- add comprehensive data persistence stores and tool implementations (`b4894c2`)
- reorganize workspace structure with peer-based documents and databases (`aec2d4b`)
- add tool result provenance store and improve tool prompt clarity (`982bc13`)
- suggest similar tool names on unknown tool errors (`204af49`)
- add extension manifest system with plugin hooks and native tools (`cb872fd`)
- add telegram channel and extension framework support (`0d6daa0`)
- add browser automation and secured tool controls (`797c6de`)
- add peer llm (`df620bc`)
- allow peers to add mcp for their own use (`fe39a64`)
- allow mcp for owners only (`4d88909`)
- update release scripts (`927904d`)
- clean source (`300b598`)
- update and fix the versions and errors (`054ecb5`)


### Fixed

- update ledger for orchestrator updates (`71a015e`)
- restore session maintenance defaults (`6244774`)
- replace all diagnostics error with correct code (`bcc2359`)
- update the race condition change (`65eff94`)
- update the channels to prevent race (`4b4da3b`)


### Documentation

- clarify usage tracking issue wording (`612037a`)


### Maintenance

- update koios codebase with recent changes (`6349492`)
- split handler, memory, and orchestrator into focused modules (`bce76f6`)
- reorganize cli and config packages (`9194803`)

## [0.6.0] - 2026-07-20

### Added

- initial commit engine koios (`937c477`)
- update the handler and tools capability (`a63e2a0`)
- add the vault for mk env (`55bf794`)
- update heartbeat config (`ae1650b`)
- update the parts of compactor and subagent (`049f452`)
- add mcp and privacy redaction functions (`bed180c`)
- move multiline strings to templates (`3cee1a9`)
- add bootstrap templates (`2903f40`)
- update the agent to use irc like tui (`00de2d0`)
- add workflow engine (`b684a32`)
- add new file tools for llm to easily work (`637dcc6`)
- add a proper orchestrator (`3cc3222`)
- add slash commands and activate run ledger (`221208c`)
- add sandboxed code execution and streaming controls (`18dd844`)
- expand doctor diagnostics and repairs (`f1a6afe`)
- update mk vault (`5aad2b0`)
- add personal planning and memory workflows (`7c21cbb`)
- add bookmarks, memory provenance, and profiles (`a1c5057`)
- add repository governance docs, release notes, and GitHub Actions baseline (`6f749f3`)
- add comprehensive data persistence stores and tool implementations (`b4894c2`)
- reorganize workspace structure with peer-based documents and databases (`aec2d4b`)
- add tool result provenance store and improve tool prompt clarity (`982bc13`)
- suggest similar tool names on unknown tool errors (`204af49`)
- add extension manifest system with plugin hooks and native tools (`cb872fd`)
- add telegram channel and extension framework support (`0d6daa0`)
- add browser automation and secured tool controls (`797c6de`)
- add peer llm (`df620bc`)
- allow peers to add mcp for their own use (`fe39a64`)
- allow mcp for owners only (`4d88909`)
- update release scripts (`927904d`)
- clean source (`300b598`)
- update and fix the versions and errors (`054ecb5`)


### Fixed

- update ledger for orchestrator updates (`71a015e`)
- restore session maintenance defaults (`6244774`)
- replace all diagnostics error with correct code (`bcc2359`)
- update the race condition change (`65eff94`)


### Documentation

- clarify usage tracking issue wording (`612037a`)


### Maintenance

- update koios codebase with recent changes (`6349492`)
- split handler, memory, and orchestrator into focused modules (`bce76f6`)
- reorganize cli and config packages (`9194803`)

## [0.5.0] - 2026-07-20

### Added

- initial commit engine koios (`937c477`)
- update the handler and tools capability (`a63e2a0`)
- add the vault for mk env (`55bf794`)
- update heartbeat config (`ae1650b`)
- update the parts of compactor and subagent (`049f452`)
- add mcp and privacy redaction functions (`bed180c`)
- move multiline strings to templates (`3cee1a9`)
- add bootstrap templates (`2903f40`)
- update the agent to use irc like tui (`00de2d0`)
- add workflow engine (`b684a32`)
- add new file tools for llm to easily work (`637dcc6`)
- add a proper orchestrator (`3cc3222`)
- add slash commands and activate run ledger (`221208c`)
- add sandboxed code execution and streaming controls (`18dd844`)
- expand doctor diagnostics and repairs (`f1a6afe`)
- update mk vault (`5aad2b0`)
- add personal planning and memory workflows (`7c21cbb`)
- add bookmarks, memory provenance, and profiles (`a1c5057`)
- add repository governance docs, release notes, and GitHub Actions baseline (`6f749f3`)
- add comprehensive data persistence stores and tool implementations (`b4894c2`)
- reorganize workspace structure with peer-based documents and databases (`aec2d4b`)
- add tool result provenance store and improve tool prompt clarity (`982bc13`)
- suggest similar tool names on unknown tool errors (`204af49`)
- add extension manifest system with plugin hooks and native tools (`cb872fd`)
- add telegram channel and extension framework support (`0d6daa0`)
- add browser automation and secured tool controls (`797c6de`)
- add peer llm (`df620bc`)
- allow peers to add mcp for their own use (`fe39a64`)
- allow mcp for owners only (`4d88909`)
- update release scripts (`927904d`)
- clean source (`300b598`)
- update and fix the versions and errors (`054ecb5`)


### Fixed

- update ledger for orchestrator updates (`71a015e`)
- restore session maintenance defaults (`6244774`)
- replace all diagnostics error with correct code (`bcc2359`)


### Documentation

- clarify usage tracking issue wording (`612037a`)


### Maintenance

- update koios codebase with recent changes (`6349492`)
- split handler, memory, and orchestrator into focused modules (`bce76f6`)
- reorganize cli and config packages (`9194803`)

## [0.4.0] - 2026-07-20

### Added

- initial commit engine koios (`937c477`)
- update the handler and tools capability (`a63e2a0`)
- add the vault for mk env (`55bf794`)
- update heartbeat config (`ae1650b`)
- update the parts of compactor and subagent (`049f452`)
- add mcp and privacy redaction functions (`bed180c`)
- move multiline strings to templates (`3cee1a9`)
- add bootstrap templates (`2903f40`)
- update the agent to use irc like tui (`00de2d0`)
- add workflow engine (`b684a32`)
- add new file tools for llm to easily work (`637dcc6`)
- add a proper orchestrator (`3cc3222`)
- add slash commands and activate run ledger (`221208c`)
- add sandboxed code execution and streaming controls (`18dd844`)
- expand doctor diagnostics and repairs (`f1a6afe`)
- update mk vault (`5aad2b0`)
- add personal planning and memory workflows (`7c21cbb`)
- add bookmarks, memory provenance, and profiles (`a1c5057`)
- add repository governance docs, release notes, and GitHub Actions baseline (`6f749f3`)
- add comprehensive data persistence stores and tool implementations (`b4894c2`)
- reorganize workspace structure with peer-based documents and databases (`aec2d4b`)
- add tool result provenance store and improve tool prompt clarity (`982bc13`)
- suggest similar tool names on unknown tool errors (`204af49`)
- add extension manifest system with plugin hooks and native tools (`cb872fd`)
- add telegram channel and extension framework support (`0d6daa0`)
- add browser automation and secured tool controls (`797c6de`)
- add peer llm (`df620bc`)
- allow peers to add mcp for their own use (`fe39a64`)
- allow mcp for owners only (`4d88909`)
- update release scripts (`927904d`)
- clean source (`300b598`)


### Fixed

- update ledger for orchestrator updates (`71a015e`)
- restore session maintenance defaults (`6244774`)
- replace all diagnostics error with correct code (`bcc2359`)


### Documentation

- clarify usage tracking issue wording (`612037a`)


### Maintenance

- update koios codebase with recent changes (`6349492`)
- split handler, memory, and orchestrator into focused modules (`bce76f6`)
- reorganize cli and config packages (`9194803`)

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

[Unreleased]: https://github.com/ffimnsr/koios/compare/v0.7.13...HEAD
[0.7.13]: https://github.com/ffimnsr/koios/releases/tag/v0.7.13
[0.7.12]: https://github.com/ffimnsr/koios/releases/tag/v0.7.12
[0.7.11]: https://github.com/ffimnsr/koios/releases/tag/v0.7.11
[0.7.10]: https://github.com/ffimnsr/koios/releases/tag/v0.7.10
[0.7.9]: https://github.com/ffimnsr/koios/releases/tag/v0.7.9
[0.7.8]: https://github.com/ffimnsr/koios/releases/tag/v0.7.8
[0.7.7]: https://github.com/ffimnsr/koios/releases/tag/v0.7.7
[0.7.6]: https://github.com/ffimnsr/koios/releases/tag/v0.7.6
[0.7.5]: https://github.com/ffimnsr/koios/releases/tag/v0.7.5
[0.7.4]: https://github.com/ffimnsr/koios/releases/tag/v0.7.4
[0.7.3]: https://github.com/ffimnsr/koios/releases/tag/v0.7.3
[0.7.2]: https://github.com/ffimnsr/koios/releases/tag/v0.7.2
[0.7.1]: https://github.com/ffimnsr/koios/releases/tag/v0.7.1
[0.7.0]: https://github.com/ffimnsr/koios/releases/tag/v0.7.0
[0.6.0]: https://github.com/ffimnsr/koios/releases/tag/v0.6.0
[0.5.0]: https://github.com/ffimnsr/koios/releases/tag/v0.5.0
[0.4.0]: https://github.com/ffimnsr/koios/releases/tag/v0.4.0
[0.3.0]: https://github.com/ffimnsr/koios/releases/tag/v0.3.0
[0.1.0]: https://github.com/ffimnsr/koios/releases/tag/v0.1.0
