# Repository hygiene audit

Audit date: 2026-07-26. Status: in progress; external scanner gates remain.

## Scope and reviewed findings

- Tracked files and the complete reachable Git history were inspected for
  credentials, personal paths/data, generated worker state, proprietary prompt
  content, binaries, and oversized objects.
- The working tree contained an ignored local `promptgrinder` build, `tmp/`,
  local prompt packs, and `.DS_Store`; none were tracked at audit time.
- No tracked PromptGrinder worker state was found.
- The largest public objects are PNG branding assets. The editor-only XCF
  source was removed before publication. No oversized tracked executable was
  found.
- Development roadmaps, specifications, and prompt packs were removed from the
  public tree before publication.
- Maintainer-specific absolute paths are not part of the new user documentation.
  Test source may contain generated temporary paths as fixtures.

## Commands and results

Repository-native checks at the audit revision:

| Command | Result |
| --- | --- |
| Repository-native hygiene review | passed; no forbidden tracked output, unexpected executable, blob over 5 MiB, credential-like history path, or high-confidence credential shape in reachable history |
| Public documentation and examples | passed; required files, local links, public-path checks, example validation, and smoke dry-run |
| `go mod verify` | passed; all modules verified |
| `go vet ./...` | passed |
| `go test ./...` | passed with an isolated `PROMPTGRINDER_HOME` |
| `go test -race ./...` | passed with an isolated `PROMPTGRINDER_HOME` |

Record the revision and exact output in the final release evidence. Findings
from heuristic matches are reviewed, not blindly treated as secrets.

## External release gates

Run current, pinned versions of the following on the full checkout and reachable
history. Store tool versions, commands, raw reports, and false-positive
dispositions with private release evidence:

```sh
gitleaks git --redact --report-format json --report-path gitleaks.json
govulncheck -json ./... > govulncheck.json
go-licenses report ./... > go-licenses.csv
```

These tools were not installed in the local audit environment, so their results
must not be claimed as passing. `go mod verify` is an integrity check, not a
vulnerability or license scan. A release remains blocked until secret, history,
dependency-vulnerability, and dependency-license reports are reviewed.

## Deferred items and handoff

- Measure the supported matrix through clean-machine qualification.
- Enable GitHub private vulnerability reporting and verify the link in
  `SECURITY.md` before making the repository public.
- Archive external scanner reports and reviewer dispositions.
- Run the exact onboarding path in `qualification.md` without verbal help.
