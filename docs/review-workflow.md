# Local review and release handoffs

Named workers enter `awaiting_review` only through an evidence-complete local
handoff. A handoff records the implementing worker and task, completion
summary, changed repository paths, commit identifiers (which may be empty),
validation commands and results, a snapshot of all task attempts, and derived
runtime/run/session evidence.

Projects may require implementer/reviewer separation in `.ai/workers.yaml`:

```yaml
project:
  id: example
  name: Example
  require_separate_reviewer: true
```

Reviewers and release roles are ordinary named workers in the same registry.
Their work is assigned and scheduled with `task assign`, `task enqueue`, and
the same worker queue; review does not have a separate execution system.

The local flow is:

```sh
promptgrinder review submit task-id implementer-id \
  --summary "implemented the change" \
  --changed-path internal/example.go \
  --commit abc123 \
  --validation "go test ./...=pass"
promptgrinder review inspect task-id
promptgrinder review accept task-id reviewer-id --reason "verified locally"
```

`review reject` uses one deterministic policy: it appends the rejected decision
to the handoff, preserves every attempt and evidence record, clears the active
implementation assignment, and appends the task to the tail of its original
implementing worker's FIFO. A later implementation attempt creates another
handoff; it never overwrites the rejected one.

All review commands are local. Acceptance does not push, create a pull request,
merge, tag, publish, or release. Those external actions require a separate,
explicit user-invoked workflow.
