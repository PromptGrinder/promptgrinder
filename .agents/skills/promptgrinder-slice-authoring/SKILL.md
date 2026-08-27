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

For an explicitly parallel feature, start from
`templates/parallel-worktree-train/` instead. It is valid only with
`run-folder --parallel-worktrees`; retain sequential execution unless isolated
lanes provide a real benefit.

1. Inspect the repository and define independently verifiable vertical outcomes.
2. Prefer typed numbered files such as `10-implement-*.md`, `20-test-*.md`, and `30-verify-*.md`. An ordering number can have an uppercase suffix, such as `08A-implement-*.md`. Existing descriptive `NN[A-Z]*-*.md` filenames are runnable when frontmatter declares a stable `id` and explicit `type`. Use `00[A-Z]*-specification*.md` only for shared non-runnable context.
3. Keep the folder free of stale Markdown files; `run-folder` treats numbered task files as the execution sequence. Generated reports, handoffs, and evidence must not begin with a runnable number (for example, use `capability-audit-report.md`, not `01-capability-audit-report.md`). Validate the final folder after each generated artifact is added.
4. Make each slice build on committed output from the previous slice. State prerequisites and boundaries explicitly.

## Author parallel worktree lanes

Use this mode only when each lane can own a non-empty, repository-relative
write scope and be validated independently. Every runnable lane must declare:

- `lane`: a unique lowercase kebab-case identity;
- `priority`: a positive integer; lower priorities integrate first and equal
  priorities use filename order;
- `context_mode: fresh`: lane workers cannot share an engine session;
- non-empty `allowed_paths`, plus `forbidden_paths` for adjacent lane-owned
  surfaces where useful.

`depends_on` controls launch eligibility, not merge rank. A dependent lane
starts only after its prerequisite has integrated through the coordinator.
Independent lanes may start together; a completed later-priority lane is
expected to show `waiting-to-merge` until earlier integrations finish.

Run the completed train only with:

```sh
promptgrinder run-folder <folder> --repo . --parallel-worktrees --fresh \
  --checkpoint --commit-each --require-clean-git --detach=false
```

PromptGrinder uses separate lane and coordinator worktrees and fast-forwards
the feature branch only after safe integration. Do not place a capability gate
inside the current parallel mode. Inspect trains from any terminal with
`promptgrinder sequence list`. A failed lane remains inspectable, but durable
individual-lane restart is not yet supported; resolve it and deliberately
start a fresh parallel train.

## Resolve hard gates before a commit-each train

- Complete capability and feasibility audits before creating a PromptGrinder implementation train. A blocked result means do not dispatch the train; a ready audit becomes source evidence for executable slices. `gate_outcome: BLOCKED` remains an exceptional safety mechanism for a known hard gate, not the normal workflow for discovering whether a feature should be sliced.
- Reserve this declared gate contract for a completed audit with a known product/prerequisite outcome. An undeclared `STATUS: BLOCKED`, or a declared gate whose worker cannot actually perform the audit, remains an ordinary worker failure: it must retain evidence and must not masquerade as a completed product decision.
- Do not rely on frontmatter parsing or a `runfolder` fake-launcher test alone when introducing or changing a gate outcome. Prove the full CLI → runtime → worker-result → run-folder path preserves the declared outcome: the worker process must not convert the valid gate result into an exit failure before `run-folder` can checkpoint it. The expected end-to-end result is `product-blocked`, one checkpointed gate report, and zero launched dependents.
- After changing PromptGrinder source, rebuild the exact executable that the invoking shell resolves before a costly run. Verify `command -v promptgrinder` and `promptgrinder --version` show the intended revision; a stale Homebrew or prior Go-bin binary can pass preflight yet omit new runtime behavior. Use a fresh shell or re-source its PATH configuration when the executable location changed.
- If a user explicitly requires a complete runnable future graph up front, retain its dependent `.pg` files at the top level, make the hard dependency explicit, and state that the run is expected to product-block before unsafe implementation. Do not move or hide those slices: that changes the requested execution graph.
- Do not disguise a product blocker as `STATUS: PASS` merely to continue into implementation. A successful report and a safe next no-op are different from permission to implement.

## Write safe prompts

- Put executable task instructions in the Markdown body.
- Use only supported frontmatter keys. Declare `id`, `type`, `role`, and `depends_on` for descriptive filenames and dependency-aware trains. Semantic keys include `acceptance_criteria`, `allowed_paths`, `forbidden_paths`, `validation`, and the hard-gate-only `gate_outcome: BLOCKED`; validate the prompt before running it.
- Treat `role` as the outer execution boundary and align ordinary implementation slices with it: use the full owned module (for example `backend/**` or `mobile-android/**`) rather than a list of individual source files. Use a test role with only the relevant `src/test/**` subtree for test-only work, and `docs/**` for documentation-only verification.
- Narrow a module grant only when slices will genuinely run concurrently, a shared/generated contract needs one explicit owner, or a worker is intentionally test- or documentation-only. Otherwise, serialize module work through dependencies; do not create brittle exclusions that prevent required compile-closure changes.
- The slice's `allowed_paths` and `forbidden_paths` remain the enforced policy and can only narrow a registered role's boundary. Do not claim that generated role YAML permissions are automatically merged.
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
