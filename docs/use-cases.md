# PromptGrinder use cases

This catalog describes the user-visible workflows PromptGrinder currently
supports. It is the canonical use-case inventory for product development and
release review. Command help and the README remain the detailed interface
reference.

## Repository onboarding

### UC-01: Check local readiness

Use `promptgrinder doctor` to verify the platform, configuration, state home,
Git, configured runtimes, and terminal adapter without changing files. Use
`--active` only when a visible terminal launch probe is wanted.

### UC-02: Set up PromptGrinder-owned local state

Use `promptgrinder setup --dry-run` to preview first-use files, then
`promptgrinder setup` to create the minimal local configuration and state.
When writes are planned, the dry-run summary identifies the output as a
preview and confirms that the planned changes were not written.
Setup does not install or authenticate an AI runtime, edit shell profiles, or
change operating-system privacy settings.

### UC-03: Inspect effective configuration

Use `promptgrinder defaults` to understand resolved defaults, configuration
locations, and override precedence. Use environment variables, repository
configuration, or CLI flags to change execution behavior without redesigning
work orders or workers.

### UC-04: Inspect runtime support

Use `promptgrinder engines` and `promptgrinder engines describe <engine>` to
see registered execution engines and their capabilities before assigning work.

## Work-order authoring and validation

### UC-05: Validate a Markdown work order safely

Use `promptgrinder validate <task.md>` to check paths, strict frontmatter,
engine configuration, task semantics, and warnings without launching a worker
or creating execution state.

### UC-06: Inspect the exact engine prompt

Use `promptgrinder validate --render <task.md>` to inspect the task semantics
preamble and Markdown body exactly as the runtime will receive them. This makes
discarded or unexpectedly small task instructions visible before execution.

### UC-07: Encode task constraints separately from instructions

Use supported frontmatter for acceptance criteria, allowed and forbidden
paths, validation instructions, runtime options, environment values, labels,
working directory, and timeout. Keep executable task instructions in the
Markdown body. PromptGrinder rejects unsupported metadata instead of silently
discarding it.

### UC-08: Produce automation-friendly validation results

Use `--json` and optionally `--compact` where supported to consume validation,
planning, state, and error envelopes from scripts without scraping decorated
terminal output.

## One-off execution

### UC-09: Run one work order

Use `promptgrinder run task.md` to launch a local worker for a single explicit
Markdown task.

### UC-10: Select several work orders

Pass files, directories, or quoted wildcard patterns to `promptgrinder run` to
resolve and execute a deterministic set of tasks.

### UC-11: Preview a run without invoking AI

Use `promptgrinder run --dry-run ...` to resolve, validate, and display the
execution plan without launching Codex or creating worker execution state.

### UC-12: Run independent tasks concurrently

Run several ordinary work orders as separate workers when they do not need a
shared conversation. PromptGrinder records each worker independently and can
open configured terminal sessions for visible execution.

### UC-13: Run related tasks in shared context

Use `promptgrinder run --shared-context ...` when selected prompts must execute
sequentially in one runtime context. Git cleanliness, checkpoints, per-prompt
commits, and rollback-on-failure protect the shared worktree by default.

### UC-14: Override execution policy for a run

Use flags such as `--engine`, `--sandbox`, `--require-clean-git`,
`--commit-each`, `--rollback-on-failure`, and
`--allow-concurrent-worktree` when an explicit run needs policy different from
the configured defaults.

## Ordered prompt folders

### UC-15: Execute an implementation plan as numbered slices

Use `promptgrinder run-folder <folder>` to execute recognized numbered
Markdown tasks in order. A `00-specification*.md` file supplies shared context
without running unless `--include-specification` is selected.
Before sequence creation, PromptGrinder reports how many Markdown files are
included, identifies ignored notes, validates all included prompts, and rejects
numbered task-like filenames that would otherwise be silently omitted.
Foreground failures before sequence state exists print the actionable
preflight reason.

### UC-16: Stop unsafe sequence continuation

Require every runnable ordered task to finish with `STATUS: PASS` and
`NEXT_PROMPT_SAFE: yes`. Missing, malformed, partial, blocked, or unsafe
completion reports stop the sequence even when the runtime process exits zero.

### UC-17: Run a sequence in the foreground

Use `--detach=false` to keep the sequence in the invoking terminal with prompt
inventory, current activity, elapsed time, worker IDs, compact log links, and
immediate failure reasons.

### UC-18: Run a sequence in the background

Use detached mode to return control to the shell while a local supervisor runs
the sequence. Startup prints the sequence ID and a copyable status command;
completion and failure are retained as local events.

### UC-19: Resume or restart ordered work

Use `--resume` to continue an unfinished sequence, `--restart` to rerun the
same sequence from the beginning, `--fresh` to create a new sequence, or
`--no-resume` to avoid existing resume state.

### UC-20: Keep reviewable Git checkpoints

Use `--checkpoint` and `--commit-each` to retain prompt-level Git evidence.
PromptGrinder requires safe baselines when requested and must not include
pre-existing unrelated changes in a focused worker commit.

### UC-21: Inspect sequence history

Use `promptgrinder sequence <id|current>` for prompt-level progress and
`promptgrinder sequences` for sequence history. Filter the list by prompt
folder when investigating repeated or concurrent workflows.

## Project and role discovery

### UC-22: Discover a repository deterministically

Run `promptgrinder discover` at a repository root to detect supported
languages, frameworks, build tools, CI, documentation, infrastructure, and
project structure without an AI call. It creates a new `.promptgrinder/`
project manifest, role YAML files, and context directory without overwriting
existing files. If an existing generated target differs from current analysis,
discovery reports the exact conflict, confirms that nothing was written, and
explains how to preserve and reconcile the existing configuration before
trying again.

### UC-23: Discover roles for a monorepo

Generate project-level roles for supported areas such as backend, Android,
frontend, infrastructure, CI, testing, and documentation. Each role uses
allowed paths to distinguish its responsibility inside the shared repository.

### UC-24: Review AI-assisted role improvements

Use `promptgrinder roles enhance` to collect bounded repository, build, CI,
documentation, and skill evidence and request structured recommendations from
the configured advisor runtime once. PromptGrinder validates, deterministically
refines, and persists the exact proposal outside the repository. The default is
review-only and writes no role YAML; non-interactive, piped, and JSON runs save
the review without prompting.

### UC-25: Enhance roles in a large monorepo

PromptGrinder applies deterministic global evidence budgets while prioritizing
workflows, nested build manifests, public documentation, and skills. This lets
large repositories receive grounded recommendations without exceeding advisor
request limits.

### UC-26: Apply only approved role changes

Use `--apply-selected <recommendation-id>` to apply chosen changes or
`--apply-all` for non-removal recommendations. Removals always require
individual selection. PromptGrinder, not the AI runtime, validates and merges
YAML while preserving unrelated user-authored fields.

### UC-27: Reject a role-enhancement proposal

Use the default review mode or `--reject-all` to inspect or reject all
recommendations without writing project files.

### UC-27a: Resume a persisted role review without AI

Use `promptgrinder roles reviews` and `promptgrinder roles review <id|latest>`
to find and inspect saved reviews. `roles refine`, `roles apply`, and `roles
reject` use only the persisted proposal. They do not invoke the advisor, and a
source-hash mismatch stops application before role YAML is changed.

### UC-27b: Edit and approve destructive role changes explicitly

In an interactive review, edit structured proposed values or decide items one
at a time. `roles apply <id> --safe` applies additions only; replacements,
conflicts, and removals require `--selected <item-id>`. EOF, interruption,
rejection, inspection, and save-for-later leave role YAML unchanged.

## Persistent named workers

### UC-28: Define project-owned engineering roles

Declare named workers in `.ai/workers.yaml` with stable identity, project,
runtime, branch policy, worktree policy, allowed paths, and forbidden paths.
Definitions are reviewable project configuration rather than repeated prompts.

### UC-29: Inspect named-worker definitions

Use `promptgrinder worker list` and `promptgrinder worker show <worker-id>` to
inspect available roles and policy before assigning or starting work.

### UC-30: Start a specialized worker

Use `promptgrinder worker start <worker-id>` to load its definition, current
task, durable state, runtime adapter, branch/worktree policy, and injected
identity. Use `--dry-run` to inspect launch preparation without execution.

### UC-31: Preserve worker identity across tasks

Reuse the same named worker for many assignments while PromptGrinder remains
the source of truth for state and task ownership. The runtime is an execution
engine, not the durable worker record.

### UC-32: Isolate worker changes

Use allowed and forbidden paths, branch naming, clean-worktree requirements,
and managed worktree selection to keep workers inside their responsibilities.
Violations remain available for review rather than being silently reverted.

### UC-33: Change runtimes without changing roles

Select a symbolic runtime in the worker definition. PromptGrinder currently
supports Codex and Antigravity adapters for named workers and negotiates
required capabilities before launch.

## Tasks, queues, and scheduling

### UC-34: Assign a task to a worker

Use `promptgrinder task assign <worker-id> <task.md>` to make work active when
the worker is idle or append it to that worker's FIFO queue.

### UC-35: Queue work without activating it

Use `promptgrinder task enqueue <worker-id> <task.md>` to place work at the end
of a worker queue regardless of current idle state.

### UC-36: Inspect tasks and attempts

Use `promptgrinder task list` and `promptgrinder task show <task-id>` to inspect
ownership, status, attempts, retained evidence, and current assignment.

### UC-37: Manage queue order

Use `task queue list`, `task queue reorder`, and `task queue remove` to inspect
and edit pending FIFO work without deleting completed attempt evidence.

### UC-38: Dispatch eligible workers

Use `promptgrinder scheduler run --once` for one scheduling decision or run the
local scheduler loop continuously. Project and per-runtime concurrency limits
bound dispatch.

### UC-39: Retry or cancel task work

Use `task retry` to create a new retained attempt and `task cancel` to stop
active or queued work without erasing prior evidence.

## Lifecycle, review, and release handoff

### UC-40: Pause and resume a named worker

Use `worker pause` for a graceful local stop and `worker resume` to continue a
paused worker while preserving durable task and attempt state.

### UC-41: Inspect named-worker status

Use `worker status <worker-id>` to see lifecycle, assignment, queue, runtime,
branch, worktree, and failure or review state.

### UC-42: Reset recoverable worker state

Use `worker reset <worker-id>` when an explicit local reset is required. State
transitions remain owned by PromptGrinder rather than inferred from a runtime
session.

### UC-43: Submit completed work for local review

Use `review submit <task-id> <implementer-worker-id>` to create a durable local
handoff containing implementation evidence.

### UC-44: Inspect and decide a review

Use `review show` (or `review inspect`), `review accept`, and `review reject` to
record reviewer decisions. Rejection preserves evidence and requeues work under
the configured policy.

### UC-45: Keep review and release local

Review commands do not merge, push, publish, or create releases. A human or an
explicitly authorized release workflow remains responsible for external Git
and publication actions.

## Observability and recovery

### UC-46: List one-off execution workers

Use `promptgrinder list` or `promptgrinder workers` to inspect local execution
records separately from project-owned named-worker definitions.

### UC-47: Inspect worker status and logs

Use `promptgrinder status <worker-id>` and `promptgrinder logs <worker-id>` to
inspect execution state and retained output. Interactive run-folder output uses
a compact clickable `worker.log` label while plain output preserves the full
path.

### UC-48: Follow structured events

Use `promptgrinder events [worker-id]` to inspect or follow local worker events
for automation, debugging, and lifecycle auditing.

### UC-49: Manage PromptGrinder terminal sessions

Use `promptgrinder terminals` to list managed tabs and
`promptgrinder terminals kill` to close a selected PromptGrinder terminal
without treating unrelated terminal windows as managed workers.

### UC-50: Reconcile stale state

Use `promptgrinder reconcile` to identify stale active workers and sequences
whose owning process or supervisor no longer exists and repair eligible local
state explicitly.

### UC-51: Record explicit lifecycle outcomes

Use top-level `complete`, `fail`, and `cancel` commands when an integration or
operator must record a worker lifecycle outcome explicitly.

### UC-52: Prune completed local state

Use `promptgrinder prune` to remove eligible completed execution state while
retaining active work and respecting the command's safety checks.

## Interface and operating modes

### UC-53: Use interactive terminal output

Use the default interactive interface for the PromptGrinder logo, theme colors,
spinners, elapsed time, prompt inventory, and compact links. Select a supported
theme with `--theme`.

### UC-54: Use stable plain output

Use `--plain` to disable colors, animation, and terminal control sequences
while retaining status, sequence IDs, worker IDs, logs, elapsed time, and
failure reasons.

### UC-55: Integrate PromptGrinder into local automation

Use JSON-capable commands, deterministic exit codes, isolated
`PROMPTGRINDER_HOME` directories, and headless runtime adapters in scripts and
CI-like local checks. PromptGrinder remains local-first and does not require a
hosted orchestration service.

### UC-56: Generate shell completion

Use `promptgrinder completion <shell>` to generate the supported completion
script for a local shell and integrate command and flag discovery into the
user's preferred shell configuration explicitly.

### UC-57: Run privately with replaceable execution engines

Keep project configuration, worker state, tasks, queues, logs, and review
evidence on the local machine. AI providers receive only the execution or
advisor input required by their configured adapter and remain replaceable.
Beginning with `v1.0.0-rc.2.2`, GitHub releases retain GitHub-generated source
archives without attaching compiled binaries or binary-only checksums; macOS
installation will be supported through Homebrew from tagged source.

### UC-58: Propose a Homebrew update after publication

Publishing a stable or prerelease GitHub release starts a separate, source-only
Homebrew workflow. It preserves the exact tag suffix, calculates the tagged
source checksum, and opens a reviewable formula pull request when an identical
version or pull request does not already exist. Drafts and tag pushes alone do
not update the tap. Tap CI and manual maintainer review remain required; the
workflow never auto-merges.

## Product boundaries

PromptGrinder orchestrates engineering work; it does not provide a coding
model, replace Git, silently merge or publish work, guarantee that AI-generated
commands are safe, or turn discovered role proposals into active named workers
without explicit project configuration.
