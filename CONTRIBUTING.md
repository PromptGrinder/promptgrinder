# Contributing

Thank you for helping improve PromptGrinder.

## Before opening a change

- Use GitHub Discussions or an issue for support and design questions.
- Use GitHub private vulnerability reporting for security issues; do not open a
  public issue.
- Keep changes within the documented v1 product scope.
- Never commit credentials, private prompt packs, generated worker state,
  proprietary prompts, or maintainer-specific paths.

## Development

PromptGrinder requires the Go toolchain pinned in `go.mod`.

```sh
go mod verify
test -z "$(gofmt -l .)"
go vet ./...
go test -shuffle=on -count=1 ./...
go test -race -count=1 ./...
go test -count=1 -v ./acceptance
```

Normal tests do not open terminal applications. Compile the live terminal
integration tests without running them:

```sh
go test -tags=integration -run '^$' ./internal/terminal
```

To execute them intentionally:

```sh
PROMPTGRINDER_LIVE_TERMINAL=1 go test -tags=integration ./internal/terminal
```

Execution may open Terminal.app and iTerm2 and trigger macOS Automation
permission prompts.

Add focused tests for behavior changes. Public commands, JSON meanings,
persisted formats, and successful task files are compatibility surfaces.
Breaking changes require an explicit migration plan.

When exercising automatic commits, start with a clean worktree, pass
`--require-clean-git`, and leave `--commit-each=false` unless the test is
specifically proving focused commits. Put worker instructions in the task body
and use only supported frontmatter metadata.

## Pull requests

Explain the user-facing result, verification performed, compatibility or
security impact, and any deferred work. Keep generated files and unrelated
formatting out of the change. By contributing, you agree that your contribution
is licensed under the repository’s MIT License.
