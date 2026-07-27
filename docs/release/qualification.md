# v1.0.0-rc.1 qualification record

Status: not qualified; release blocking.

Exact minimum versions must be measured from release artifacts. Do not replace
“pending” with an assumption.

Procedure:
[clean-machine qualification checklist](clean-machine-checklist.md).
Commit only redacted evidence or stable links to access-controlled artifacts.

| Target | Version | Architecture | Result | Evidence |
| --- | --- | --- | --- | --- |
| Current macOS major at release | pending | arm64 | pending | clean-machine run required |
| Current macOS major at release | pending | amd64 | pending | clean-machine run required |
| Previous macOS major at release | pending | arm64 | pending | clean-machine run required |
| Previous macOS major at release | pending | amd64 | pending | clean-machine run required |
| Codex CLI | pending | n/a | pending | `doctor` and smoke output required |
| Terminal.app | pending | n/a | pending | opt-in live check required |
| iTerm2 | pending | n/a | pending | opt-in live check required |
| Headless | pending | both | pending | release artifact smoke required |

## Clean-machine onboarding path

1. Download the correct release archive and `checksums.txt`.
2. Verify SHA-256 before extracting.
3. Install the binary into an existing executable search path.
4. Install and authenticate Codex using Codex’s own instructions.
5. Run `promptgrinder doctor --terminal headless`.
6. If needed, preview and perform `promptgrinder setup`, then rerun `doctor`.
7. Clone the release repository and run the tracked read-only smoke example.
8. Inspect the resulting worker with `list`, `status`, `logs`, and `events`.
9. Confirm success and an unchanged `git status --short`.

Record tester, date, hardware, exact versions, all commands/output, permission
prompts, artifact checksum, and deviations. A reviewer must follow this path
without verbal help before v1.0 publication.

## Run records

| Run ID | Tester/date | macOS build | Architecture | Artifact SHA-256 | Result | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| pending | pending | pending | pending | pending | pending | clean-machine run required |

## Negative onboarding results

| Scenario | Expected outcome | Result | Evidence/fix retest |
| --- | --- | --- | --- |
| Missing Codex | Required doctor failure; concrete install/config remedy; no worker/repository mutation | pending | clean-machine run required |
| Codex readiness unknown/not authenticated | Required doctor failure; Codex-owned authentication remedy; no credential inspection | pending | clean-machine run required |
| Missing iTerm2 | Required failure only when iTerm is selected; headless/Terminal remedy | pending | clean-machine run required |
| Denied Automation permission | Distinct launch failure and System Settings/headless remedy; no Codex process | pending | fresh permission state required |
| Unwritable home override | Required doctor failure before worker creation | pending | clean-machine run required |
| Malformed configuration | Named source/parse failure before worker creation | pending | clean-machine run required |
| Non-Git directory | Repository workflow rejected without valuable mutation | pending | clean-machine run required |
| Custom shell `PATH` | Codex and PromptGrinder remain discoverable in generated worker environment | pending | clean-machine run required |

## Workflow and lifecycle results

| Check | Result | Evidence/fix retest |
| --- | --- | --- |
| Documentation-only onboarding | pending | independent reviewer required |
| Headless smoke | pending | release artifact and real Codex required |
| Terminal.app smoke | pending | live clean-machine run required |
| iTerm2 smoke where installed | pending | live clean-machine run required |
| list/status/events/logs | pending | worker evidence required |
| cancellation and process cleanup | pending | worker/process evidence required |
| timeout and process cleanup | pending | worker/process evidence required |
| reconcile dry-run and mark-failed | pending | stale-worker evidence required |
| prune preservation boundary | pending | before/after state evidence required |
| upgrade/state preservation | pending | latest shipped pre-release required |
| rollback | pending | installed previous binary required |
| uninstall/no active workers/retained data | pending | clean-machine evidence required |

## Deviations, fixes, and blockers

- Release blocker: no clean-machine evidence has been recorded yet.
- Release blocker: exact qualified macOS, Codex, Terminal.app, and iTerm2
  versions remain unmeasured.
- Add every failure with its issue/fix reference and the release artifact used
  for the successful retest. Unresolved supported-matrix deviations block
  publication.
