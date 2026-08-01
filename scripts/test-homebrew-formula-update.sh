#!/bin/zsh
set -euo pipefail

repo_root=${0:A:h:h}
script="$repo_root/scripts/homebrew-formula-update.sh"
workflow="$repo_root/.github/workflows/homebrew-update.yml"
release_workflow="$repo_root/.github/workflows/release.yml"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/promptgrinder-homebrew-test.XXXXXX")
trap 'rm -rf -- "$fixture"' EXIT

assert_contains() {
  local value=$1 expected=$2
  [[ "$value" == *"$expected"* ]] || {
    print -u2 "expected '$expected' in: $value"
    exit 1
  }
}

stable=$($script metadata v1.2.3)
assert_contains "$stable" 'version=1.2.3'
assert_contains "$stable" 'url=https://github.com/PromptGrinder/promptgrinder/archive/refs/tags/v1.2.3.tar.gz'

prerelease=$($script metadata v1.0.0-rc.2.2)
assert_contains "$prerelease" 'version=1.0.0-rc.2.2'
assert_contains "$prerelease" 'refs/tags/v1.0.0-rc.2.2.tar.gz'

for invalid in 1.0.0 vv1.0.0 v1.0 v1.0.0- v01.0.0 v1.00.0 v1.0.00 'v1.0.0+build'; do
  if $script metadata "$invalid" >"$fixture/invalid.out" 2>"$fixture/invalid.err"; then
    print -u2 "malformed tag unexpectedly accepted: $invalid"
    exit 1
  fi
  grep -q 'invalid release tag' "$fixture/invalid.err"
done

grep -Fq 'types: [published]' "$workflow"
grep -Fq 'workflow_dispatch:' "$workflow"
grep -Fq 'default: true' "$workflow"
grep -Fq 'permissions:' "$workflow"
grep -Fq 'contents: read' "$workflow"
grep -Fq 'github.event.release.tag_name || inputs.release_tag' "$workflow"
grep -Fq 'actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803' "$workflow"
grep -Fq 'HOMEBREW_GITHUB_API_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}' "$workflow"
grep -Fq 'GH_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}' "$workflow"
if grep -Fq 'HOMEBREW_TAP_TOKEN' "$release_workflow"; then
  print -u2 'tag-triggered draft-release workflow must not update Homebrew'
  exit 1
fi

mkdir -p "$fixture/bin"
cat >"$fixture/bin/curl" <<'EOF'
#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) shift; output=$1 ;;
    https://*) url=$1 ;;
  esac
  shift
done
test -n "$output" && test -n "$url"
printf 'source archive fixture' > "$output"
printf '%s\n' "$url" > "$PROMPTGRINDER_TEST_CURL_URL"
EOF
chmod 0755 "$fixture/bin/curl"
cat >"$fixture/bin/shasum" <<'EOF'
#!/bin/sh
printf '%s  %s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$3"
EOF
chmod 0755 "$fixture/bin/shasum"
cat >"$fixture/bin/brew" <<'EOF'
#!/bin/sh
case "$1" in
  tap) exit 0 ;;
  info)
    printf '{"formulae":[{"versions":{"stable":"%s"}}]}\n' "$PROMPTGRINDER_TEST_FORMULA_VERSION"
    ;;
  bump-formula-pr)
    printf '%s\n' "$@" > "$PROMPTGRINDER_TEST_BREW_ARGS"
    ;;
  *) exit 91 ;;
esac
EOF
chmod 0755 "$fixture/bin/brew"
cat >"$fixture/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$PROMPTGRINDER_TEST_GH_ARGS"
printf '%s\n' "${PROMPTGRINDER_TEST_OPEN_PRS:-0}"
EOF
chmod 0755 "$fixture/bin/gh"
export PROMPTGRINDER_TEST_BREW_ARGS="$fixture/brew.args"
export PROMPTGRINDER_TEST_GH_ARGS="$fixture/gh.args"
export PROMPTGRINDER_TEST_CURL_URL="$fixture/curl.url"
export PROMPTGRINDER_TEST_FORMULA_VERSION=0.9.0

PATH="$fixture/bin:$PATH" $script plan v1.0.0-rc.2.2 >"$fixture/plan.out"
grep -qx 'version=1.0.0-rc.2.2' "$fixture/plan.out"
grep -qx 'sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$fixture/plan.out"
grep -qx 'https://github.com/PromptGrinder/promptgrinder/archive/refs/tags/v1.0.0-rc.2.2.tar.gz' "$fixture/curl.url"

PATH="$fixture/bin:$PATH" $script bump \
  1.0.0-rc.2.2 \
  https://github.com/PromptGrinder/promptgrinder/archive/refs/tags/v1.0.0-rc.2.2.tar.gz \
  aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  true >"$fixture/dry-run.out"

grep -qx 'bump-formula-pr' "$fixture/brew.args"
grep -qx 'PromptGrinder/tap/promptgrinder' "$fixture/brew.args"
grep -qx -- '--url=https://github.com/PromptGrinder/promptgrinder/archive/refs/tags/v1.0.0-rc.2.2.tar.gz' "$fixture/brew.args"
grep -qx -- '--sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$fixture/brew.args"
grep -qx -- '--version=1.0.0-rc.2.2' "$fixture/brew.args"
grep -qx -- '--strict' "$fixture/brew.args"
grep -qx -- '--no-browse' "$fixture/brew.args"
grep -qx -- '--no-fork' "$fixture/brew.args"
grep -qx -- '--dry-run' "$fixture/brew.args"
grep -q 'dry run:' "$fixture/dry-run.out"

export GH_TOKEN=test-read-only-token
export PROMPTGRINDER_TEST_FORMULA_VERSION=1.0.0-rc.2.2
PATH="$fixture/bin:$PATH" $script check 1.0.0-rc.2.2 >"$fixture/current.out"
grep -qx 'skip=true' "$fixture/current.out"
grep -q 'formula already contains version 1.0.0-rc.2.2' "$fixture/current.out"

export PROMPTGRINDER_TEST_FORMULA_VERSION=1.0.0-rc.2.1
export PROMPTGRINDER_TEST_OPEN_PRS=1
PATH="$fixture/bin:$PATH" $script check 1.0.0-rc.2.2 >"$fixture/pr.out"
grep -qx 'skip=true' "$fixture/pr.out"
grep -q 'open pull request already exists: promptgrinder 1.0.0-rc.2.2' "$fixture/pr.out"
grep -qx -- '--repo' "$fixture/gh.args"
grep -qx 'PromptGrinder/homebrew-tap' "$fixture/gh.args"

export PROMPTGRINDER_TEST_OPEN_PRS=0
PATH="$fixture/bin:$PATH" $script check 1.0.0-rc.2.2 >"$fixture/update.out"
grep -qx 'skip=false' "$fixture/update.out"
grep -q 'formula update is required' "$fixture/update.out"

print 'homebrew formula update tests passed'
