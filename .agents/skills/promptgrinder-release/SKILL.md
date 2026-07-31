---
name: promptgrinder-release
description: Prepare, qualify, or review a PromptGrinder release candidate or release. Use for RC readiness, version and tag decisions, changelogs, supported macOS artifacts, checksums, release workflow compatibility, pre-push verification, or release notes.
---

# PromptGrinder release

## Establish release state

1. Confirm the target version with the user. Feature-complete code is not formally an RC until version metadata, tag, and release are intentionally created.
2. Inspect the branch, worktree, recent commits, version source, `README.md`, `.github/workflows/ci.yml`, and `.github/workflows/release.yml`.
3. Separate qualification from publication. Reading, building, and testing are allowed; pushing tags, creating releases, or uploading artifacts requires explicit authorization.

## Qualify

Run:

```sh
PROMPTGRINDER_HOME="$(mktemp -d)" GOCACHE=/tmp/promptgrinder-go-cache go test -shuffle=on -count=1 ./...
PROMPTGRINDER_HOME="$(mktemp -d)" GOCACHE=/tmp/promptgrinder-go-cache go test -race -count=1 ./...
GOCACHE=/tmp/promptgrinder-go-cache go vet ./...
go mod verify
test -z "$(gofmt -l .)"
git diff --check
```

Build the real CLI and verify `--help`, `--version`, validation, dry-run, JSON, and exit-code behavior without a real Codex installation. Use isolated state and fake executables.

Cross-build supported artifacts with `CGO_ENABLED=0` and `-trimpath` for Darwin arm64 and amd64. Verify nonempty Mach-O outputs. Execute only the artifact native to the runner; file inspection is not execution proof.

## Review safety and compatibility

- Confirm documentation matches the candidate version and public commands.
- Review state/config schema defaults and old-state compatibility.
- Confirm no credentials, private prompts, generated binaries, PromptGrinder state, or developer-specific paths are tracked.
- Keep release publication in the release workflow; do not duplicate it in CI.
- Confirm actions are pinned explicitly, permissions are read-only by default, and no live AI or terminal integration runs in CI.

## Report

State whether the candidate is feature-complete, qualified, tagged, and published as four separate facts. Include commits/files, commands and results, artifact/platform limits, compatibility findings, and remaining blockers. Do not call an untagged `rc.1` binary `rc.2`.
