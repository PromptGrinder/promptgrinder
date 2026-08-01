# Homebrew formula updates

PromptGrinder updates its Homebrew tap through a reviewable pull request. When
a GitHub release is published, `.github/workflows/homebrew-update.yml` reads the
exact release tag, validates it, downloads GitHub's immutable tagged-source
archive, and calculates its SHA-256 checksum. It then asks Homebrew's supported
`brew bump-formula-pr` command to update `PromptGrinder/tap/promptgrinder`.

Stable releases and prereleases are supported. Only the leading `v` is removed
from the formula version, so `v1.0.0-rc.2.2` remains `1.0.0-rc.2.2`. Draft
releases and tag pushes do not trigger a tap update. If the formula already has
the version, or an open `promptgrinder VERSION` pull request exists, the
workflow exits successfully without creating a duplicate.

The generated pull request changes the source `url`, explicit `version`, and
`sha256`. Homebrew preserves the rest of the formula, including its build,
linker flags, license, dependency, and test. The workflow never force-pushes or
automatically merges the pull request. Tap CI must pass and a maintainer must
review and merge it manually.

## Manual dry run

Maintainers can validate a published or proposed tag without creating a branch
or pull request:

```sh
gh workflow run homebrew-update.yml \
  --repo PromptGrinder/promptgrinder \
  -f release_tag=v1.0.0-rc.2.2 \
  -f dry_run=true
```

A manual non-dry run additionally verifies that the tag exists and belongs to
a published GitHub release. Do not set `dry_run=false` merely to test access;
that mode is authorized to create a tap branch and pull request.

## Token management

`HOMEBREW_TAP_TOKEN` is a fine-grained token stored as an Actions secret in
`PromptGrinder/promptgrinder`. Restrict it to `PromptGrinder/homebrew-tap` with
only Contents and Pull requests read/write access. The source repository's
built-in `GITHUB_TOKEN` remains read-only; only the final non-dry-run steps
receive the tap token.

To rotate the token:

1. Create and, if required by the organization, approve a replacement
   fine-grained token with the same repository and permission restrictions.
2. Replace the `HOMEBREW_TAP_TOKEN` Actions secret in
   `PromptGrinder/promptgrinder`.
3. Revoke the old token.
4. Run this workflow manually with `dry_run=true`. A dry run does not consume
   the tap token or write to the tap.

Never commit, echo, or paste the token into workflow logs. GitHub organization
approval and the token's effective repository permissions must be verified by
a maintainer in GitHub settings.
