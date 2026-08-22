# Changelog

All notable changes are recorded here. This project uses semantic versioning.

## Unreleased

### v1.0.0-rc.5.0 candidate

- Added explicit `gate_outcome: BLOCKED` for hard-gate audit slices. A declared
  gate can checkpoint its path-policy-approved report with `--commit-each`,
  then ends the sequence as `product-blocked` without launching dependent
  implementation slices.
- Kept ordinary `STATUS: BLOCKED` as a failed worker result. Terminal and
  sequence output now distinguish a completed product-blocking capability gate
  from an execution failure and from a completed sequence.
- Product-blocked sequences retain their audit evidence but cannot auto-retry
  or resume until the prerequisite is resolved and a new compatible sequence
  is deliberately started.

### v1.0.0-rc.4.3 candidate

- Show the exact `promptgrinder sequence cancel <sequence-id>` command whenever a run-folder sequence starts.

### v1.0.0-rc.4.2 candidate

- Hardened run-folder recovery after a runtime client disconnect. Known worker
  identity, log evidence, and completion details are retained rather than
  being replaced by an unscoped/default failure.
- Before retrying a failed slice with retained, path-policy-approved changes,
  PromptGrinder moves untracked output and records a binary patch in a durable
  recovery artifact, then restores the proven clean baseline without using a
  reset, clean, stash, or automatic partial commit.
- Recovery remains fail-closed for ordinary validation failures, completion
  contract failures, cancellation, timeouts, path-policy violations, changed
  Git history, and ambiguous changes.
- Added one bounded same-session validation-repair pass for a worker-declared
  `PARTIAL`/`no` result only when its durable log proves a declared validation
  command failed and the retained diff is exclusively within that slice's path
  policy. The repair keeps the scoped diff for the worker to correct; it never
  checkpoints or advances until a later `PASS`/`yes` result succeeds.

RC.4.2 is a compatible recovery-safety candidate. It is not tagged or
published by merging this branch.

### v1.0.0-rc.4.1 candidate

- Added structured, reported Codex token usage to run-folder summaries and
  status output. Per-slice input, cached-input, output, reasoning-output, and
  total values are retained when the runtime reports them; unavailable values
  remain explicitly unavailable rather than being inferred from worker logs.
- Recovery attempts now accumulate reported usage once per attempt, so the
  sequence total represents the observed cost of the complete run rather than
  only the final retry.

RC.4.1 is a compatible observability candidate. It is not tagged or published
by merging this branch.

### v1.0.0-rc.2.4 candidate

- Added repository-owned model policy with explicit low, medium, and high cost
  tiers plus declared capabilities; role defaults and per-slice overrides now
  resolve deterministically without silently escalating to another model.
- Added Codex live-catalog preflight for every resolved model. Unavailable,
  unapproved, over-budget, or incompatible model selections fail before a
  worker starts, while runtime changes retain Codex's failure rather than
  falling back.
- Extended the slice DSL and role guidance to pair model selection with the
  existing enforced role path boundary, including safe examples that keep
  documentation and CI roles out of production code.
- Added opt-in, bounded same-slice automatic recovery for ordered folders.
  Recoverable execution and completion-report failures can retry with their
  prior failure context while preserving the completed prefix; non-passing
  completions retry only the same slice, while model, preflight, path-policy,
  cancellation, and required-clean-baseline failures remain fail-closed.
- Added repository-aware standalone validation with explicit role/slice scope,
  model cost/capability, and sandbox reporting. Added `validate-folder` for
  complete no-launch ordered-folder preflight, including roles, dependencies,
  Git baseline checks, and live Codex model validation.

### v1.0.0-rc.2.3 candidate

- Added first-run machine capability guidance through `setup` and expanded
  `doctor` diagnostics without changing local-first or read-only defaults.
- Added compact run-folder role/runtime identity, synchronous detached
  preflight, external sequence state, explicit detach lifecycle reporting, and
  sequence-level cancellation with resumable checkpoints.
- Hardened slice path policies with directory-pattern suggestions,
  `expected_paths` validation, generated subtree roles, visible enforcement
  reasons, and explicit PromptGrinder commit ownership.
- Improved conflicting `discover` output so it names the file, confirms that
  nothing was written, and gives preservation-first recovery guidance.

RC.2.3 is a stabilization candidate. It remains source-only and is not tagged
or published by merging this branch.

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
