#!/bin/zsh
set -euo pipefail

readonly source_repository="PromptGrinder/promptgrinder"
readonly tap_repository="PromptGrinder/homebrew-tap"
readonly formula="PromptGrinder/tap/promptgrinder"

fail() {
  print -u2 "homebrew update: $*"
  exit 1
}

release_metadata() {
  local tag=${1:-}
  [[ "$tag" =~ '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' ]] || \
    fail "invalid release tag '$tag'; expected vMAJOR.MINOR.PATCH with an optional semantic prerelease suffix"

  local version=${tag#v}
  local url="https://github.com/$source_repository/archive/refs/tags/$tag.tar.gz"
  print "tag=$tag"
  print "version=$version"
  print "url=$url"
}

plan_update() {
  local tag=${1:-}
  release_metadata "$tag" >/dev/null
  local version=${tag#v}
  local url="https://github.com/$source_repository/archive/refs/tags/$tag.tar.gz"
  local archive checksum

  archive=$(mktemp "${TMPDIR:-/tmp}/promptgrinder-source.XXXXXX")
  trap "rm -f -- ${(q)archive}" EXIT
  curl --fail --location --silent --show-error --output "$archive" "$url" || \
    fail "could not download source archive for $tag"
  [[ -s "$archive" ]] || fail "downloaded source archive for $tag is empty"
  checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
  [[ "$checksum" =~ '^[0-9a-f]{64}$' ]] || fail "could not calculate SHA-256 for $tag"
  rm -f -- "$archive"
  trap - EXIT

  release_metadata "$tag"
  print "sha256=$checksum"
}

verify_published_release() {
  local tag=${1:-}
  release_metadata "$tag" >/dev/null
  [[ -n "${GH_TOKEN:-}" ]] || fail "GH_TOKEN is required to verify a published release"

  local ref release_tag draft
  ref=$(gh api "repos/$source_repository/git/ref/tags/$tag" --jq '.ref') || \
    fail "tag $tag does not exist in $source_repository"
  [[ "$ref" == "refs/tags/$tag" ]] || fail "tag lookup returned unexpected ref '$ref'"

  release_tag=$(gh api "repos/$source_repository/releases/tags/$tag" --jq '.tag_name') || \
    fail "tag $tag does not have a published GitHub release"
  draft=$(gh api "repos/$source_repository/releases/tags/$tag" --jq '.draft') || \
    fail "could not inspect GitHub release for $tag"
  [[ "$release_tag" == "$tag" && "$draft" == "false" ]] || \
    fail "GitHub release for $tag is not published"
  print "published release verified: $tag"
}

check_existing_update() {
  local version=${1:-}
  [[ -n "$version" ]] || fail "formula version is required"
  release_metadata "v$version" >/dev/null
  [[ -n "${GH_TOKEN:-}" ]] || fail "GH_TOKEN is required to inspect tap pull requests"

  brew tap PromptGrinder/tap >/dev/null
  local current_version
  current_version=$(brew info --json=v2 "$formula" | jq -r '.formulae[0].versions.stable // empty')
  [[ -n "$current_version" ]] || fail "could not read the current $formula version"
  if [[ "$current_version" == "$version" ]]; then
    print "skip=true"
    print "reason=formula already contains version $version"
    return
  fi

  local title="promptgrinder $version"
  local existing
  existing=$(gh pr list --repo "$tap_repository" --state open --json title \
    --jq "map(select(.title == \"$title\")) | length")
  [[ "$existing" =~ '^[0-9]+$' ]] || fail "could not inspect existing tap pull requests"
  if (( existing > 0 )); then
    print "skip=true"
    print "reason=open pull request already exists: $title"
    return
  fi

  print "skip=false"
  print "reason=formula update is required"
}

bump_formula() {
  local version=${1:-}
  local url=${2:-}
  local checksum=${3:-}
  local dry_run=${4:-true}
  [[ -n "$version" ]] || fail "formula version is required"
  release_metadata "v$version" >/dev/null
  [[ "$url" == "https://github.com/$source_repository/archive/refs/tags/v$version.tar.gz" ]] || \
    fail "source URL does not match exact version $version"
  [[ "$checksum" =~ '^[0-9a-f]{64}$' ]] || fail "SHA-256 must contain exactly 64 lowercase hexadecimal characters"
  [[ "$dry_run" == "true" || "$dry_run" == "false" ]] || fail "dry_run must be true or false"

  local -a args=(
    bump-formula-pr "$formula"
    "--url=$url"
    "--sha256=$checksum"
    "--version=$version"
    --strict
    --no-browse
    --no-fork
  )
  if [[ "$dry_run" == "true" ]]; then
    args+=(--dry-run)
    print "dry run: validating formula update for $version"
  else
    [[ -n "${HOMEBREW_GITHUB_API_TOKEN:-}" && -n "${GH_TOKEN:-}" ]] || \
      fail "HOMEBREW_GITHUB_API_TOKEN and GH_TOKEN are required for a tap update"
    print "creating Homebrew formula pull request: promptgrinder $version"
  fi
  brew "${args[@]}"
}

case "${1:-}" in
  metadata) release_metadata "${2:-}" ;;
  plan) plan_update "${2:-}" ;;
  verify-published) verify_published_release "${2:-}" ;;
  check) check_existing_update "${2:-}" ;;
  bump) bump_formula "${2:-}" "${3:-}" "${4:-}" "${5:-true}" ;;
  *) fail "usage: $0 metadata|plan|verify-published|check|bump ..." ;;
esac
