#!/bin/zsh
set -euo pipefail

repo_root=${0:A:h:h}
workflow="$repo_root/.github/workflows/release.yml"

[[ -f "$workflow" ]]

for forbidden in \
  'actions/upload-artifact' \
  'actions/download-artifact' \
  'gh release upload' \
  'scripts/release.sh' \
  'scripts/verify-release.sh' \
  'scripts/smoke-release.sh' \
  'checksums.txt' \
  '.tar.gz' \
  '.zip' \
  '.tgz' \
  '.dmg' \
  '.exe' \
  'dist/*'; do
  if grep -Fq -- "$forbidden" "$workflow"; then
    print -u2 "release workflow contains binary-asset configuration: $forbidden"
    exit 1
  fi
done

grep -Fq 'gh release create "${GITHUB_REF_NAME}" \' "$workflow"
grep -Fq -- '--draft' "$workflow"
grep -Fq -- '--verify-tag' "$workflow"
grep -Fq -- '--generate-notes' "$workflow"
grep -Fq -- '--title "PromptGrinder ${GITHUB_REF_NAME}"' "$workflow"

if find "$repo_root" -maxdepth 1 -type f -name '.goreleaser*' | grep -q .; then
  print -u2 'GoReleaser configuration would reintroduce an unreviewed artifact publisher'
  exit 1
fi

print 'source-only release configuration verified'
