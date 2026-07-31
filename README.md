# PromptGrinder

**Run AI prompts as deterministic, reviewable engineering workflows.**

## Quick Start

Run multiple prompts with shared context:

    promptgrinder run --shared-context "docs/tasks/02[A-E]-*.md"

Run every prompt in a folder:

    promptgrinder run-folder docs/tasks/

Validate without executing:

    promptgrinder validate docs/tasks/

Inspect available commands:

    promptgrinder --help

Most users only need these four commands to get started.

![PromptGrinder executing five sequential work orders successfully](docs/images/sequential-workflow.png)

## Overview

PromptGrinder is a local-first macOS CLI for engineers who use the Codex CLI.
It validates Markdown work orders, runs them individually or in a defined
sequence, carries context between sequential tasks, and records worker state,
logs, and events for review.

### Core capabilities

- **Ordered execution:** run numbered work orders in a predictable sequence.
- **Shared context:** continue sequential tasks in one resumed Codex session.
- **Reviewable Git history:** checkpoint successful tasks as focused commits.
- **Worker lifecycle:** inspect status, logs, events, cancellation, and stale runs.
- **Deterministic validation:** resolve and validate work orders before launch.
- **Local-first operation:** keep PromptGrinder orchestration state on your machine.
- **Codex integration:** execute through the locally installed Codex CLI.
- **macOS terminals:** use Terminal.app, iTerm2, or headless execution.

## Installation

PromptGrinder `v1.0.0-rc.1` supports macOS on Apple silicon and Intel. It
requires Git and an installed, authenticated
[Codex CLI](https://developers.openai.com/codex/cli/).

### Release archive

Download the archive for your Mac from the
[GitHub releases page](https://github.com/PromptGrinder/promptgrinder/releases):

- Apple silicon: `promptgrinder_v1.0.0-rc.1_darwin_arm64.tar.gz`
- Intel: `promptgrinder_v1.0.0-rc.1_darwin_amd64.tar.gz`

Download `checksums.txt` from the same release, then verify and install. This
example uses the Apple silicon archive:

```sh
grep 'promptgrinder_v1.0.0-rc.1_darwin_arm64.tar.gz' checksums.txt |
  shasum -a 256 -c -
tar -xzf promptgrinder_v1.0.0-rc.1_darwin_arm64.tar.gz
install -d "$HOME/.local/bin"
install -m 0755 \
  promptgrinder_v1.0.0-rc.1_darwin_arm64/promptgrinder \
  "$HOME/.local/bin/promptgrinder"
promptgrinder --version
```

Use the `amd64` archive and directory on Intel. Ensure `$HOME/.local/bin` is on
`PATH`. Release binaries are currently unsigned and not notarized.

### Build from source

The required Go version is pinned in [`go.mod`](go.mod).

```sh
git clone https://github.com/PromptGrinder/promptgrinder.git
cd promptgrinder
go install ./cmd/promptgrinder
promptgrinder --version
```

Homebrew, Linux, Windows, and automatic updates are not supported by this
release candidate.

## Running prompts

The tracked smoke work order reads repository documentation but does not edit
files. Run it from a PromptGrinder checkout:

```sh
promptgrinder --version
promptgrinder doctor --terminal headless
promptgrinder validate examples/smoke/read-only.md
PROMPTGRINDER_TERMINAL_ADAPTER=headless \
  promptgrinder run examples/smoke/read-only.md
promptgrinder list
promptgrinder status <worker-id>
promptgrinder logs <worker-id>
```

Replace `<worker-id>` with the ID printed by `run` or `list`. A successful
worker ends in `succeeded`; `git status --short` should remain unchanged.

For a new work order, copy [`examples/minimal.md`](examples/minimal.md), replace
its objective, and run `promptgrinder validate` before execution.

## Reviewable Git history

Sequential workflows can commit each successful work order separately. This
keeps AI-assisted engineering work aligned with a normal reviewable Git
workflow instead of collecting every change in one commit.

```text
git log --oneline

cc568e36 PromptGrinder: complete 08E-android-obsolete-achievement-orchestration-cleanup.md
b256af84 PromptGrinder: complete 08D-android-obsolete-ranking-orchestration-cleanup.md
55e7e6ab PromptGrinder: complete 08C-android-obsolete-check-in-orchestration-cleanup.md
b8e112d2 PromptGrinder: complete 08B-android-obsolete-scoring-orchestration-cleanup.md
7f8a6dd7 PromptGrinder: complete 08A-android-obsolete-tourism-orchestration-cleanup.md
```

## How PromptGrinder works

1. You describe a bounded engineering task in Markdown.
2. PromptGrinder resolves its repository, metadata, Codex settings, and order.
3. Validation rejects invalid task input before a worker is launched.
4. The worker invokes the local Codex CLI in the selected repository.
5. PromptGrinder records local status, logs, events, and completion summaries.
6. Ordered workflows can pass completion context forward and checkpoint each
   successful task in Git.

`run` accepts independent work orders. `run --shared-context` and `run-folder`
execute sequential work. A failed sequential task stops the sequence.
PromptGrinder does not provide a command that pushes commits, creates pull
requests, publishes releases, or merges branches.

## Examples

- [`examples/minimal.md`](examples/minimal.md) — the smallest useful work order.
- [`examples/multi-step.md`](examples/multi-step.md) — ordered multi-step work.
- [`examples/shared-context.md`](examples/shared-context.md) — sequential tasks
  that build on prior context in a disposable repository.
- [`examples/smoke/read-only.md`](examples/smoke/read-only.md) — safe read-only
  installation verification.

## Main commands

| Command | Purpose |
| --- | --- |
| `run <path...>` | Run one or more work orders |
| `run-folder <folder>` | Run numbered work orders sequentially |
| `validate <task.md>` | Validate a work order without creating a worker |
| `doctor` | Check platform, Codex, Git, configuration, state, and terminal readiness |
| `setup` | Preview or create PromptGrinder-owned first-use files |
| `list` | List workers |
| `status <worker-id>` | Inspect one worker |
| `logs <worker-id>` | Read one worker log |
| `events [worker-id]` | Read or follow worker or global events |
| `sequence <id\|current>` | Inspect an ordered workflow |
| `cancel <worker-id>` | Cancel an active worker |
| `reconcile` | Inspect stale workers and sequences |

Use `promptgrinder --help` and `promptgrinder <command> --help` for the complete
command and option reference. Add `--json` where supported for structured
output.

## Configuration

Configuration precedence, highest first:

1. CLI flags
2. `PROMPTGRINDER_*` environment variables
3. Repository `.ai/config.yaml`
4. User `~/.promptgrinder/config.yaml`
5. User `~/.promptgrinder/templates/default.yaml`
6. Built-in defaults

Inspect resolved settings and their sources:

```sh
promptgrinder defaults
promptgrinder doctor --repo . --json
```

`promptgrinder setup --dry-run` previews first-use writes. `setup` does not
install Codex, authenticate accounts, edit shell profiles, or change macOS
privacy settings.

## Validation and safety model

PromptGrinder provides orchestration controls, not a security guarantee:

- work orders are explicit Markdown files;
- task paths, metadata, repositories, and supported settings are validated;
- ordered work is selected and executed predictably;
- worker state, events, logs, and summaries remain inspectable locally;
- shared-context workflows require Git and a clean worktree by default;
- no hosted PromptGrinder service is required.

PromptGrinder passes the selected sandbox and approval settings to Codex; Codex
performs model execution and tool use. Review work orders and permissions before
running them. Arbitrary AI-generated commands are not inherently safe.

PromptGrinder stores state under
`${PROMPTGRINDER_HOME:-$HOME/.promptgrinder}` and sends no PromptGrinder product
telemetry. Task content is handled by Codex under its own configuration and
terms. See [`SECURITY.md`](SECURITY.md) and [`SUPPORT.md`](SUPPORT.md).

### Named-worker runtimes

Named workers select a runtime by symbolic key in `.ai/workers.yaml`.
PromptGrinder currently registers `codex` and `antigravity` for named-worker
launches. Existing `run` and `run-folder` workflows continue to use the Codex
engine.

Antigravity uses its documented non-interactive JSON mode. Install `agy`
separately and either place it on `PATH` or configure its executable:

```yaml
runtime:
  antigravity:
    executable: /Users/example/.local/bin/agy
    sandbox: true
    mode: accept-edits
    required_capabilities:
      headless: true
      structured_output: true
      working_directory: true
```

Supported Antigravity options are `executable`, `model`, `agent`, `effort`,
`mode`, `sandbox`, `print_timeout`, and
`dangerously_skip_permissions`. PromptGrinder does not advertise Antigravity
session resume because the CLI does not currently document a conversation ID
in headless results; resume therefore starts a new retained task attempt.
All named-worker launches require headless operation, structured output, and
working-directory selection by default. A runtime may additionally require
`interactive`, `session_resume`, `sandbox`, `approval`, or `environment` under
its namespaced `required_capabilities` mapping; PromptGrinder rejects a missing
capability before adapter preflight or process launch.

## Platform support

The `v1.0.0-rc.1` release target is:

- macOS on Apple silicon (`darwin/arm64`);
- macOS on Intel (`darwin/amd64`);
- the Codex CLI as the only execution engine;
- Terminal.app, iTerm2, and headless execution.

Exact qualified macOS, Codex, Terminal.app, and iTerm2 versions remain pending
clean-machine qualification. Linux and Windows may be technically compilable
in part, but they are untested and unsupported for this release candidate.
Source builds require the Go version declared in [`go.mod`](go.mod).

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
```

Ordinary tests do not open terminal applications. Compile the live macOS
integration tests without running them:

```sh
go test -tags=integration -run '^$' ./internal/terminal
```

Run them intentionally with:

```sh
PROMPTGRINDER_LIVE_TERMINAL=1 \
  go test -tags=integration ./internal/terminal
```

This may open Terminal.app and iTerm2 and trigger macOS Automation prompts.
See [`CONTRIBUTING.md`](CONTRIBUTING.md) before submitting a change.

## Documentation and support

- [Worker runtime use cases](docs/product/worker-runtime-use-cases.md)
- [Release policy](docs/RELEASE_POLICY.md)
- [Release qualification](docs/release/qualification.md)
- [Release-candidate notes](docs/release/v1.0.0-rc.1-release-notes.md)
- [Support](SUPPORT.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

PromptGrinder was created and is maintained by
[Andreas Nyberg](AUTHORS.md).

## License

PromptGrinder is available under the [MIT License](LICENSE).
