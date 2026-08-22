---
name: promptgrinder-slice-authoring
description: Break engineering work into ordered Markdown prompts for PromptGrinder run-folder execution. Use when creating or reviewing prompt folders, task frontmatter, dependencies, shared context, completion contracts, commit-each behavior, or resumable PromptGrinder sequences.
---

# PromptGrinder slice authoring

## Design the sequence

Start from `templates/baseline-train/` in this skill when authoring a new
train. Copy it into the target repository, replace every placeholder, then add
only the slices the feature needs; it is a baseline, not a required two-slice
limit.

1. Inspect the repository and define independently verifiable vertical outcomes.
2. Prefer typed numbered files such as `10-implement-*.md`, `20-test-*.md`, and `30-verify-*.md`. An ordering number can have an uppercase suffix, such as `08A-implement-*.md`. Existing descriptive `NN[A-Z]*-*.md` filenames are runnable when frontmatter declares a stable `id` and explicit `type`. Use `00[A-Z]*-specification*.md` only for shared non-runnable context.
3. Keep the folder free of stale Markdown files; `run-folder` treats numbered task files as the execution sequence. Generated reports, handoffs, and evidence must not begin with a runnable number (for example, use `capability-audit-report.md`, not `01-capability-audit-report.md`). Validate the final folder after each generated artifact is added.
4. Make each slice build on committed output from the previous slice. State prerequisites and boundaries explicitly.

## Resolve hard gates before a commit-each train

- Do not place a capability audit that may legitimately return a product `BLOCKED` outcome before runnable implementation slices in the same `run-folder` sequence. `STATUS: BLOCKED` / `NEXT_PROMPT_SAFE: no` correctly stops the runner, but also makes `--commit-each` unable to commit the blocker report automatically.
- Run such an audit as its own sequence, review and commit its evidence, then author or enable the implementation train only when the gate is ready. If a user explicitly requires a complete runnable future graph up front, retain its dependent `.pg` files at the top level, make the hard dependency explicit, and state that the run is expected to stop before unsafe implementation. Do not move or hide those slices: that changes the requested execution graph.
- Do not disguise a product blocker as `STATUS: PASS` merely to continue into implementation. A successful report and a safe next no-op are different from permission to implement.

## Write safe prompts

- Put executable task instructions in the Markdown body.
- Use only supported frontmatter keys. Declare `id`, `type`, `role`, and `depends_on` for descriptive filenames and dependency-aware trains. Semantic keys include `acceptance_criteria`, `allowed_paths`, `forbidden_paths`, and `validation`; validate the prompt before running it.
- Treat `role` as declared execution identity for ordinary folder runs; the slice's `allowed_paths` and `forbidden_paths` remain its enforced path policy. Do not claim that generated role YAML permissions are automatically merged.
- Keep paths repository-relative. Make forbidden paths override allowed paths.
- State concrete behavior, tests, compatibility expectations, and non-goals. Avoid vague requests such as “finish the feature.”
- Every sequence that adds or changes a user-visible feature must include updating `docs/use-cases.md` in the implementing slice or a dedicated documentation slice before final verification.
- Do not instruct a nested worker to run `git add` or `git commit` when the outer sequence uses `--commit-each`; the supervisor owns Git metadata.
- Do not request network access, real AI calls, GUI terminal launches, credentials, or publishing unless explicitly required and authorized.
- End every runnable prompt with this exact required contract:

```text
STATUS: PASS|PARTIAL|BLOCKED
NEXT_PROMPT_SAFE: yes|no
```

Require `PASS` and `yes` for continuation. A clarification, missing field, malformed/duplicate field, `PARTIAL`, `BLOCKED`, or `no` must stop the sequence.

## Run and recover

Prefer a clean worktree and focused supervisor commits:

```sh
promptgrinder run-folder tmp/<sequence>/ \
  --repo . \
  --commit-each \
  --require-clean-git \
  --detach=false
```

Use `promptgrinder sequence <sequence-id>` to inspect status and the worker log for evidence. Resume only after correcting the actual failure. If a prompt itself requires a commit while the supervisor owns commits, correct the prompt instead of retrying indefinitely.

## Review the folder

Before handing it off, validate every prompt, check numbering/order and path policies, ensure no nested Git ownership conflict, and confirm the final slice runs the full repository verification suite.
