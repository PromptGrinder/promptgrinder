---
name: promptgrinder-ci
description: Diagnose, implement, or review PromptGrinder continuous integration and compiled CLI acceptance coverage. Use for GitHub Actions failures, `.github/workflows/ci.yml`, fake Codex tests, public examples, race/static checks, cache or temporary-path isolation, and supported artifact builds.
---

# PromptGrinder CI

## Diagnose

1. Read the failing GitHub Actions job and exact log before editing.
2. Reproduce the smallest failing command locally with the same environment assumptions.
3. Classify the failure as workflow syntax/context, product behavior, test isolation, platform limitation, or external infrastructure.

## Preserve CI contracts

- Use read-only default repository permissions, explicit action versions, job timeouts, and concurrency cancellation.
- Derive Go from `go.mod`; use dependency caching keyed through `go.sum`.
- Put `PROMPTGRINDER_HOME`, binaries, fixtures, and outputs below `$RUNNER_TEMP` in workflow steps. Do not use `${{ runner.temp }}` in workflow-level contexts where GitHub does not recognize `runner`.
- Never depend on installed Codex, credentials, AI services, network behavior, or live terminal applications.
- Install a failing Codex stub for dry-run checks and prove it was not invoked.
- Use a recording fake Codex for real execution-boundary acceptance tests.

## Required checks

Keep these independently diagnosable:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go mod verify
go test -shuffle=on -count=1 ./...
go test -race -count=1 ./...
```

Build the CLI once per acceptance package/job. Assert stdout, stderr, JSON envelopes, exit codes, arguments, environment, working directory, prompt input, and persisted success/failure state.

Treat committed examples as executable specifications. Validate and safely dry-run every runnable example. If validation resolves an engine executable, provide the fake even when no execution should occur.

When CI or acceptance work introduces or changes user-visible product behavior, update `docs/use-cases.md` in the same change and cover the documented use case at the appropriate test boundary.

For Darwin arm64 and amd64 artifacts, use `CGO_ENABLED=0` and `-trimpath`; verify both as Mach-O and run a smoke test only for the native architecture.

## Finish

Validate YAML with an available parser or Actions linter. Run the full local command set, review expression scopes and temporary paths, and report jobs changed, contracts enforced, commands/results, release-workflow boundaries, and platform limitations. Do not publish releases or upload unnecessary artifacts.
