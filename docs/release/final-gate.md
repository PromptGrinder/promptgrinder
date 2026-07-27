# v1.0.0-rc.1 final release gate

Decision: **NO-GO**

Gate date: pending final qualification

Audited source commit: pending final Git checkout

Intended release version: `v1.0.0-rc.1`

Candidate status: untagged and not yet independently reviewed as a release
commit.

No tag, release, Homebrew update, publication, or announcement was made.

## Ordered blockers

1. **Clean-machine and supported-matrix qualification is absent.** Owner:
   release qualification lead. `docs/release/qualification.md` still has no
   completed row for the current or previous macOS major, either architecture,
   real Codex readiness, Terminal.app, iTerm2, lifecycle failures, upgrade,
   rollback, uninstall, or independent documentation-only onboarding. This
   blocks claims about every exact minimum version and supported terminal.
2. **Required external scans are absent.** Owner: security/release reviewer.
   `gitleaks`, `govulncheck`, and `go-licenses` were not installed in the audit
   environment. No pinned-version raw report or reviewed disposition exists
   for full-history secrets, dependency vulnerabilities, or dependency
   licenses.
3. **There is no tagged release artifact set.** Owner: release maintainer after
   blockers 1 and 2 pass and a human authorizes tagging. The guarded release
   path requires an exact annotated `v1.0.0-rc.1` tag. Pre-tag archives prove the
   build mechanics only; their hashes are not publishable release hashes.
4. **Repository-host controls and review are not evidenced.** Owner: repository
   administrator. Confirm branch/tag protection or rulesets, required review,
   Actions permissions, private vulnerability reporting, artifact retention,
   and any protected release environment in GitHub. Repository YAML inspection
   cannot prove these server-side settings.
5. **Upgrade evidence from the latest actually shipped pre-release is absent.**
   Owner: release qualification lead. The repository has no qualifying prior
   binary/tag available locally, and the checklist explicitly disallows a
   synthetic state record as final upgrade evidence.

These are release blockers, not accepted limitations. The documented unsigned
and unnotarized distribution, direct-download installation, deferred Homebrew
support, Codex-only engine, and macOS-only platform are accepted v1 limitations
owned by the release maintainer because they are explicit, internally
consistent, and do not weaken a claimed supported path. Linux, Windows, Warp,
other engines, hosted state, GUI management, remote workers, PR automation,
and automatic installation remain post-v1 product work owned by the product
maintainer.

## Local automated evidence

The following commands were run against the audited source commit with Go
`1.26.4` on `darwin/arm64`. Go tests used isolated writable
`PROMPTGRINDER_HOME` and `GOCACHE` directories because the evaluation sandbox
does not permit writes to the normal user locations.

| Gate | Result |
| --- | --- |
| `test -z "$(gofmt -l .)"` | pass |
| `go vet ./...` | pass |
| Public documentation, links, and examples | pass |
| Repository-native hygiene review | pass; external reports still required |
| `go mod verify` | pass |
| `go test -count=1 ./...` | pass |
| `go test -race -count=1 ./...` | pass |
| `gitleaks git ...` | blocked; tool/report absent |
| `govulncheck -json ./...` | blocked; tool/report absent |
| `go-licenses report ./...` | blocked; tool/report absent |

The first unisolated Go invocation failed only because the sandbox denied
writes to the default Go cache and PromptGrinder home. The isolated rerun is
the gate result above; the environmental failure is not presented as a product
pass or failure.

## Provisional artifact evidence

Earlier pre-tag artifacts were built with the stable `v1.0.0` label against an
older development commit. They are obsolete now that the active candidate is
`v1.0.0-rc.1` and must not be reused, relabelled, or published.

The release-candidate archives and hashes must be generated from the exact
annotated `v1.0.0-rc.1` tag. Archive verification and offline `--version`,
`help`, and `doctor --terminal headless --json` smoke checks must then be rerun
for both architectures. Native Intel and clean-machine evidence remain
required.

## Contract and documentation review

- `README.md`, `docs/RELEASE_POLICY.md`, the documented public compatibility
  surfaces, `doctor`, and release automation consistently describe a
  macOS-only, Codex-only v1 with Terminal.app, iTerm2, and headless adapters.
- Exact OS and application versions are visibly pending rather than invented.
- Existing state formats have a documented non-destructive compatibility
  baseline; legacy events remain readable and future incompatible schemas must
  be migrated or rejected with path-specific remediation.
- The public docs disclose unsigned/notarized status, privacy boundaries,
  persisted explicit environment values, direct-download verification,
  rollback, and retained-state behavior.
- `.github/workflows/release.yml` scopes write permission to the final draft
  job after two architecture smoke jobs and does not publish the draft.
  GitHub-hosted policy remains to be confirmed by an administrator.

## Exact path to a future GO

1. Commit and review the final release-only documentation changes; push the
   reviewed candidate through required CI without feature additions.
2. Run pinned `gitleaks`, `govulncheck`, and `go-licenses`; archive raw reports,
   versions, and reviewer dispositions.
3. With explicit human authorization, create the annotated `v1.0.0-rc.1` tag at the
   reviewed candidate commit. Do not publish it yet.
4. Build once through `scripts/release.sh`, preserve `build-metadata.json` and
   `checksums.txt`, and use those unchanged artifacts for every clean-machine
   matrix row and upgrade/rollback test.
5. Complete `docs/release/clean-machine-checklist.md`, including native arm64
   and amd64, current and previous macOS majors, real Codex readiness, supported
   adapters, lifecycle failures, and independent onboarding. Record exact
   measured versions in `docs/release/qualification.md`.
6. Verify server-side GitHub rules, workflow permissions, draft artifact
   retention, private vulnerability reporting, and failed-publication recovery.
7. Rerun all automated gates at the tagged commit, compare local and CI hashes,
   replace placeholders in the draft notes, and obtain one explicit human
   approval to publish the already-built draft without changing artifacts.

## Publication and failed-publication recovery

After every blocker is closed and the final report says `GO`, push the single
reviewed annotated tag:

```sh
git push origin refs/tags/v1.0.0-rc.1
```

The workflow must build, verify, smoke, and create a draft release. A maintainer
compares the draft artifacts and checksums with the qualified set, replaces
only release-note placeholders, then explicitly publishes the draft. Built
archives must never be manually replaced.

If automation fails before a draft exists, keep the tag and artifacts
unpublished, diagnose the failed job, and fix through a new reviewed commit and
new semantic version rather than moving a public tag. If an unpublished draft
is incorrect, delete only the draft release, preserve its logs/evidence, and
repeat from a new reviewed version. If a published release is defective, do
not rewrite its tag or artifacts: mark it affected, publish a fixed patch
release, and use the rollback procedure below.

## User rollback

With no active workers:

```sh
mv "$HOME/.local/bin/promptgrinder.previous" \
  "$HOME/.local/bin/promptgrinder"
promptgrinder --version
promptgrinder doctor --terminal headless
```

Rollback replaces only the executable. Configuration, logs, events, summaries,
and worker state remain preserved.
