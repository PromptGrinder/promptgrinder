# Parallel PromptGrinder worktree train

Copy this directory into a target repository and replace every placeholder.
Use it only when lanes own separate paths and can validate independently.

Run it from the repository root:

```sh
promptgrinder run-folder <folder> --repo . --parallel-worktrees --fresh \
  --checkpoint --commit-each --require-clean-git --detach=false
```

`depends_on` controls eligibility to start; `priority` controls integration
order. Every runnable lane has a unique `lane`, `context_mode: fresh`, and a
non-empty `allowed_paths` list. Keep generated reports outside this folder or
give them non-runnable names.
