#!/bin/zsh
set -euo pipefail

binary=${1:A}
version=${2:-}
revision=${3:-}
stage=$(mktemp -d "${TMPDIR:-/tmp}/promptgrinder-smoke.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT

mkdir -p "$stage/bin"
cp "$binary" "$stage/bin/promptgrinder"
chmod 0755 "$stage/bin/promptgrinder"

cat > "$stage/bin/codex" <<'EOF'
#!/bin/zsh
if [[ "$1" == "--version" ]]; then
  print "codex-cli offline-smoke"
  exit 0
fi
if [[ "$1" == "login" && "$2" == "status" ]]; then
  print "Logged in (offline smoke fixture)"
  exit 0
fi
exit 2
EOF
chmod 0755 "$stage/bin/codex"

version_output=$("$stage/bin/promptgrinder" --version)
[[ "$version_output" == "promptgrinder $version (${revision[1,7]})" ]]
"$stage/bin/promptgrinder" help >/dev/null

PROMPTGRINDER_HOME="$stage/home" \
PROMPTGRINDER_TERMINAL_ADAPTER=headless \
PROMPTGRINDER_ENGINE_CODEX_EXECUTABLE="$stage/bin/codex" \
PATH="$stage/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
  "$stage/bin/promptgrinder" doctor --terminal headless --json > "$stage/doctor.json"

grep -q '"ok": true' "$stage/doctor.json"
print "offline smoke passed: $(uname -m) $version (${revision[1,7]})"
