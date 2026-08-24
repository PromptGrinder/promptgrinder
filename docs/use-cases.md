# PromptGrinder use cases

This catalog describes the user-visible workflows PromptGrinder currently
supports. It is the canonical use-case inventory for product development and
release review. Command help and the README remain the detailed interface
reference.

## Repository onboarding

### UC-01: Check local readiness

Use `promptgrinder doctor` to verify the platform, configuration, state home,
supported runtime CLI inventory, Git, repository, generated-worker `PATH`,
headless availability, installed Terminal.app/iTerm2 applications, and selected
terminal adapter without modifying files. Use `--active` only when a visible
terminal launch probe is wanted.

### UC-02: Set up PromptGrinder-owned local state

Running plain `promptgrinder` on an unconfigured installation points to setup
without writing files. Use `promptgrinder setup --dry-run` to scan machine
capabilities and preview first-use files, then `promptgrinder setup` to repeat
the scan and create the minimal local configuration and state after approval.
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
The supported release-candidate surface is macOS (Apple silicon and Intel),
Terminal.app, iTerm2, and headless execution. Codex supports `run` and
`run-folder`; Codex and Antigravity adapters are available for named workers.
Other operating systems, terminals, and engines are not qualified or supported
until explicitly documented.

## Work-order authoring and validation

### UC-05: Validate a work order safely

Use `promptgrinder validate <task.pg>` to check paths, strict frontmatter,
engine configuration, task semantics, and warnings without launching a worker
or creating execution state. When a task file lives outside its target checkout,
use `--repo <repository>` so validation resolves the repository role policy,
working directory, model policy, and effective role/slice boundary correctly.
The output distinguishes explicit and inferred repositories and prominently
warns when an inferred task directory is not a Git repository.
Human output groups the result, target repository, policy, runtime, and any
next-step hint so an external-task failure is actionable without decoding the
engine command preview.

### UC-05a: Fully preflight a folder without running it

Use `promptgrinder validate <folder> --repo <repository>` to run the
same filename, dependency, role, slice-path, clean-baseline, and live-model
preflight as `run-folder`, without creating a sequence or worker. Use it to
review role boundaries and catch external prompt-folder configuration errors
before an execution is authorized. Human output starts and ends with an
unambiguous `Preflight: PASSED` / `FAILED` result. It labels prompts without a
`role` as `unscoped`, so authors can deliberately add a registered role when a
slice needs a role boundary or role model policy. Declared roles must appear in
`.promptgrinder/project.yaml` and have a matching role file. Independent role,
prompt, and dirty-baseline failures are reported together before any worker can
launch. A passed preflight includes a checked list for every runnable prompt
with its resolved engine and model selection, plus the resolved ordered
sequence. `validate-folder` remains available as a
backward-compatible alias. Folder validation accepts the same preflight policy
overrides as `validate-folder`; for example, use `--commit-each=false` when
validating newly authored, uncommitted slices before an execution that will
create per-slice commits.

### UC-06: Inspect the exact engine prompt

Use `promptgrinder validate --render <task.pg>` to inspect the task semantics
preamble and Markdown body exactly as the runtime will receive them. This makes
discarded or unexpectedly small task instructions visible before execution.

### UC-07: Encode task constraints separately from instructions

Use supported frontmatter for acceptance criteria, allowed and forbidden
paths, validation instructions, runtime options, environment values, labels,
working directory, and timeout. Keep executable task instructions in the
Markdown body. PromptGrinder rejects unsupported metadata instead of silently
discarding it.

Use `.pg` when you want to label a prompt as a PromptGrinder slice, or `.md`
when Markdown preview is more useful. Both extensions use the same YAML
frontmatter, safety checks, and ordered-folder behavior.

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
Markdown work-order slices in order. A `00-specification*.(pg|md)` file supplies shared context
without running unless `--include-specification` is selected.
Typed filenames remain supported. A numeric order token may include an optional
uppercase letter suffix, so `08A-ranking-history.pg` and
`08A-implement-ranking-history.md` are valid. A generic ordered filename is
runnable when frontmatter supplies a stable `id` and explicit `type`; optional
`role` becomes the displayed execution identity, and `depends_on` references
must resolve to earlier task IDs before the sequence can start. Slice path
metadata and declared role boundaries are the enforced ordinary run-folder file
policy.

`run-folder --sandbox danger-full-access` overrides the Codex sandbox for the
current invocation only, including a resumed failed slice and later runnable
slices. Completed checkpoints remain untouched. Omit the flag to preserve the
task/default sandbox selection.

Every started sequence prints its copyable cancellation command,
`promptgrinder sequence cancel <sequence-id>`. Cancellation preserves completed
checkpoints and recorded worker evidence.
When a slice declares a role, the role description and allowed paths are also
injected as an outer boundary: changed and expected paths must satisfy both the
role and slice policies. Role quality gates are visible readiness guidance, not
additional validation commands for an intermediate slice.
Before sequence creation, PromptGrinder reports how many slice files are
included, identifies ignored notes, validates all included prompts, and rejects
numbered task-like filenames that would otherwise be silently omitted.
The invoking process also resolves roles and dependencies and checks Git
cleanliness before a detached supervisor is started. Preflight failures print
the actionable reason synchronously and create no sequence state.
Slices use `context_mode: shared` by default, continuing the preceding runtime
session when supported. Set `context_mode: fresh` on a slice to start it without
that session while retaining the specification, committed repository state, and
its declared policy. A successful fresh slice becomes the context ancestor for
later shared slices.
See the [Slice DSL Reference](slice-dsl.md) for the full runnable filename,
frontmatter, path-policy, and completion-report contract.

### UC-16: Stop unsafe sequence continuation

Require every runnable ordered task to finish with `STATUS: PASS` and
`NEXT_PROMPT_SAFE: yes`. Missing, malformed, partial, blocked, or unsafe
completion reports stop the sequence even when the runtime process exits zero.

### UC-17: Run a sequence in the foreground

Use `--detach=false` to keep the sequence in the invoking terminal with prompt
inventory, current activity, elapsed time, and immediate failure reasons.
Finished rows compactly identify the enforced scope and effective runtime, for
example `task.md|slice-policy|codex/gpt-5.6-sol|4m 39s`. An unrestricted task
is labeled `unscoped`. PromptGrinder uses the model reported by the runtime;
when no trustworthy model evidence is available it displays `default` rather
than guessing. Exact worker IDs, paths, policy, and logs remain in worker and
JSON detail. The active prompt uses the same `|`, `/`, `-`, and `\\` spinner as
foreground shared `run` work. Failed rows show a copyable absolute worker-log
path and use a local file hyperlink where the terminal supports it.
When a worker returns `PARTIAL` or `BLOCKED` with an optional structured
failure report, foreground output also shows the completion fields, failure
type, independently blocking checks, evidence-report path, and next action.
It bounds the display and directs users to the worker log only for remaining
detail. The same additive fields are retained in `promptgrinder sequence <id>
--json` and the sequence progress-event JSONL, so automation does not need to
scrape terminal output. An undeclared `STATUS: BLOCKED` is displayed as a
blocked result with its diagnostic, while it remains an ordinary failed worker
for sequencing purposes.

### UC-17a: Select a model within cost and capability policy

Declare repository-approved models in `.promptgrinder/models.yaml` with a
`low`, `medium`, or `high` cost tier and supported capabilities. A role can
provide model defaults, and a slice may choose a specific model or request the
lowest-cost approved model satisfying `engine.max_cost` and
`engine.capabilities`. Role `allowed_paths` remain an outer enforced boundary,
so a low-cost documentation or CI slice cannot modify production code merely
because its task asks it to.

Before a worker or detached sequence starts, PromptGrinder asks the installed
Codex runtime for the active account's selectable catalog. An unknown,
disallowed, unavailable, over-budget, or image-incompatible model fails
preflight without creating a worker or sequence. PromptGrinder never falls back
to another model; a runtime availability change stops the worker with Codex's
retained error.

### UC-18: Run a sequence in the background

Use detached mode to return control to the shell while a local supervisor runs
the sequence. Startup reports whether it is starting, running, already
completed, or failed preflight, then prints the sequence ID and a copyable status command;
completion and failure are retained as local events.

### UC-19: Resume or restart ordered work

PromptGrinder automatically continues an unfinished sequence when the same
folder and repository have an unchanged completed prefix. If a later failed
slice or its role policy was repaired, it prints the compatible sequence,
retained completed slices, and restart point, then reruns only from the first
failed or changed slice. A changed completed slice is never adopted. Use
`--resume` to request this behavior explicitly, `--restart` to rerun the same
sequence from the beginning, `--fresh` to create a new sequence, or
`--no-resume` to avoid existing resume state.

An explicit `--resume` can sometimes select a validation-only preflight record
after a changed completed prefix. That record deliberately has no persisted
`run.json` and is not resumable. PromptGrinder keeps the command failed, names
validation-only preflight as a possible cause, and prints a copyable
`--fresh` command; it never silently starts fresh. Ordinary failed or
interrupted runs that retain valid run state continue to show their normal
`--resume` guidance.

When several interrupted records exist, use
`promptgrinder run-folder <folder> --resume-sequence <sequence-id>` to adopt exactly one named unfinished
sequence. PromptGrinder does not recalculate, copy, or silently rewrite the
requested identity. It requires an exact canonical repository and folder match,
the same prompt names and order, and the same task IDs and dependency graph. A
completed or cancelled sequence, unknown ID, reordered folder, or changed
completed prompt is rejected. The flag is mutually exclusive with `--resume`,
`--fresh`, `--restart`, and `--no-resume`.

Explicit adoption retains the contiguous succeeded/skipped prefix and restarts
at the first failed, interrupted, running, or pending slice. All remaining
slices are revalidated against current role files, model availability, path
policy, engine configuration, and Git-baseline requirements before a worker
launches. Role-policy changes for completed slices are recorded as policy-hash
changes but do not invalidate their unchanged prompt content or cause them to
rerun. The adoption is recorded in the sequence event log and Markdown summary.
Legacy sequence records are accepted when their combined hashes or retained Git
checkpoint/commit evidence prove the same content and dependency identity. If
that proof is unavailable, PromptGrinder fails with a migration message and
leaves the original state untouched.

### UC-19a: Inspect reported run-folder token usage

When a runtime reports structured token usage, `run-folder` stores and displays
the input, cached input, output, reasoning output, and total for each executed
slice, plus the sequence aggregate. These are observed runtime counts, not
currency estimates. A missing runtime usage event is displayed as `unavailable`;
PromptGrinder does not infer token counts from worker prose or logs. Usage from
each recorded recovery attempt is included in that slice's total.

### UC-20: Keep reviewable Git checkpoints

Use `--checkpoint` and `--commit-each` to retain prompt-level Git evidence.
PromptGrinder requires safe baselines when requested and must not include
pre-existing unrelated changes in a focused worker commit. If exactly one
worker commit contains exactly the approved changes and the worktree is clean,
PromptGrinder reports a commit-ownership conflict with the commit SHA and safe
recovery choices. Unrelated paths, multiple commits, residual changes, and
genuine index mismatches retain the generic fail-closed diagnostic.
Dirty preflight diagnostics group the exact modified, added, deleted,
conflicted, and untracked paths. PromptGrinder-owned run state lives below
`PROMPTGRINDER_HOME`, outside the worktree.
With `--commit-each`, the rendered worker prompt explicitly forbids worker-owned
commits and leaves PromptGrinder responsible for the checkpoint. If a worker
commits anyway, the existing exact-commit diagnostic identifies the commit
without amending, resetting, or accepting it.

### UC-20a: Automatically recover one stuck slice

Set `run_folder.recovery_attempts` or pass `--recovery-attempts 1` when a
slice can plausibly repair an obvious execution, reasoning, or completion
report problem after receiving its own failure. PromptGrinder retries only that
slice and preserves the successful prefix, with at most three configured
retries. Model selection, preflight, path-policy, cancellation, and
required-clean-baseline failures remain fail-closed; a non-passing completion
is retried only within the same slice and still blocks later slices until it
reaches `PASS`/safe. Inspect retained evidence and repair the hard-stop
failures before using `--resume`.

### UC-21: Inspect sequence history

Use `promptgrinder sequence <id|current>` for prompt-level progress and
`promptgrinder sequences` for sequence history. Filter the list by prompt
folder when investigating repeated or concurrent workflows.

### UC-21a: Cancel a sequence safely

Use `promptgrinder sequence cancel <sequence-id>` to cancel the active worker
and matching detached supervisor. PromptGrinder marks unfinished slices
cancelled, releases its worktree claim, and preserves completed checkpoints so
the folder can be resumed later.

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
Directory subtrees must use an explicit glob such as `backend/**`; a trailing
slash is rejected with a correction. Optional task `expected_paths` are checked
against allowed and forbidden patterns during preflight. Completion output
prints the exact violating path and reason immediately.

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
an absolute, copyable worker-log path with a local file hyperlink where the
terminal supports it; plain output also preserves the full path.

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
while retaining status, sequence IDs, compact scope/runtime identity, elapsed
time, and failure reasons. Exact worker and log detail remains inspectable with
the status commands and JSON output.

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
## UC-19b — recover safely from an interrupted worker runtime

When a worker log contains the explicit runtime evidence `client disconnection
detected`, `run-folder --recovery-attempts N` may retry only that slice. On a
later `--resume` with `--commit-each` or `--require-clean-git`, PromptGrinder
checks persisted failed-slice evidence before applying the ordinary clean-Git
rejection. Before the retry, it attributes changes against the exact persisted
pre-slice baseline and enforces the allowed/forbidden path policy. Proven
slice-owned untracked output is moved, and tracked output is represented by a
binary patch, under
`$PROMPTGRINDER_HOME/recovery-artifacts/`; the artifact manifest explains how
to inspect and selectively restore it. The retry begins from a clean baseline.

PromptGrinder never resets, cleans, stashes, broadly kills processes, or
auto-commits partial output. A test assertion, compiler error, malformed
completion report, explicit cancellation, timeout, changed Git history, or
ambiguous path is not a recoverable client disconnect and stops with retained
evidence instead.

## UC-19c — repair one declared validation failure in the same worker session

When an implementation worker returns `STATUS: PARTIAL` and
`NEXT_PROMPT_SAFE: no`, PromptGrinder may use one configured recovery attempt
to continue the same runtime session only when the worker log proves that a
command declared in the slice's `validation` list failed and the pre-slice
baseline proves the current diff is exclusively within the slice's allowed,
non-forbidden paths. The repair context names that command and requires the
worker to inspect the retained delta, fix it within scope, rerun validation,
and return `PASS`/`yes` before normal checkpointing can occur.

This is distinct from client-disconnect recovery: validation repair keeps the
scoped delta in place, while a runtime interruption uses a preservation
artifact and a clean retry baseline. Cancellation, timeout, `BLOCKED`, missing
or malformed completion evidence, an unrelated/forbidden path, no reusable
runtime session, another active worker, or a repeated partial result stops
without source cleanup or automatic commit.

## UC-19d — finish a capability audit that product-blocks implementation

A hard-gate audit may complete successfully by proving that an authoritative
data source or prerequisite is unavailable. Such a slice explicitly declares
`gate_outcome: BLOCKED` and returns `STATUS: BLOCKED` with
`NEXT_PROMPT_SAFE: no`. PromptGrinder still enforces its exact baseline and
allowed/forbidden path policy; with `--commit-each`, it checkpoints only the
scoped audit report. It then records the slice as `gate-blocked`, records the
sequence as `product-blocked`, and does not launch dependent implementation
slices.

This is distinct from an execution failure: the terminal summary identifies a
completed capability gate and its product outcome, while an undeclared
`STATUS: BLOCKED` continues to be a failed worker. A product-blocked sequence
cannot auto-recover or resume. Resolve the prerequisite and deliberately start
a new compatible or fresh sequence.
