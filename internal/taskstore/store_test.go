package taskstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerstate"
)

func definition(project, worker string) workerdomain.WorkerDefinition {
	return workerdomain.WorkerDefinition{
		ID: worker, ProjectID: project, DisplayName: "Worker", Role: "Do work.",
		Runtime: workerdomain.RuntimeRef{Name: "codex"},
		Policy: workerdomain.WorkerPolicy{
			BranchPrefix: "worker/" + worker, DefaultWorktree: ".",
			AllowedPaths: []string{"**"}, ForbiddenPaths: []string{"secrets/**"},
		},
	}
}

func TestAssignmentSnapshotsMarkdownAndUpdatesWorkerState(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "tasks", "sonar-001.md")
	original := "---\npriority: high\n---\n# Fix sonar\n\nKeep the audit trail.\n"
	if err := os.WriteFile(source, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(home)
	task, err := store.Assign(root, definition("example", "backend"), "tasks/sonar-001.md")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "sonar-001" || task.Instructions != "# Fix sonar\n\nKeep the audit trail.\n" ||
		task.ContentSnapshot != original || task.AttemptCount != 0 ||
		task.Status != workerdomain.TaskStatusAssigned {
		t.Fatalf("assigned task = %#v", task)
	}
	if err := os.WriteFile(source, []byte("changed later"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("example", "sonar-001")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentSnapshot != original || loaded.Instructions != task.Instructions {
		t.Fatalf("snapshot changed with source: %#v", loaded)
	}
	state, err := workerstate.New(home).Load("example", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveTaskID != task.ID {
		t.Fatalf("active task = %q, want %q", state.ActiveTaskID, task.ID)
	}
	info, err := os.Stat(store.Path("example", "sonar-001"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("task permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestAdditionalAssignmentQueuesAndMissingSourceLeavesAssignmentUnchanged(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(root, "one.md"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.md"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(home)
	def := definition("example", "backend")
	first, err := store.Assign(root, def, "one.md")
	if err != nil {
		t.Fatal(err)
	}
	before, err := workerstate.New(home).Load("example", "backend")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Assign(root, def, "two.md")
	if err != nil {
		t.Fatalf("additional assignment: %v", err)
	}
	if second.Status != workerdomain.TaskStatusPending {
		t.Fatalf("additional task status = %q, want pending", second.Status)
	}
	if _, err := store.Assign(root, definition("example", "frontend"), "missing.md"); err == nil {
		t.Fatal("missing source assignment succeeded")
	}
	after, err := workerstate.New(home).Load("example", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.ActiveTaskID != first.ID {
		t.Fatalf("active assignment changed: before=%#v after=%#v", before, after)
	}
	if _, err := os.Stat(store.Path("example", "two")); err != nil {
		t.Fatalf("queued task snapshot missing: %v", err)
	}
}

func TestProjectWorkerMismatchAndListFilter(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(home)
	if _, err := store.Assign(root, definition("example", "backend"), "task.md"); err != nil {
		t.Fatal(err)
	}
	all, err := store.List("example", "")
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %#v, %v", all, err)
	}
	filtered, err := store.List("example", "frontend")
	if err != nil || len(filtered) != 0 {
		t.Fatalf("filtered list = %#v, %v", filtered, err)
	}

	path := store.Path("example", "task")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var task workerdomain.Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	task.ProjectID = "other"
	data, _ = json.Marshal(task)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("example", "task"); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("project mismatch error = %v", err)
	}
}

func TestTaskCreationFailureLeavesExistingWorkerStateUnchanged(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	def := definition("example", "backend")
	stateStore := workerstate.New(home)
	before, err := stateStore.Ensure(def)
	if err != nil {
		t.Fatal(err)
	}
	store := New(home)
	taskDir := filepath.Dir(store.Path("example", "task"))
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path("example", "task"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(root, def, "task.md"); !errors.Is(err, ErrTaskExists) {
		t.Fatalf("assignment error = %v", err)
	}
	after, err := stateStore.Load("example", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.ActiveTaskID != "" {
		t.Fatalf("worker state changed after task write failure: %#v", after)
	}
}
