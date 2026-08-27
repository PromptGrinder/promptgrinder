# Slice DSL Reference

This reference defines the Markdown work-order format accepted by
`promptgrinder validate` and `promptgrinder run-folder`.

Each runnable slice has YAML frontmatter followed by Markdown instructions.
Use `.pg` to label PromptGrinder slices or `.md` for conventional Markdown;
both extensions have identical semantics.
Frontmatter declares machine-checked metadata and constraints; the Markdown
body tells the worker what to do. Frontmatter is not an instruction language.

## Runnable filenames and order

`run-folder` reads visible, top-level `.pg` and `.md` slice files. It runs recognized
slices in lexicographic filename order. `depends_on` validates that a
prerequisite is earlier in that order; it never changes the order.

Use one of these typed names when possible:

```text
10-implement-ranking-history.pg
20-test-ranking-history.pg
30-verify-ranking-history.pg
40-review-ranking-history.pg
```

The supported typed patterns are:

```text
NN[A-Z]*-implement-*.(pg|md)
NN[A-Z]*-test-*.(pg|md)
NN[A-Z]*-verify-*.(pg|md)
NN[A-Z]*-final-verify*.(pg|md)
NN[A-Z]*-review-*.(pg|md)
```

`NN` is exactly two digits and `[A-Z]*` is an optional uppercase letter suffix.
For example, both `10-implement-ranking-history.pg` and
`08A-implement-ranking-history.pg` are typed slices.
`00[A-Z]*-specification*.pg` is shared context and is not run unless
`--include-specification` is set.

Generic names in the form `NN[A-Z]*-*.(pg|md)`, such as
`10-ranking-history-semantics.pg` or `08A-ranking-history-semantics.md`, are
runnable only when frontmatter declares both `id` and `type`.

## Complete slice

For a complete, copyable train structure—including shared specification,
implementation, verification, path boundaries, and the required completion
contract—start with the
[`baseline-train`](../.agents/skills/promptgrinder-slice-authoring/templates/baseline-train)
template. Copy it into the target repository and replace every placeholder
before running validation.

```markdown
---
id: ranking-history-api
type: implement
role: backend-feature
depends_on:
  - ranking-history-model
context_mode: fresh
engine:
  name: codex
  model: gpt-5.6-sol
  max_cost: high
  capabilities: [code, image]
working_directory: .
timeout: 45m
labels:
  - ranking-v4
env:
  FEATURE_FLAG: ranking-v4
acceptance_criteria:
  - The API returns stable ranking-history data for a completed round.
allowed_paths:
  - backend/src/**
  - backend/test/**
forbidden_paths:
  - mobile-android/**
expected_paths:
  - backend/src/main/java/com/example/RankingHistoryController.java
validation:
  - ./mvnw -pl backend test
---
# Add the ranking history API

Implement the endpoint described above. Preserve existing API behavior outside
the new endpoint. Add focused tests and run the declared validation.
```

Only include fields that the slice needs. The example is complete to show the
available DSL; it is not a requirement to set every optional field.

## Frontmatter fields

| Field | Purpose | Rules |
| --- | --- | --- |
| `id` | Stable task identity | Lowercase, hyphen-separated slug. Required for generic `NN[A-Z]*-*.(pg|md)` names and recommended for every dependency-aware sequence. |
| `type` | Slice kind | One of `implement`, `test`, `verify`, or `review`. Required for generic names; must agree with a typed filename. |
| `role` | Declared execution responsibility | Lowercase, hyphen-separated slug. PromptGrinder loads its description and allowed paths as an outer boundary; the slice policy can only narrow that scope. |
| `depends_on` | Earlier prerequisite task IDs | List of `id` values. Every referenced ID must exist and occur earlier in filename order. |
| `lane` | Parallel-worktree lane identity | Lowercase kebab-case identifier. Required for each runnable slice when `run-folder --parallel-worktrees` is used. |
| `priority` | Deterministic lane integration order | Positive integer. Lower values integrate first; equal values use filename order. Required for each runnable slice when `run-folder --parallel-worktrees` is used. |
| `context_mode` | Runtime conversation continuity | `shared` (the default) resumes the preceding runtime session when the engine supports it. `fresh` starts this slice without the preceding session, establishing a deliberate clean-context boundary. The shared specification and current repository state remain available in both modes. |
| `gate_outcome` | Capability-gate product outcome | Optional. The only supported value is `BLOCKED`. Use on an audit slice that may successfully establish an authoritative-data or prerequisite blocker. It requires the worker to return `STATUS: BLOCKED` and `NEXT_PROMPT_SAFE: no`; PromptGrinder checkpoints permitted scoped evidence, marks the sequence `product-blocked`, and does not launch later slices. |
| `engine` | Runtime and model selection | A string engine name, or a mapping with `name`, `model`, `max_cost`, `capabilities`, `profile`, `sandbox`, `approval`, `web_search`, and `images`. `max_cost` is `low`, `medium`, or `high`; `capabilities` uses `text`, `image`, `code`, or `web-search`. |
| `working_directory` | Worker directory relative to the repository | Optional. |
| `timeout` | Worker timeout | Optional duration such as `45m`. |
| `labels` | Run labels | Optional list of strings. |
| `env` | Worker environment values | Optional mapping. Do not put credentials or secrets in a slice. |
| `sandbox`, `approval`, `web_search`, `images` | Top-level engine options | Optional alternatives compatible with the selected engine. |
| `acceptance_criteria` | Observable successful outcomes | A nonempty string or nonempty list of strings. |
| `allowed_paths` | Repository-relative paths the worker may change | Nonempty list when supplied. Use `path/**` for a subtree. |
| `forbidden_paths` | Repository-relative paths the worker may not change | Optional list. Forbidden paths take precedence. |
| `expected_paths` | Concrete files the slice expects to change | Optional list checked against the path policy before launch. |
| `validation` | Commands or checks the worker must perform | A nonempty string or nonempty list of instructions. PromptGrinder renders these for the worker; it does not execute them itself. |

Unknown fields are errors. YAML anchors and aliases are rejected. IDs, roles,
and dependency values must be lowercase hyphen-separated slugs.

## Path patterns

Path patterns are repository-relative and use explicit glob semantics:

```yaml
allowed_paths:
  - backend/src/**       # every path below backend/src
  - README.md            # this exact file only
forbidden_paths:
  - backend/src/secrets/**
```

`backend/src` matches only that exact path, not its contents. A trailing slash
is invalid; use `backend/src/**` for a directory tree. Absolute paths,
repository-escaping paths, and the same pattern in both path lists are
rejected.

## Role inheritance

For a slice with `role`, PromptGrinder loads the matching
`.promptgrinder/roles/<role>.yaml` before sequence creation. It adds the role
description, scope, and advisory readiness gates to an `# Effective Role
Policy` section in the worker prompt.

Role `allowed_paths` are an outer boundary. Every expected path and completed
change must satisfy both the role and slice path policies, so a slice may
narrow its role but cannot broaden it. Legacy role paths that name an existing
directory, such as `backend`, are treated as `backend/**` when used as a role
boundary. New role paths should use explicit `/**` directory patterns.

Role `quality_gates` are not appended to `validation` and are not required for
an intermediate slice. Run only the slice's declared validation unless its task
body explicitly requests additional checks.

## Model policy and role defaults

Optional `.promptgrinder/models.yaml` declares the models a repository permits,
their relative `low`, `medium`, or `high` cost tiers, and capabilities. With a
policy, an explicit `engine.model` must be declared. Without an explicit model,
PromptGrinder uses the role `runtime` defaults and then the policy default; a
cost/capability request selects the lowest-cost configured matching model.

Prompt settings override the same role runtime setting. The resolved model is
checked against the live Codex catalog before launch. This detects account,
provider, and CLI-version availability instead of relying on a stale bundled
list. No fallback model is selected after a validation or runtime failure.

## Required completion report

End every runnable slice with this exact contract and require the worker to
emit it once, after its summary:

```text
STATUS: PASS|PARTIAL|BLOCKED
NEXT_PROMPT_SAFE: yes|no
```

The sequence continues only when the final worker output contains exactly one
`STATUS: PASS` and exactly one `NEXT_PROMPT_SAFE: yes`. Missing, malformed, or
duplicate fields, plus `PARTIAL`, `BLOCKED`, or `no`, stop the sequence.
PromptGrinder also appends this requirement to runnable `run-folder` prompts,
but including it in the slice makes the worker-facing contract unambiguous.

When a slice returns `PARTIAL` or `BLOCKED`, it may also include this optional
bounded failure report before the completion fields. PromptGrinder persists it
with the sequence item, renders it immediately in foreground mode, and exposes
the same fields through `promptgrinder sequence <id> --json` and sequence event
JSONL. This does not change completion semantics.

```text
Failure category: product-test|environment-capability|path-policy|worker-crash|cancellation
Failure summary: one-line reason
Feature evidence:
- completed check
Blocking checks:
- check: failed or not run
  - concise detail
Evidence report: docs/features/.../handoffs/report.md
Next action: repair, configure, or rerun action
```

The report is intentionally optional for compatibility. When absent,
PromptGrinder derives a conservative category from the terminal failure and
retains the worker log as the detailed source.

### Capability-gate outcomes

Ordinarily `STATUS: BLOCKED` is a worker failure and no automatic checkpoint is
made. Declare `gate_outcome: BLOCKED` only for a hard-gate audit whose useful,
successful result can be that product implementation must not proceed. The
worker must still write its report only inside `allowed_paths` and pass the
normal path-policy checks. With `--commit-each`, PromptGrinder commits those
scoped findings, records the completed slice as `gate-blocked`, and ends the
sequence as `product-blocked`; dependent implementation slices remain pending
and are never launched.

PromptGrinder carries this declared outcome through the worker completion
handoff. The sole exception is exactly `STATUS: BLOCKED` with
`NEXT_PROMPT_SAFE: no`; an undeclared `BLOCKED`, malformed completion, or any
other status/safety combination remains a failed worker result.

```yaml
id: authoritative-data-gate
type: implement
gate_outcome: BLOCKED
allowed_paths:
  - docs/decisions/**
```

This is not a successful product delivery and is not recoverable through
`--resume`. After the underlying prerequisite is explicitly resolved, start a
new compatible sequence (or a deliberately fresh one) with updated gate
evidence. Do not use `STATUS: PASS` to conceal a product blocker.

## Opt-in parallel worktree lanes

`run-folder` is sequential by default. Use `--parallel-worktrees` only for
independent, clean-context slices that can be safely committed and merged:

```yaml
---
id: android-location-policy
type: implement
lane: location-policy
priority: 1
context_mode: fresh
allowed_paths:
  - mobile-android/app/src/main/location/**
---
```

The mode requires `--checkpoint --commit-each --require-clean-git`. Each
dependency-eligible slice runs in its own Git worktree and commits only its
permitted files there. PromptGrinder integrates completed lane branches into a
separate coordinator worktree in ascending `priority` (then filename) order.
Only after every merge succeeds does it fast-forward the feature branch.

`depends_on` controls when a slice may start; it is not an integration rank.
A lower-priority lane that finishes early is displayed as
`waiting-to-merge` until every earlier integration is safe. If a lane fails or
an isolated merge conflicts, the feature branch is not changed and the lane
worktrees remain inspectable under the sequence state directory.

Parallel lanes do not share a runtime conversation. Every runnable slice must
declare `context_mode: fresh`, a `lane`, a positive `priority`, and non-empty
`allowed_paths`. Initial RC.6.0 lane execution always starts from a fresh
baseline; `--resume`, `--resume-sequence`, `--restart`, and `--no-resume` are
intentionally rejected until lane-level recovery is durable.
Capability gates remain sequential in this initial mode so their terminal
product-blocked semantics are never confused with lane integration.

## Authoring and preflight

Validate every slice, then inspect the final engine prompt when metadata or
scope is important:

```sh
promptgrinder validate tasks/ranking-v4/10-implement-ranking-history.pg
promptgrinder validate --render tasks/ranking-v4/10-implement-ranking-history.pg
promptgrinder run-folder tasks/ranking-v4 --repo . --commit-each --require-clean-git --detach=false
```

With `--commit-each`, PromptGrinder owns commits. Do not ask workers to run
`git add` or `git commit` in the slice body.
