# Clean-machine release qualification checklist

Use this checklist for the exact tagged source and proposed Homebrew formula. It is an evidence
procedure, not a claim that qualification has occurred. Run it once for every
row in the proposed macOS/architecture matrix using a fresh user account,
ephemeral VM, or equivalent clean environment. A long-lived development
account is not sufficient evidence for every row.

## Evidence rules

Create one evidence directory per run, named with the date, macOS build, and
architecture. Do not record tokens, credential contents, full environment
dumps, or sensitive configuration values. Record:

- tester, UTC start/end, hardware or VM, `sw_vers`, `uname -m`, Terminal and
  iTerm versions, Codex version, and `promptgrinder --version`;
- exact tag, commit, source URL, Homebrew formula revision, commands and exit codes;
- duration, outcome, prompts/dialogs seen, confusing steps, and any deviation;
- redacted command output, screenshots where useful, and before/after Git
  status or state hashes.

Failures remain failures until the fixed release artifact is retested on the
affected row. Never edit a captured result into a pass.

## 1. Source and deterministic preflight

Confirm the annotated tag resolves to the reviewed commit and that the GitHub
release exposes only GitHub's automatic source ZIP and tar.gz links. Confirm
there are no PromptGrinder-uploaded compiled archives, binary-only checksum
files, or build metadata assets. Record the tag and source URL used by the
proposed Homebrew formula.

Before installing persistent state, exercise missing Codex,
unknown/unready Codex, unwritable home, malformed config, non-Git directory,
custom `PATH`, missing iTerm2 when absent, setup dry-run/apply, and repository
and worker non-mutation. Fixture Codex results prove discovery behavior only;
they do not qualify a real Codex installation.

Install through the proposed Homebrew formula on both Apple silicon and Intel,
recording the formula source build and installed version. Do not substitute a
locally prebuilt binary or disable Gatekeeper globally.

## 2. Real first-run onboarding

Start with no `~/.promptgrinder`, no `PROMPTGRINDER_*` variables, and no
repository `.ai/config.yaml`. Install and authenticate Codex using its own
instructions. Do not inspect or copy credential files.

In a disposable repository whose path contains both spaces and Unicode:

```sh
promptgrinder doctor --repo "$PWD" --terminal headless
promptgrinder doctor --repo "$PWD" --terminal headless --json
promptgrinder setup --dry-run
promptgrinder setup
promptgrinder doctor --repo "$PWD" --terminal headless
promptgrinder validate smoke.md
promptgrinder run --dry-run smoke.md
```

Confirm `doctor` is read-only, setup previews every write, dry-run creates no
worker, and human/JSON doctor outcomes agree. When PromptGrinder is not on
`PATH`, confirm doctor identifies zsh, Bash, or Fish and prints a correct
copyable command without editing the shell profile. Copy
`examples/smoke/read-only.md` to `smoke.md` and adjust `working_directory` to
the disposable repository if necessary.

For RC.2, also run deterministic repository discovery in a disposable fixture,
confirm only a new `.promptgrinder/` tree is written, rerun it to prove stable
output, and run `roles enhance` in review-only and rejected modes before testing
an explicitly approved apply operation.

## 3. Adapter smoke runs

Run the read-only smoke task with each claimed adapter:

```sh
PROMPTGRINDER_TERMINAL_ADAPTER=headless promptgrinder run smoke.md --json
promptgrinder doctor --terminal terminal --active
PROMPTGRINDER_TERMINAL_ADAPTER=terminal promptgrinder run smoke.md --json
promptgrinder doctor --terminal iterm --active
PROMPTGRINDER_TERMINAL_ADAPTER=iterm promptgrinder run smoke.md --json
```

For iTerm2, use `skipped` only when it is not installed and the release claim
does not require it on that matrix row. Record every Automation dialog and
which process macOS names as the controller. To test denial, deny the prompt on
a fresh permission state, verify doctor/run report a launch failure with a
concrete System Settings or headless remedy, confirm no Codex process starts,
then reset only through normal System Settings and retest the successful path.
Do not use the release script to change privacy permissions.

For every worker, capture:

```sh
promptgrinder list --events --json
promptgrinder status WORKER_ID --json
promptgrinder events WORKER_ID --json
promptgrinder logs WORKER_ID --json
git status --short
```

Require final status `succeeded`, useful logs/events, and unchanged repository
status for the read-only task. Any mismatch with doctor blocks the release
unless fixed and retested or explicitly accepted as a documented limitation.

## 4. Lifecycle and failure behavior

Use disposable tasks and a disposable repository. Preserve their task files
outside the PromptGrinder home before pruning.

- Cancellation: launch a long-running headless task, cancel its worker, and
  verify `cancelled`, bounded process exit, preserved files, logs, and events.
- Timeout: use a task with a deliberately short timeout; require failed status,
  timeout event/diagnostic, exit classification, and no surviving owned process.
- Reconcile: create or retain a genuinely stale test worker, run
  `reconcile --older-than ...` first, inspect it, then use `--mark-failed`.
  Verify a live process is never marked failed.
- Prune: archive evidence, run `prune --json`, and verify only terminal worker
  directories are removed while global events and useful summaries remain.

Capture the following command families with exact IDs and exit codes:

```sh
promptgrinder list --json
promptgrinder status WORKER_ID --json
promptgrinder events WORKER_ID --json
promptgrinder logs WORKER_ID --json
promptgrinder cancel WORKER_ID --json
promptgrinder reconcile --older-than 1m --json
promptgrinder reconcile --older-than 1m --mark-failed --json
promptgrinder prune --json
```

For ordered folders, cover foreground and detached modes. Require the startup
sequence ID/status command, full inventory, semantic failure reason, strict
PASS/safe continuation, blocked or clarification stop, safe resume, timestamped
sequence output, folder filtering, stale-supervisor reconciliation, and a clean
focused commit that excludes pre-existing or forbidden paths.

## 5. Upgrade, rollback, and uninstall

Start from the latest actually shipped pre-release, not a synthetic state
record. Populate it with non-secret configuration plus successful, failed, and
cancelled worker history. Hash the retained home, replace only the executable,
then verify the new version can read list/status/events/logs and run a new smoke
task. File hashes may legitimately change when the new smoke task is run, so
compare the pre-upgrade hash immediately after `--version` and read-only
doctor, before launching it.

Test the documented rollback using the preserved previous executable.
Before uninstall, cancel all active workers and confirm no PromptGrinder-owned
worker process remains. Remove only the installed binary. Verify configuration,
logs, events, summaries, and worker state remain and document their locations.
Delete retained data only as a separate, explicit tester action.

## 6. Matrix decision

Copy the redacted evidence into the qualification report. Record exact measured
versions; never infer minimum versions from the build toolchain. Each claimed
adapter and architecture must pass on the macOS versions claimed. A deviation
from the supported matrix is a release blocker until fixed and retested.

Have a second person follow the public README from download to successful
result without verbal assistance. Record confusing steps and whether the
five-minute path was achieved.
