# Baseline PromptGrinder slice train

Copy this directory into the target repository (for example,
`docs/prompts/my-feature/` or `tasks/my-feature/`) before editing it. Replace
every `<placeholder>` and remove optional fields that do not apply. The files
are intentionally named and ordered for `promptgrinder run-folder`.

1. Write accepted behaviour, boundaries, prerequisites, and final validation
   in `00-specification.md`.
2. Split independently verifiable outcomes into additional numbered slices;
   each slice consumes only committed predecessor output.
3. Keep allowed paths narrow, list forbidden adjacent surfaces, and declare
   only validation the slice must actually run.
4. Run `promptgrinder validate <folder> --repo .` before starting the train.

Use a separate, committed gate sequence for a capability or product decision
that can legitimately return `BLOCKED`; do not place it before implementation
slices in a `--commit-each` train.

Run the completed train from the repository root:

```sh
promptgrinder run-folder <folder> --repo . --checkpoint --commit-each \
  --require-clean-git --detach=false
```

Every runnable slice must end with the exact completion contract already shown
in the templates. The startup output prints the sequence cancellation command.
