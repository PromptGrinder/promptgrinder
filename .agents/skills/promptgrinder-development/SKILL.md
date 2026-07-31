---
name: promptgrinder-development
description: Implement, diagnose, review, or verify changes in the PromptGrinder Go repository. Use for CLI commands, worker/runtime orchestration, state, configuration, terminal adapters, discovery, role enhancement, run-folder behavior, tests, or contributor documentation.
---

# PromptGrinder development

## Orient

1. Read the user-visible contract in `README.md` and the nearest tests before changing code.
2. Locate behavior with `rg`; preserve package boundaries under `internal/` and keep `cmd/promptgrinder` thin.
3. Treat PromptGrinder as the source of truth for orchestration, lifecycle, state, permissions, worktrees, and runtime selection. Keep execution engines replaceable.
4. Preserve existing user changes and avoid writes outside the requested scope.

## Implement

- Keep CLI orchestration in `internal/cli`; place domain behavior in the owning package.
- Keep runtime-specific behavior behind adapters. Do not leak Codex assumptions into worker definitions or generic state.
- Preserve stdout, stderr, exit-code, JSON-envelope, state-schema, and resume compatibility unless the task explicitly changes a public contract.
- Isolate tests with `t.TempDir()` and `PROMPTGRINDER_HOME`. Use fake executables and context deadlines; never require Codex, network access, GUI terminals, credentials, sleeps, or developer state.
- For subprocess boundaries, assert arguments, environment, working directory, stdin, streams, exit status, and persisted state.
- Use `apply_patch` for edits. Do not overwrite unrelated dirty-worktree changes.

## Verify

Run focused tests first, then the canonical suite with an isolated writable Go cache when needed:

```sh
PROMPTGRINDER_HOME="$(mktemp -d)" GOCACHE=/tmp/promptgrinder-go-cache go test -count=1 ./...
PROMPTGRINDER_HOME="$(mktemp -d)" GOCACHE=/tmp/promptgrinder-go-cache go test -race -count=1 ./...
GOCACHE=/tmp/promptgrinder-go-cache go vet ./...
go mod verify
test -z "$(gofmt -l .)"
git diff --check
```

Do not weaken or skip a failing check. Distinguish sandbox/cache failures from code failures and rerun with isolated paths.

## Hand off

Report the changed public behavior, files, verification results, compatibility impact, and deferred work. Commit only when requested. Never push, tag, publish, or alter release state without explicit authorization.
