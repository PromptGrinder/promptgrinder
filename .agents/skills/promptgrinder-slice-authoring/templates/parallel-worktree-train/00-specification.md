# <Feature name> — shared parallel-worktree specification

## Outcome

Describe the user-visible outcome, non-goals, compatibility expectations, and
the product decision source of truth.

## Parallel lane boundaries

- `<lane-a>` owns: <paths and outcome>.
- `<lane-b>` owns: <paths and outcome>.
- `<verification>` depends on all implementation lane IDs and owns final
  evidence only.

## Integration and final gate

List the required feature/component checks. Lower `priority` lanes integrate
first; dependencies control when later work may start.
