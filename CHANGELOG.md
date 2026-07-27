# Changelog

All notable changes are recorded here. This project uses semantic versioning.

## Unreleased

- Complete external scanner review, clean-machine qualification, and the final
  release approval for v1.0.0-rc.1.

## v1.0.0-rc.1

Not released.

- Added read-only `doctor` readiness checks and an explicit, previewable
  `setup` flow.
- Hardened Codex discovery, configuration precedence, generated worker
  environments, and preflight non-mutation behavior.
- Added Terminal.app, iTerm2, and headless adapters with observable launch and
  failure diagnostics.
- Added durable worker status, logs, events, reconciliation, cancellation,
  timeout, and preservation-focused recovery behavior.
- Documented the v1 command, JSON, configuration, task, state, and exit-code
  compatibility baseline.
- Added reproducible `darwin/arm64` and `darwin/amd64` release archives,
  checksums, build metadata, native smoke jobs, and draft-only GitHub release
  automation.
- Added public installation, onboarding, privacy, security, support, upgrade,
  rollback, and clean-machine qualification documentation.

The exact supported macOS, Codex CLI, Terminal.app, and iTerm2 versions remain
pending clean-machine qualification. This section is a draft and does not
announce availability.
