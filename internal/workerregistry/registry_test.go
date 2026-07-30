package workerregistry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validYAML = `version: 1
project:
  id: footybadger
  name: FootyBadger
workers:
  frontend:
    display_name: Frontend Engineer
    role: Build the UI.
    runtime: claude
    branch:
      prefix: worker/frontend
    worktree:
      default: .
    paths:
      allowed: [frontend/**]
      forbidden: [frontend/secrets/**]
  backend-sonar:
    display_name: Backend Sonar Engineer
    role: Resolve findings.
    runtime: codex
    branch:
      prefix: worker/backend-sonar
    worktree:
      default: worktrees/backend
    paths:
      allowed: [backend/**]
      forbidden: [infrastructure/production/**]
`

func writeRepository(t *testing.T, contents string) (string, string) {
	t.Helper()
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		if err := os.WriteFile(filepath.Join(root, RegistryPath), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, nested
}

func TestLoadDiscoversRootFromRootAndNestedDirectory(t *testing.T) {
	root, nested := writeRepository(t, validYAML)
	for _, start := range []string{root, nested} {
		registry, err := Load(start)
		if err != nil {
			t.Fatal(err)
		}
		if registry.Root != root {
			t.Fatalf("root = %q, want %q", registry.Root, root)
		}
		if got := []string{registry.List()[0].ID, registry.List()[1].ID}; !reflect.DeepEqual(got, []string{"backend-sonar", "frontend"}) {
			t.Fatalf("worker IDs = %v", got)
		}
	}
}

func TestLoadMissingMalformedAndInvalidRegistry(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "missing", want: "is missing"},
		{name: "malformed", contents: "version: [", want: "parse worker registry"},
		{name: "unknown field", contents: validYAML + "surprise: true\n", want: "field surprise not found"},
		{name: "unknown version", contents: strings.Replace(validYAML, "version: 1", "version: 2", 1), want: "unsupported"},
		{name: "invalid worker ID", contents: strings.Replace(validYAML, "backend-sonar:", "Backend_Sonar:", 1), want: "worker id"},
		{name: "absolute path", contents: strings.Replace(validYAML, "backend/**", "/backend/**", 1), want: "repository-relative"},
		{name: "escape", contents: strings.Replace(validYAML, "backend/**", "../backend/**", 1), want: "escapes"},
		{name: "bad glob", contents: strings.Replace(validYAML, "allowed: [backend/**]", `allowed: ["backend/["]`, 1), want: "invalid glob"},
		{name: "bad branch", contents: strings.Replace(validYAML, "worker/backend-sonar", "worker//backend", 1), want: "branch prefix"},
		{name: "bad runtime", contents: strings.Replace(validYAML, "runtime: codex", "runtime: Codex CLI", 1), want: "runtime name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, nested := writeRepository(t, test.contents)
			_, err := Load(nested)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestGetUnknownWorker(t *testing.T) {
	root, _ := writeRepository(t, validYAML)
	registry, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Get("missing")
	if !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("error = %v", err)
	}
}
