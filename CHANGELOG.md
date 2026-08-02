# Changelog

All notable changes are recorded here. This project uses semantic versioning.

## Unreleased

### v1.0.0 release candidate

- Promoted the embedded application version from the release-candidate line to
  `v1.0.0` while preserving the documented v1 CLI, JSON, configuration, task,
  and persisted-state compatibility contracts.
- Completed the local-first worker runtime, deterministic role discovery,
  reviewable role enhancement, ordered folder execution, semantic completion
  enforcement, safe commit boundaries, sequence lifecycle controls, and
  replaceable Codex and Antigravity runtime adapters developed through the RC
  series.
- Kept GitHub releases source-only and added review-first Homebrew distribution:
  publishing a release proposes an exact-version formula update, but never
  auto-merges it.
- Improved repeated `discover` failures so conflicting generated configuration
  names the file, confirms that no files changed, and gives preservation-first
  recovery guidance.
- Added evidence-backed commit-ownership diagnostics when a worker commits the
  exact approved change set before `--commit-each`, while retaining generic
  fail-closed errors for ambiguous Git states.

This entry prepares the final version-bump pull request. `v1.0.0` is not tagged
or published until the reviewed commit is merged and the separate release
procedure is explicitly authorized.

### v1.0.0-rc.2.1 candidate

- Fixed ordered-folder completion parsing when a Codex JSONL command event is
  larger than the scanner's default token limit. Final `STATUS` and
  `NEXT_PROMPT_SAFE` fields are now found after large tool output.
- Persisted parsed completion evidence before publishing a terminal worker
  status, preventing an ordered runner from observing stale completion data.

RC.2.1 is a compatible runtime bug-fix candidate. It is not tagged, qualified,
or published yet.

### v1.0.0-rc.2 candidate

- Added persistent project-owned worker definitions, task assignment and FIFO
  queues, scheduling, lifecycle controls, path policies, worktree/branch
  selection, and local review handoffs.
- Added runtime-neutral named-worker launch requests with Codex and Antigravity
  adapters.
- Added deterministic `discover` project/role generation and review-first,
  AI-assisted `roles enhance` recommendations with explicit apply controls.
- Added compiled CLI acceptance coverage and hardened CI for formatting, vet,
  module integrity, ordinary/race tests, public examples, and Darwin artifacts.
- Added foreground `run-folder` execution with a live terminal dashboard,
  sequence/status commands, elapsed time, logs, and immediate failure reasons.
- Made ordered sequences trustworthy: strict task frontmatter, rendered semantic
  fields, `validate --render`, explicit `STATUS: PASS` and
  `NEXT_PROMPT_SAFE: yes`, persisted failure reasons, timestamps, filtering,
  stale-supervisor reconciliation, and local completion notifications.
- Hardened automatic commits with clean baselines, focused path staging, race
  detection, and ordinary-worker `allowed_paths`/`forbidden_paths` enforcement.
- Added shell-aware `doctor` PATH remediation and repository-local development,
  slice-authoring, CI, and release skills.

RC.2 is release-prepared on its candidate branch but is not tagged, qualified,
or published yet.

## v1.0.0-rc.1

Released as the annotated `v1.0.0-rc.1` tag.

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
