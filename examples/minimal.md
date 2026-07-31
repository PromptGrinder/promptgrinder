---
engine:
  name: codex
  sandbox: workspace-write
working_directory: .
timeout: 20m
labels:
  - example
acceptance_criteria:
  - The requested result is complete and focused tests pass.
allowed_paths:
  - "**"
forbidden_paths:
  - ".env"
validation:
  - Run the focused tests for changed code.
---

Replace this paragraph with one bounded objective.

Expected result:

- Describe the exact change or answer required.
- Add or update focused tests when code changes.
- Report the verification commands and results.

Constraints:

- Do not change unrelated files.
- Preserve pre-existing uncommitted and untracked work.
