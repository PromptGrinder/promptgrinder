# ADR 0001: Persistent named workers are separate from execution runs

Status: Accepted

PromptGrinder will represent a long-lived project role with a repository-owned
worker definition in `.ai/workers.yaml`. Its stable ID is a project-scoped
slug, its runtime is a symbolic registry key, and its identity and policy
cannot be widened or changed by an assigned task.

RC.2 repository discovery uses a separate, intentionally non-executable model:
`.promptgrinder/project.yaml` and `.promptgrinder/roles/*.yaml` hold generated
and enhanced role suggestions. They do not become named workers until a user
authors an explicit `.ai/workers.yaml` definition. PromptGrinder performs no
silent conversion or activation between these configuration surfaces.

PromptGrinder-owned mutable worker state and assigned tasks live beneath
`${PROMPTGRINDER_HOME}/projects/<project-id>/`. Repository definitions and
local state/task documents carry explicit schema versions. Mutable state is
authoritative for named-worker lifecycle.

The existing `internal/state.Worker` and
`${PROMPTGRINDER_HOME}/workers/<execution-run-id>/` records continue to mean
one execution attempt. They are not named-worker definitions or named-worker
state, and this decision does not rename or migrate them.

The initial named-worker lifecycle is:

```text
idle -> starting -> executing
starting -> failed
executing -> idle | blocked | awaiting_review | failed
blocked -> executing
awaiting_review -> idle
failed -> idle
```

Runtime adapters may report facts, but PromptGrinder validates and owns these
transitions. Core project, worker, policy, task, and lifecycle contracts remain
runtime-neutral and must not import a runtime-specific adapter.

Path policy is an orchestration guardrail. PromptGrinder snapshots the Git
working tree before a named runtime starts, attributes subsequent changes
without claiming unchanged pre-existing user edits, and blocks progression
when changed paths fall outside allowed rules or match a forbidden rule.
Forbidden rules take precedence. Violating changes remain available for human
inspection and recovery; PromptGrinder does not revert, stash, delete, commit,
or otherwise overwrite them.

This enforcement complements runtime sandboxing. It does not replace a runtime
sandbox and is not a complete security boundary: it detects repository changes
at safe checkpoints rather than preventing arbitrary process or filesystem
access.
