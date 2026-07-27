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
