---
engine:
  name: codex
  sandbox: read-only
working_directory: .
timeout: 5m
labels:
  - smoke
  - read-only
---

Perform a read-only PromptGrinder smoke check.

1. Read the first heading and first paragraph of `README.md`.
2. Report the project name and summarize its purpose in one sentence.
3. Report whether `LICENSE` exists.

Do not create, edit, rename, or delete files. Do not run network commands.
