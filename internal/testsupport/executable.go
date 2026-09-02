package testsupport

import (
	"os"
	"path/filepath"
	"testing"
)

// FakeCodex creates a per-test executable that satisfies Codex discovery but
// fails loudly if a planning or dry-run test unexpectedly executes it.
func FakeCodex(t testing.TB) string {
	t.Helper()
	return FakeExecutable(t, "codex", `#!/bin/sh
if [ "$1" = "app-server" ]; then
  IFS= read -r initialize
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"codexHome":"/tmp","platformFamily":"unix","platformOs":"macos","userAgent":"fake"}}'
  IFS= read -r models
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"gpt-5","model":"gpt-5","inputModalities":["text","image"]},{"id":"gpt-5.5","model":"gpt-5.5","inputModalities":["text","image"]},{"id":"gpt-5.6-sol","model":"gpt-5.6-sol","inputModalities":["text","image"]}]}}'
  exit 0
fi
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.150.1"
  exit 0
fi
echo "error: fake Codex must not be executed by this test" >&2
exit 99
`)
}

// FakeExecutable creates a deterministic executable owned by the current test.
func FakeExecutable(t testing.TB, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("create fake executable %s: %v", name, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve fake executable %s: %v", name, err)
	}
	return absolute
}
