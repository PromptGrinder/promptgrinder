package workerworktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"promptgrinder/internal/workerdomain"
)

func TestBranchNameSanitizesDeterministically(t *testing.T) {
	if got, want := BranchName("worker/backend", " Sonar_001 @ Fix "), "worker/backend/sonar_001-fix"; got != want {
		t.Fatalf("BranchName = %q, want %q", got, want)
	}
}

func TestPrepareCreatesDedicatedWorktreeAndRecordsBase(t *testing.T) {
	repo := gitRepository(t)
	task := assignedTask("sonar-001")
	policy := workerdomain.WorkerPolicy{BranchPrefix: "worker/backend", DefaultWorktree: "worktrees/backend", RequireClean: true}
	selection, err := Prepare(repo, policy, task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Branch != "worker/backend/sonar-001" || selection.BaseBranch != "main" || len(selection.BaseRevision) != 40 {
		t.Fatalf("selection = %#v", selection)
	}
	if got := runGit(t, selection.Worktree, "branch", "--show-current"); got != selection.Branch {
		t.Fatalf("worktree branch = %q", got)
	}
}

func TestPrepareRejectsBranchCollisionWithoutChangingCurrentBranch(t *testing.T) {
	repo := gitRepository(t)
	runGit(t, repo, "branch", "worker/backend/sonar-001")
	_, err := Prepare(repo, workerdomain.WorkerPolicy{BranchPrefix: "worker/backend", DefaultWorktree: "."}, assignedTask("sonar-001"))
	if err == nil || !strings.Contains(err.Error(), "branch collision") {
		t.Fatalf("error = %v", err)
	}
	if got := runGit(t, repo, "branch", "--show-current"); got != "main" {
		t.Fatalf("current branch changed to %q", got)
	}
}

func TestPrepareHonorsDirtyPolicy(t *testing.T) {
	repo := gitRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(repo, workerdomain.WorkerPolicy{
		BranchPrefix: "worker/backend", DefaultWorktree: ".", RequireClean: true,
	}, assignedTask("sonar-001"))
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "dirty.txt")); statErr != nil {
		t.Fatalf("dirty user file was changed: %v", statErr)
	}
}

func TestPrepareRecoversExactInterruptedSetup(t *testing.T) {
	repo := gitRepository(t)
	policy := workerdomain.WorkerPolicy{BranchPrefix: "worker/backend", DefaultWorktree: "."}
	task := assignedTask("sonar-001")
	selection, err := Plan(repo, policy, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Worktree, task.Branch = selection.Worktree, selection.Branch
	task.BaseBranch, task.BaseRevision, task.LaunchSetup = selection.BaseBranch, selection.BaseRevision, "preparing"
	runGit(t, repo, "switch", "-c", selection.Branch, selection.BaseRevision)
	recovered, err := Prepare(repo, policy, task)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Branch != selection.Branch || recovered.BaseRevision != selection.BaseRevision {
		t.Fatalf("recovered = %#v, want %#v", recovered, selection)
	}
}

func assignedTask(id string) workerdomain.Task {
	return workerdomain.Task{ID: id}
}

func gitRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "PromptGrinder Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
