// Package workerworktree selects and prepares the Git location for a named
// worker launch without ever removing or overwriting user work.
package workerworktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"promptgrinder/internal/workerdomain"
)

type Selection struct {
	Worktree     string
	Branch       string
	BaseBranch   string
	BaseRevision string
}

var unsafeBranchRun = regexp.MustCompile(`[^a-z0-9._-]+`)

func BranchName(prefix, taskID string) string {
	part := strings.ToLower(strings.TrimSpace(taskID))
	part = unsafeBranchRun.ReplaceAllString(part, "-")
	part = strings.Trim(part, ".-")
	for strings.Contains(part, "..") {
		part = strings.ReplaceAll(part, "..", "-")
	}
	if part == "" {
		part = "task"
	}
	return strings.TrimSuffix(prefix, "/") + "/" + part
}

func Plan(repository string, policy workerdomain.WorkerPolicy, taskID string) (Selection, error) {
	repository, err := filepath.Abs(repository)
	if err != nil {
		return Selection{}, err
	}
	worktree := filepath.Clean(filepath.Join(repository, filepath.FromSlash(policy.DefaultWorktree)))
	relative, err := filepath.Rel(repository, worktree)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Selection{}, fmt.Errorf("configured worktree %q escapes repository", policy.DefaultWorktree)
	}
	baseBranch, err := git(repository, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Selection{}, fmt.Errorf("resolve base branch: %w", err)
	}
	baseRevision, err := git(repository, "rev-parse", "HEAD")
	if err != nil {
		return Selection{}, fmt.Errorf("resolve base revision: %w", err)
	}
	return Selection{
		Worktree: worktree, Branch: BranchName(policy.BranchPrefix, taskID),
		BaseBranch: baseBranch, BaseRevision: baseRevision,
	}, nil
}

func ValidateAvailable(repository string, selection Selection) error {
	if _, err := git(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+selection.Branch); err == nil {
		return fmt.Errorf("branch collision: %q already exists; refusing to reuse or overwrite it", selection.Branch)
	}
	return nil
}

// Prepare selects a worktree and creates the task branch. A previously
// persisted "preparing" selection permits idempotent recovery when an
// interrupted setup already performed the exact Git mutation.
func Prepare(repository string, policy workerdomain.WorkerPolicy, task workerdomain.Task) (Selection, error) {
	selection, err := Plan(repository, policy, task.ID)
	if err != nil {
		return Selection{}, err
	}
	worktree, branch, baseRevision := selection.Worktree, selection.Branch, selection.BaseRevision
	recovery := (task.LaunchSetup == "preparing" || task.LaunchSetup == "prepared") && task.Worktree == worktree &&
		task.Branch == branch && task.BaseBranch != "" && task.BaseRevision != ""

	if _, err := git(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if !recovery {
			return Selection{}, fmt.Errorf("branch collision: %q already exists; refusing to reuse or overwrite it", branch)
		}
		selection.BaseBranch, selection.BaseRevision = task.BaseBranch, task.BaseRevision
		current, currentErr := git(worktree, "branch", "--show-current")
		if currentErr == nil && current == branch {
			return selection, validateClean(worktree, policy.RequireClean)
		}
		return Selection{}, fmt.Errorf("interrupted launch branch %q exists but is not checked out in selected worktree %s", branch, worktree)
	}

	if info, statErr := os.Stat(worktree); statErr == nil {
		if !info.IsDir() {
			return Selection{}, fmt.Errorf("configured worktree %s is not a directory", worktree)
		}
		if _, err := git(worktree, "rev-parse", "--show-toplevel"); err != nil {
			return Selection{}, fmt.Errorf("configured worktree %s is not a Git worktree", worktree)
		}
		if err := validateClean(worktree, policy.RequireClean); err != nil {
			return Selection{}, err
		}
		if _, err := git(worktree, "switch", "-c", branch, baseRevision); err != nil {
			return Selection{}, fmt.Errorf("create branch %q in worktree %s: %w", branch, worktree, err)
		}
		return selection, nil
	} else if !os.IsNotExist(statErr) {
		return Selection{}, statErr
	}

	parent := filepath.Dir(worktree)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Selection{}, err
	}
	if _, err := git(repository, "worktree", "add", "-b", branch, worktree, baseRevision); err != nil {
		return Selection{}, fmt.Errorf("create dedicated worktree %s on branch %q: %w", worktree, branch, err)
	}
	return selection, nil
}

func validateClean(worktree string, required bool) error {
	if !required {
		return nil
	}
	status, err := git(worktree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("worktree %s is dirty and worker policy requires a clean worktree", worktree)
	}
	return nil
}

func git(directory string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s", message)
	}
	return strings.TrimSpace(stdout.String()), nil
}
