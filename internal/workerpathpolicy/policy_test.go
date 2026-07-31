package workerpathpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"promptgrinder/internal/workerdomain"
)

func TestAttributedChangesEditsAddsDeletesRenamesAndPreexisting(t *testing.T) {
	repo := disposableRepo(t)
	write(t, repo, "backend/edit.go", "old\n")
	write(t, repo, "backend/delete.go", "delete\n")
	write(t, repo, "backend/rename.go", "rename\n")
	write(t, repo, "user/preexisting.txt", "user edit\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixtures")
	write(t, repo, "user/preexisting.txt", "pre-existing dirty\n")

	before, err := Capture(repo)
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo, "backend/edit.go", "new\n")
	write(t, repo, "backend/add.go", "added\n")
	if err := os.Remove(filepath.Join(repo, "backend/delete.go")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "mv", "backend/rename.go", "backend/renamed.go")

	got, err := AttributedChanges(repo, before)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"backend/add.go", "backend/delete.go", "backend/edit.go", "backend/rename.go", "backend/renamed.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
	if string(read(t, repo, "user/preexisting.txt")) != "pre-existing dirty\n" {
		t.Fatal("pre-existing change was modified")
	}
}

func TestAttributedChangesDetectsFurtherEditToPreexistingPath(t *testing.T) {
	repo := disposableRepo(t)
	write(t, repo, "backend/edit.go", "base\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	write(t, repo, "backend/edit.go", "user\n")
	before, err := Capture(repo)
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo, "backend/edit.go", "runtime\n")
	got, err := AttributedChanges(repo, before)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"backend/edit.go"}) {
		t.Fatalf("changes = %#v", got)
	}
}

func TestViolationsForbiddenOverridesAllowedAndSupportsGlobstar(t *testing.T) {
	policy := workerdomain.WorkerPolicy{
		BranchPrefix: "worker/test", DefaultWorktree: ".",
		AllowedPaths:   []string{"backend/**"},
		ForbiddenPaths: []string{"backend/secrets/**"},
	}
	got, err := Violations(policy, []string{"backend/main.go", "backend/secrets/key.txt", "docs/readme.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Violation{
		{Path: "backend/secrets/key.txt", Rule: "backend/secrets/**", Reason: "forbidden path"},
		{Path: "docs/readme.md", Reason: "outside allowed paths"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %#v, want %#v", got, want)
	}
}

func disposableRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "test@example.invalid")
	git(t, repo, "config", "user.name", "Test")
	write(t, repo, "README.md", "fixture\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func write(t *testing.T, repo, name, value string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, repo, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
