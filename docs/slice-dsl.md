# Slice DSL Reference

This reference defines the Markdown work-order format accepted by
`promptgrinder validate` and `promptgrinder run-folder`.

Each runnable slice has YAML frontmatter followed by Markdown instructions.
Frontmatter declares machine-checked metadata and constraints; the Markdown
body tells the worker what to do. Frontmatter is not an instruction language.

## Runnable filenames and order

`run-folder` reads visible, top-level Markdown files only. It runs recognized
slices in lexicographic filename order. `depends_on` validates that a
prerequisite is earlier in that order; it never changes the order.

Use one of these typed names when possible:

```text
10-implement-ranking-history.md
20-test-ranking-history.md
30-verify-ranking-history.md
40-review-ranking-history.md
```

The supported typed patterns are:

```text
NN[A-Z]*-implement-*.md
NN[A-Z]*-test-*.md
NN[A-Z]*-verify-*.md
NN[A-Z]*-final-verify*.md
NN[A-Z]*-review-*.md
```

`NN` is exactly two digits and `[A-Z]*` is an optional uppercase letter suffix.
For example, both `10-implement-ranking-history.md` and
`08A-implement-ranking-history.md` are typed slices.
`00[A-Z]*-specification*.md` is shared context and is not run unless
`--include-specification` is set.

Generic names in the form `NN[A-Z]*-*.md`, such as
`10-ranking-history-semantics.md` or `08A-ranking-history-semantics.md`, are
runnable only when frontmatter declares both `id` and `type`.

## Complete slice

```markdown
---
id: ranking-history-api
type: implement
role: backend-feature
depends_on:
  - ranking-history-model
engine:
  name: codex
  model: gpt-5.6-sol
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
| `id` | Stable task identity | Lowercase, hyphen-separated slug. Required for generic `NN[A-Z]*-*.md` names and recommended for every dependency-aware sequence. |
| `type` | Slice kind | One of `implement`, `test`, `verify`, or `review`. Required for generic names; must agree with a typed filename. |
| `role` | Declared execution responsibility | Lowercase, hyphen-separated slug. It labels the worker; slice path policy remains the enforced file boundary. |
| `depends_on` | Earlier prerequisite task IDs | List of `id` values. Every referenced ID must exist and occur earlier in filename order. |
| `engine` | Runtime selection | A string engine name, or a mapping with `name`, `model`, `profile`, `sandbox`, `approval`, `web_search`, and `images`. |
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

## Authoring and preflight

Validate every slice, then inspect the final engine prompt when metadata or
scope is important:

```sh
promptgrinder validate tasks/ranking-v4/10-implement-ranking-history.md
promptgrinder validate --render tasks/ranking-v4/10-implement-ranking-history.md
promptgrinder run-folder tasks/ranking-v4 --repo . --commit-each --require-clean-git --detach=false
```

With `--commit-each`, PromptGrinder owns commits. Do not ask workers to run
`git add` or `git commit` in the slice body.
