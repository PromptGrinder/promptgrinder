#!/bin/zsh
set -euo pipefail

release_dir=${1:-dist}
version=${2:-}
revision=${3:-}
release_dir=${release_dir:A}

if [[ -z "$version" || -z "$revision" ]]; then
  print -u2 "usage: scripts/verify-release.sh release-directory version revision"
  exit 2
fi

(cd "$release_dir" && shasum -a 256 -c checksums.txt)

for arch in arm64 amd64; do
  artifact="promptgrinder_${version}_darwin_${arch}"
  lipo_arch="$arch"
  if [[ "$arch" == "amd64" ]]; then
    lipo_arch="x86_64"
  fi
  archive="$release_dir/$artifact.tar.gz"
  stage=$(mktemp -d "${TMPDIR:-/tmp}/promptgrinder-verify.XXXXXX")
  tar -xzf "$archive" -C "$stage"
  test -x "$stage/$artifact/promptgrinder"
  test -f "$stage/$artifact/LICENSE"
  test -f "$stage/$artifact/README.md"
  file "$stage/$artifact/promptgrinder" | grep -q "Mach-O 64-bit executable"
  lipo -archs "$stage/$artifact/promptgrinder" | grep -qw "$lipo_arch"
  if [[ "$(uname -m)" == "$arch" ]]; then
    scripts/smoke-release.sh "$stage/$artifact/promptgrinder" "$version" "$revision"
  fi
  rm -rf -- "$stage"
done

print "release verification passed for $version at $revision"
