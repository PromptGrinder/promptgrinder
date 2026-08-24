# Worker control semantics

PromptGrinder owns pause, resume, retry, and cancellation state. Runtime facts
do not directly choose lifecycle transitions.

Pause sends `SIGTERM` only to the PID recorded for the named worker's active
attempt. It waits for the configured timeout (10 seconds by default), then
sends `SIGKILL` to that same PID. Missing/already-exited processes count as a
graceful stop. It never signals process groups or unrelated workers.

Resume uses a runtime session only when the selected adapter explicitly
advertises session-resume capability and implements the resume operation. A
session identifier alone is not support. Otherwise PromptGrinder retains the
old attempt and starts a new attempt. The Codex adapter currently advertises
that session resume is unsupported.

Retry always appends a new attempt; cancel retains every earlier attempt,
session, run identifier, log, summary, and changed-file record. Control
requests and their outcomes are appended to the task record. Repeating an
operation whose target state is already satisfied is a no-op.

## Ordered slice automatic recovery

`run-folder` can make a bounded retry of the current failed slice with
`run_folder.recovery_attempts` or `--recovery-attempts` (0 through 3; default
0). It does not rerun the completed prefix. The retry receives the prior
failure and completion reason, keeps the same role, path, model, and safety
boundaries, and is stored with its recovery count and `prompt.recovering`
event.

Only failures that may be corrected within the same slice are eligible. A
`BLOCKED`, partial, or malformed completion can retry the same slice, but no
later slice starts until a retry reports `PASS` and `NEXT_PROMPT_SAFE: yes`.
PromptGrinder never auto-retries model selection or other preflight failures,
path-policy violations, cancellation, or a dirty baseline required by the run.
It never substitutes a different model. After the configured bound or a
non-recoverable failure, the sequence remains failed with its evidence intact
and can be repaired and resumed explicitly.

## Capability-gate outcome

An ordinary `STATUS: BLOCKED` remains an execution failure. A slice that
declares `gate_outcome: BLOCKED` is the explicit exception: its completed
`BLOCKED`/`no` report is a successful capability audit with a product-blocked
outcome. PromptGrinder applies the usual Git baseline, clean-worktree, and
allowed/forbidden path checks, and may checkpoint its scoped report with
`--commit-each`; it then stops before launching later slices.

This outcome is never retried automatically and cannot be resumed as if the
gate had passed. Resolve the prerequisite, update the work order, and start a
new compatible or fresh sequence. No worker runtime semantics, cleanup, or
ordinary failed-worker handling change.
