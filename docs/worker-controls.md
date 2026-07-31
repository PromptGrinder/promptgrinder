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
