package runfolder

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"promptgrinder/internal/workerpathpolicy"
)

// runtimeClientDisconnected is deliberately narrow. A generic "cancelled"
// message is common in ordinary Gradle/test failures, whereas this marker is
// emitted by the runtime when its client transport disappears mid-command.
func runtimeClientDisconnected(logPath string) bool {
	if strings.TrimSpace(logPath) == "" {
		return false
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "client disconnection detected")
}

type recoveryManifest struct {
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	SequenceID  string    `json:"sequence_id"`
	Prompt      string    `json:"prompt"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Paths       []string  `json:"paths"`
	RestoreHint string    `json:"restore_hint"`
}

// isolateRecoveryChanges preserves only changes attributed to the failed
// slice before a clean-baseline retry. It is intentionally fail-closed: any
// changed HEAD, policy violation, or unproven path leaves the worktree alone.
// The artifact is inspectable and includes both a binary Git patch and moved
// untracked files. No reset, git clean, stash, or automatic commit is used.
func isolateRecoveryChanges(repo, home, sequenceID string, prompt Prompt, failed PromptState) (string, error) {
	if !(failed.baseline.Head != "" && (failed.WorkerID != "" || failed.Worker.ID != "")) {
		return "", nil
	}
	changes, err := attributedWorkerChanges(repo, failed.baseline)
	if err != nil {
		return "", fmt.Errorf("attribute retained changes: %w", err)
	}
	if len(changes) == 0 {
		return "", nil
	}
	policy, err := taskPathPolicy(prompt.Path)
	if err != nil {
		return "", err
	}
	violations, err := promptPolicyViolations(prompt, policy, changes)
	if err != nil {
		return "", err
	}
	if len(violations) != 0 {
		return "", fmt.Errorf("retained changes are not provably slice-owned: %s", formatViolations(violations))
	}
	current, err := workerpathpolicy.Capture(repo)
	if err != nil {
		return "", fmt.Errorf("capture recovery state: %w", err)
	}
	if current.Head != failed.baseline.Head {
		return "", fmt.Errorf("failed slice changed Git HEAD; automatic isolation is unsafe")
	}

	workerID := failed.WorkerID
	if workerID == "" {
		workerID = failed.Worker.ID
	}
	artifact := filepath.Join(home, "recovery-artifacts", safeName(sequenceID), safeName(prompt.Name), safeName(workerID))
	if err := os.MkdirAll(filepath.Join(artifact, "files"), 0o700); err != nil {
		return "", err
	}
	patch, err := exec.Command("git", "-C", repo, "diff", "--binary", "HEAD", "--").Output()
	if err != nil {
		return artifact, fmt.Errorf("capture recovery patch: %w", err)
	}
	if err := os.WriteFile(filepath.Join(artifact, "recovery.patch"), patch, 0o600); err != nil {
		return artifact, err
	}

	tracked, untracked, err := splitTrackedPaths(repo, changes)
	if err != nil {
		return artifact, err
	}
	for _, name := range untracked {
		if err := moveUntrackedToArtifact(repo, artifact, name); err != nil {
			return artifact, err
		}
	}
	if len(tracked) != 0 {
		args := append([]string{"-C", repo, "restore", "--source=HEAD", "--staged", "--worktree", "--"}, literalPathspecs(tracked)...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return artifact, fmt.Errorf("restore isolated tracked changes: %s", strings.TrimSpace(string(out)))
		}
	}
	clean, err := gitClean(repo)
	if err != nil {
		return artifact, err
	}
	if !clean {
		return artifact, fmt.Errorf("isolation did not produce a clean retry baseline; retained artifact: %s", artifact)
	}
	manifest := recoveryManifest{
		Version: 1, CreatedAt: time.Now().UTC(), SequenceID: sequenceID, Prompt: prompt.Name,
		WorkerID: workerID, Paths: changes,
		RestoreHint: "Inspect recovery.patch and files/. Restore only reviewed content; for tracked changes use git apply recovery.patch.",
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return artifact, err
	}
	if err := os.WriteFile(filepath.Join(artifact, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return artifact, err
	}
	return artifact, nil
}

func splitTrackedPaths(repo string, paths []string) (tracked, untracked []string, err error) {
	for _, name := range paths {
		if !safeRecoveryPath(name) {
			return nil, nil, fmt.Errorf("unsafe recovery path %q", name)
		}
		if command := exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", "--", name); command.Run() == nil {
			tracked = append(tracked, name)
		} else {
			untracked = append(untracked, name)
		}
	}
	return tracked, untracked, nil
}

func moveUntrackedToArtifact(repo, artifact, name string) error {
	source := filepath.Join(repo, filepath.FromSlash(name))
	if _, err := os.Lstat(source); err != nil {
		if os.IsNotExist(err) { // an already-deleted untracked path needs no move.
			return nil
		}
		return err
	}
	destination := filepath.Join(artifact, "files", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.Rename(source, destination)
}

func safeRecoveryPath(name string) bool {
	clean := filepath.ToSlash(filepath.Clean(name))
	return name != "" && clean == name && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(name)
}

func literalPathspecs(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, name := range paths {
		result = append(result, ":(literal)"+name)
	}
	return result
}
