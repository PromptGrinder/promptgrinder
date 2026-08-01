---
name: promptgrinder-slice-authoring
description: Break engineering work into ordered Markdown prompts for PromptGrinder run-folder execution. Use when creating or reviewing prompt folders, task frontmatter, dependencies, shared context, completion contracts, commit-each behavior, or resumable PromptGrinder sequences.
---

# PromptGrinder slice authoring

## Design the sequence

1. Inspect the repository and define independently verifiable vertical outcomes.
2. Use numbered runnable files only: `10-implement-*.md`, `20-test-*.md`, `30-verify-*.md`. Use `00-specification-*.md` only for shared non-runnable context.
3. Keep the folder free of stale Markdown files; `run-folder` treats numbered task files as the execution sequence.
4. Make each slice build on committed output from the previous slice. State prerequisites and boundaries explicitly.

## Write safe prompts

- Put executable task instructions in the Markdown body.
- Use only supported frontmatter keys. Current semantic keys include `acceptance_criteria`, `allowed_paths`, `forbidden_paths`, and `validation`; validate the prompt before running it.
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
