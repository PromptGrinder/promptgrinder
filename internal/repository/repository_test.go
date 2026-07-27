package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectRootFindsGitRoot(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	task := filepath.Join(nested, "task.md")
	if err := os.WriteFile(task, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectRoot(task)
	if err != nil {
		t.Fatal(err)
	}
	if got != repo {
		t.Fatalf("root = %q, want %q", got, repo)
	}
}

func TestDetectRootFallsBackToTaskParent(t *testing.T) {
	dir := t.TempDir()
	task := filepath.Join(dir, "task.md")
	if err := os.WriteFile(task, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectRoot(task)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("root = %q, want %q", got, dir)
	}
}
