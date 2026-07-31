#!/bin/zsh
set -euo pipefail

repo_root=${0:A:h:h}
cd "$repo_root"

version=${1:-}
revision=${2:-$(git rev-parse HEAD)}
output_dir=${3:-dist}
source_date_epoch=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct "$revision")}

if [[ "$output_dir" = /* || "$output_dir" == "." || "$output_dir" == *".."* ]]; then
  print -u2 "output directory must be a relative child path without '..'"
  exit 2
fi
if [[ ! "$version" =~ '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' ]]; then
  print -u2 "usage: scripts/release.sh <version> [40-character-revision] [output-directory]"
  exit 2
fi
if [[ ! "$revision" =~ '^[0-9a-f]{40}$' ]]; then
  print -u2 "release revision must be a full 40-character lowercase Git SHA"
  exit 2
fi
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  print -u2 "release builds require a clean working tree"
  exit 1
fi
if [[ "$(git rev-parse HEAD)" != "$revision" ]]; then
  print -u2 "release revision must equal the checked-out commit"
  exit 1
fi
if [[ ! -f LICENSE ]]; then
  print -u2 "LICENSE is required in release archives; select and commit project license terms before release"
  exit 1
fi
if ! git describe --exact-match --match "$version" "$revision" >/dev/null 2>&1; then
  print -u2 "release commit must have the exact tag $version"
  exit 1
fi

go_version=$(go env GOVERSION)
if [[ "$go_version" != "go1.26.4" ]]; then
  print -u2 "release requires go1.26.4; found $go_version"
  exit 1
fi

umask 022
rm -rf -- "$output_dir"
mkdir -p "$output_dir"
build_date=$(TZ=UTC date -r "$source_date_epoch" '+%Y-%m-%dT%H:%M:%SZ')
ldflags="-s -w -X promptgrinder/internal/buildinfo.Version=$version -X promptgrinder/internal/buildinfo.Revision=$revision -X promptgrinder/internal/buildinfo.BuildDate=$build_date"

for arch in arm64 amd64; do
  artifact="promptgrinder_${version}_darwin_${arch}"
  stage=$(mktemp -d "${TMPDIR:-/tmp}/promptgrinder-release.XXXXXX")
  mkdir -p "$stage/$artifact"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build \
    -trimpath -buildvcs=false -ldflags "$ldflags" \
    -o "$stage/$artifact/promptgrinder" ./cmd/promptgrinder
  cp LICENSE README.md "$stage/$artifact/"
  chmod 0755 "$stage/$artifact/promptgrinder"
  TZ=UTC touch -t "$(TZ=UTC date -r "$source_date_epoch" '+%Y%m%d%H%M.%S')" \
    "$stage/$artifact" "$stage/$artifact/promptgrinder" "$stage/$artifact/LICENSE" "$stage/$artifact/README.md"
  COPYFILE_DISABLE=1 tar --format ustar --numeric-owner --owner 0 --group 0 \
    -C "$stage" -cf - "$artifact" | gzip -n > "$output_dir/$artifact.tar.gz"
  rm -rf -- "$stage"
done

(
  cd "$output_dir"
  shasum -a 256 promptgrinder_*.tar.gz > checksums.txt
)

cat > "$output_dir/build-metadata.json" <<EOF
{
  "schema_version": 1,
  "version": "$version",
  "revision": "$revision",
  "source_date_epoch": $source_date_epoch,
  "build_date": "$build_date",
  "go_version": "$go_version",
  "cgo_enabled": false,
  "targets": ["darwin/arm64", "darwin/amd64"],
  "build_flags": ["-trimpath", "-buildvcs=false"]
}
EOF

scripts/verify-release.sh "$output_dir" "$version" "$revision"
