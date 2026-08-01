# PromptGrinder exploratory testing report

## Executive summary

Verdict: **pass with corrected findings**. Target confidence was 80; achieved confidence is **94/100**. All 59 documented use-case entries received a baseline execution through either the compiled CLI or an executable fake-runtime/headless boundary, plus risk-selected variations. No live AI query, GUI terminal launch, network request, destructive operation, or external side effect was performed.

The baseline commit is `a60f68bc2516bbe427ffff3027b76fd8b9e5c224` on macOS arm64. Two reproducible defects were found: one medium-severity missing run-folder preflight diagnostic and one low-severity contradictory setup dry-run summary. Both were corrected after the initial evidence was frozen, then reproduced successfully against a rebuilt working-tree binary. Neither met the configured failure threshold of high.

Intended CI exit: **0**.

## Confidence

| Category | Score |
| --- | ---: |
| Baseline use-case coverage | 30/30 |
| Risk-weighted exploratory variations | 19/20 |
| Supported flags and interactions | 19/20 |
| Expected results and state transitions | 10/10 |
| Error, boundary, and recovery behavior | 10/10 |
| Reproduction quality and evidence | 5/5 |
| Target and compatibility coverage | 1/5 |
| **Weighted score / mandatory gate** | **94 / 1.0** |

The compatibility score is deliberately limited: only the available macOS arm64 target was executed. macOS amd64 artifacts and real Terminal.app/iTerm2 behavior were not executed. The mandatory gate is satisfied because every use case and application-owned flag received executable coverage; unsafe integrations used fakes rather than being silently skipped.

## Environment and method

- Use cases: `docs/use-cases.md`
- Commit: `a60f68bc2516bbe427ffff3027b76fd8b9e5c224`
- Binary: `/tmp/promptgrinder-exploratory/promptgrinder`
- State: unique `/tmp/promptgrinder-et-*` homes
- Seed: `pg-et-20260801-a60f68b`
- External effects: disabled
- Destructive testing: disabled
- Primary evidence: compiled CLI probes, full Go suite, race suite, focused verbose lifecycle suites

The full suite produced 620 passing package/test events. Focused verbose campaigns executed 98 role/discovery/acceptance tests, 43 worker/task/review tests, and 113 runtime/run-folder/terminal tests. Command-level probes covered readiness, setup, configuration, engine discovery, validation/rendering, dry-run planning, discovery, empty-state observability, recovery, completion generation, themes, and invalid values.

## Use-case coverage

All 59 catalog entries passed their documented baseline (UC-01 through UC-57 plus UC-27a and UC-27b). Coverage groups:

- UC-01–04: compiled readiness, setup, defaults, and engine commands in isolated homes.
- UC-05–14: compiled validation/render/JSON and dry-run probes; fake Codex acceptance for success/failure and shared/independent execution policy.
- UC-15–21: run-folder domain, CLI, persistence, resume/restart, semantic completion, Git checkpoint, detached, foreground, sequence and filtering tests.
- UC-22–27b: deterministic discovery plus compiled fake-advisor and persisted-review acceptance; no live advisor invocation.
- UC-28–45: worker registry/start/control, task FIFO/scheduler, path policy, review handoff and release-boundary tests.
- UC-46–57: empty and populated state observability, events, terminals through fake adapters, reconciliation, lifecycle, pruning, themes/plain mode, completions, JSON, and runtime adapter tests.

The detailed per-use-case machine-readable matrix is in `results.json`; baseline JUnit cases are in `junit.xml`.

## Flag inventory and coverage

Application flags were taken from recursive command help and Cobra registration. Boolean flags were covered omitted and enabled, and negative forms where supported. Closed sets covered valid and invalid representatives. String/duration/list flags covered empty/missing, valid non-default, and invalid values where meaningful.

- Global: `--compact`, `--plain`, `--theme={default,minimal,loud,invalid}`, `--help`, `--version`.
- JSON: all command-local `--json` variants, pretty and compact envelopes.
- Doctor/setup: `--active`, `--repo`, `--terminal`, `--dry-run`, `--yes`, `--non-interactive`, `--replace`, `--backup`.
- Run/validate: engine, sandbox values, shared context, dry run, commit/clean/rollback booleans, concurrent worktree, render.
- Run-folder: resume/fresh/restart/no-resume conflicts, checkpoint/commit/clean, repo/template/engine, specification, concurrent worktree, foreground/detached, hidden supervisor arguments through tests.
- Roles: apply-all, apply-selected, reject-all, selected, safe, JSON and conflicting combinations.
- Worker/task/review/scheduler: runtime, dry-run, timeout, worker filter, queue controls, evidence lists, decisions, once and interval.
- Observability/recovery: events tail/type/severity/follow/global, sequence folder, terminal all/selection, reconcile duration/mark-failed, list events, prune JSON.
- Completion: bash, zsh, fish, PowerShell, and invalid shell.

Evidence: `evidence/command-help.txt`, `evidence/validation-run-dry.txt`, `evidence/interface-completion.txt`, and the executable test logs.

## Defects

### DEF-002 — foreground run-folder hides preflight failure reason

- Severity: **medium**
- Affected: UC-15, UC-19; `--detach=false`, `--plain`
- Reproduction: run a folder containing a numbered unsupported file such as `50-add-role-review-acceptance-and-documentation.md`.
- Expected: nonzero exit plus the unsupported filename and accepted naming patterns.
- Actual: exit 1, stdout only `Result: failed`, empty stderr.
- Reproducibility: 2/2.
- Evidence: `evidence/DEF-002-stdout.txt`, `evidence/DEF-002-stderr.txt`, `evidence/DEF-002-exit.txt`.
- Suspected area (inference): the foreground CLI finalizes the renderer but does not surface errors returned before `run.started`.
- Workaround: rename the prompt to a recognized type or run validation/tests that expose the preflight error.
- Resolution: **corrected and retested**. The foreground command now prints the preflight error before the stable failure result. Evidence: `evidence/DEF-002-retest.txt`.

### DEF-001 — setup dry-run summary contradicts its planned writes

- Severity: **low**
- Affected: UC-02; `setup --dry-run`
- Reproduction: set `PROMPTGRINDER_HOME` to a nonexistent isolated path and run `promptgrinder setup --dry-run`.
- Expected: planned creations followed by a summary stating that this is a preview and nothing was written.
- Actual: planned creations are listed, then `Setup is already complete; no files changed.` Exit is 0 and no files are written.
- Reproducibility: 2/2.
- Evidence: `evidence/DEF-001-stdout.txt`, `evidence/DEF-001-stderr.txt`, `evidence/DEF-001-exit.txt`.
- Suspected area (inference): dry-run summary uses actual mutation count instead of planned mutation count.
- Workaround: trust the individual `(planned)` rows rather than the final sentence.
- Resolution: **corrected and retested**. Dry-run now ends with `Setup preview complete; planned changes were not written.` Evidence: `evidence/DEF-001-retest.txt`.

## Blocked or inconclusive work

None. Safety-sensitive paths were executed through existing fake process, fake terminal, temporary Git repository, and isolated-state boundaries. This is executable coverage, but not qualification of live third-party services.

## Residual risks

- No real Codex/Antigravity request was sent; adapter contracts were tested with controlled executables.
- No Terminal.app or iTerm2 window was opened; adapter logic and UI bytes were exercised with fakes.
- Only macOS arm64 was executed; macOS amd64 remains release-workflow coverage.
- Exploratory concurrency was bounded to deterministic unit/integration tests rather than multiple live AI workers.
- Generated output was inspected as terminal bytes; different terminal emulators may render OSC8/save-restore sequences differently.

## Recommended regression tests

1. Compiled CLI test asserting run-folder preflight errors appear on the documented stream before `run.started`.
2. Setup dry-run test asserting the summary says writes are planned while the filesystem remains unchanged.
3. PTY snapshot test for repeated foreground redraw with wrapped rows and OSC8 links.
4. Release qualification on a native Intel macOS runner.

## Stopping reason

The target confidence was exceeded, every documented use case and supported flag family received executable coverage, all anomalies were reduced and evidenced, and further safe work would primarily duplicate existing coverage or require explicitly disallowed live AI/GUI/external effects.
