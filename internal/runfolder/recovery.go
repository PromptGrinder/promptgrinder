package runfolder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"promptgrinder/internal/markdown"
	"promptgrinder/internal/state"
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

type validationRepairEvidence struct {
	Command string
	Reason  string
}

// validationRepairEligibility recognizes only a worker-declared PARTIAL/no
// result whose durable worker log records a failed command declared by the
// slice. It deliberately does not inspect arbitrary prose for test-like words.
func validationRepairEligibility(repo, home string, prompt Prompt, failed PromptState) (validationRepairEvidence, bool) {
	if failed.CompletionStatus != "PARTIAL" || failed.NextPromptSafe == nil || *failed.NextPromptSafe || failed.Worker.EngineResult == nil || failed.Worker.EngineResult.SessionID == "" {
		return validationRepairEvidence{}, false
	}
	changes, err := recoveryIsolationEligibility(repo, prompt, failed)
	if err != nil || len(changes) == 0 || runtimeClientDisconnected(failed.Worker.LogPath) {
		return validationRepairEvidence{}, false
	}
	if !noOtherActiveWorker(home, repo, failed.WorkerID) {
		return validationRepairEvidence{}, false
	}
	commands, err := declaredValidationCommands(prompt.Path)
	if err != nil || len(commands) == 0 {
		return validationRepairEvidence{}, false
	}
	log, err := os.ReadFile(failed.Worker.LogPath)
	if err != nil {
		return validationRepairEvidence{}, false
	}
	for _, command := range commands {
		if declaredCommandFailed(string(log), command) {
			return validationRepairEvidence{Command: command, Reason: "declared validation command failed with retained slice-only changes"}, true
		}
	}
	return validationRepairEvidence{}, false
}

func noOtherActiveWorker(home, repo, workerID string) bool {
	workers, err := state.NewStore(home).List()
	if err != nil {
		return false
	}
	for _, worker := range workers {
		if worker.ID != workerID && worker.RepositoryPath == repo && !state.IsTerminalStatus(worker.Status) {
			return false
		}
	}
	return true
}

func declaredValidationCommands(taskPath string) ([]string, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return nil, err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return nil, err
	}
	return metadataStrings(task.Metadata, "validation"), nil
}

func declaredCommandFailed(log, command string) bool {
	commandTokens := validationCommandTokens(command)
	if len(commandTokens) == 0 {
		return false
	}
	// Codex records shell commands inside a JSON event and commonly normalizes
	// `cd module && command` to the command run in that module. Match the
	// terminal command's ordered tokens rather than requiring byte-for-byte
	// YAML quoting. This is still constrained to a declared validation command.
	log = strings.ReplaceAll(log, `\\"`, `"`)
	logTokens := strings.Fields(strings.NewReplacer(`"`, "", `'`, "", `\\`, "").Replace(log))
	if !orderedTokensPresent(logTokens, commandTokens) {
		return false
	}
	// The command must appear before a concrete terminal command/build failure
	// in the same durable worker record. This is intentionally narrow enough to
	// avoid treating a worker's explanatory prose as validation evidence.
	after := strings.ToLower(log)
	return strings.Contains(after, "build failed") ||
		strings.Contains(after, "execution failed") ||
		strings.Contains(after, "exit_code\":1") ||
		strings.Contains(after, "exit status 1") ||
		strings.Contains(after, "tests failed")
}

func validationCommandTokens(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if parts := strings.Split(value, "&&"); len(parts) > 1 {
		value = parts[len(parts)-1]
	}
	value = strings.NewReplacer(`"`, "", `'`, "", `\\`, "").Replace(value)
	return strings.Fields(value)
}

func orderedTokensPresent(haystack, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	start := 0
	for _, needle := range needles {
		found := false
		for start < len(haystack) {
			if haystack[start] == needle {
				found = true
				start++
				break
			}
			start++
		}
		if !found {
			return false
		}
	}
	return true
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
	changes, err := recoveryIsolationEligibility(repo, prompt, failed)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
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

// recoveryIsolationEligibility is read-only. It lets resume preflight defer a
// clean-baseline rejection only when the retained diff can subsequently be
// isolated without guessing about ownership.
func recoveryIsolationEligibility(repo string, prompt Prompt, failed PromptState) ([]string, error) {
	if failed.GitBaseline == nil || failed.GitBaseline.Head == "" || (failed.WorkerID == "" && failed.Worker.ID == "") {
		return nil, fmt.Errorf("failed slice has no persisted pre-slice Git baseline; inspect and resolve the worktree before resuming")
	}
	changes, err := attributedWorkerChanges(repo, *failed.GitBaseline)
	if err != nil {
		return nil, fmt.Errorf("attribute retained changes: %w", err)
	}
	policy, err := taskPathPolicy(prompt.Path)
	if err != nil {
		return nil, err
	}
	violations, err := promptPolicyViolations(prompt, policy, changes)
	if err != nil {
		return nil, err
	}
	if len(violations) != 0 {
		return nil, fmt.Errorf("retained changes are not provably slice-owned: %s", formatViolations(violations))
	}
	current, err := workerpathpolicy.Capture(repo)
	if err != nil {
		return nil, fmt.Errorf("capture recovery state: %w", err)
	}
	if current.Head != failed.GitBaseline.Head {
		return nil, fmt.Errorf("failed slice changed Git HEAD; automatic isolation is unsafe")
	}
	return changes, nil
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

// resumedRecoveryCandidate finds the first failed slice of the sequence that
// this invocation would resume. It is read-only and intentionally refuses to
// defer clean-Git enforcement unless the persisted worker evidence and exact
// pre-slice baseline prove that recovery isolation is safe.
func resumedRecoveryCandidate(folder, repo string, prompts []Prompt, options Options) (SequenceState, Prompt, PromptState, bool, error) {
	if options.Fresh || options.Restart || options.NoResume {
		return SequenceState{}, Prompt{}, PromptState{}, false, nil
	}
	store := newSequenceStore(options.HomeDir)
	var sequence SequenceState
	var err error
	if options.ResumeSequence != "" {
		sequence, _, err = store.validateExplicitAdoption(folder, repo, prompts, options.ResumeSequence)
		if err != nil {
			return SequenceState{}, Prompt{}, PromptState{}, false, err
		}
	} else {
		base, buildErr := buildSequence(folder, repo, prompts, options)
		if buildErr != nil {
			return SequenceState{}, Prompt{}, PromptState{}, false, buildErr
		}
		sequence, err = store.load(base.SequenceID)
		if os.IsNotExist(err) {
			sequence, _, err = store.findCompatibleResume(folder, repo, prompts)
		}
		if err != nil {
			return SequenceState{}, Prompt{}, PromptState{}, false, nil
		}
	}
	for index, item := range sequence.Items {
		if item.Status != "failed" {
			continue
		}
		if index >= len(prompts) || prompts[index].Name != item.PromptName {
			return SequenceState{}, Prompt{}, PromptState{}, false, fmt.Errorf("failed sequence item %q no longer matches the prompt folder", item.PromptName)
		}
		failed, err := store.loadPromptState(sequence.SequenceID, item.PromptName)
		if err != nil {
			return SequenceState{}, Prompt{}, PromptState{}, false, fmt.Errorf("load failed slice recovery evidence: %w", err)
		}
		failed.Worker = failedWorkerEvidence(options.HomeDir, item, failed)
		if failed.WorkerID == "" {
			failed.WorkerID = item.WorkerID
		}
		if _, err := recoveryIsolationEligibility(repo, prompts[index], failed); err != nil {
			return sequence, prompts[index], failed, false, err
		}
		if recoverableFailure(failed, errors.New(item.Error)) {
			return sequence, prompts[index], failed, true, nil
		}
		if _, eligible := validationRepairEligibility(repo, options.HomeDir, prompts[index], failed); eligible && failed.RecoveryAttempts < options.RecoveryAttempts {
			return sequence, prompts[index], failed, true, nil
		}
		return SequenceState{}, Prompt{}, PromptState{}, false, nil
	}
	return SequenceState{}, Prompt{}, PromptState{}, false, nil
}

func failedWorkerEvidence(home string, item SequenceItem, failed PromptState) state.Worker {
	workerID := failed.WorkerID
	if workerID == "" {
		workerID = item.WorkerID
	}
	if workerID != "" {
		if worker, err := state.NewStore(home).Load(workerID); err == nil {
			return worker
		}
	}
	return state.Worker{
		ID: workerID, Status: state.StatusFailed, LogPath: item.LogPath,
		EngineResult: &state.EngineResult{SessionID: failed.EngineSessionID, CompletionStatus: failed.CompletionStatus, NextPromptSafe: failed.NextPromptSafe, CompletionReason: failed.CompletionReason},
	}
}

// prepareResumedRecovery performs the actual isolation after preflight has
// selected the existing sequence but before a retry can reach runPrompt's
// ordinary clean-baseline guard.
func prepareResumedRecovery(repo, home string, sequence *SequenceState, prompts []Prompt, options Options, store folderStore) error {
	_, prompt, failed, ok, err := resumedRecoveryCandidate(sequence.Folder, repo, prompts, options)
	if err != nil || !ok || prompt.Name == "" || sequence.SequenceID == "" {
		return err
	}
	if sequence.SequenceID == "" {
		return nil
	}
	if evidence, eligible := validationRepairEligibility(repo, home, prompt, failed); eligible && failed.RecoveryAttempts < options.RecoveryAttempts {
		failed.Status = "repairing"
		failed.RecoveryAttempts++
		failed.RecoveryMode = "validation-repair"
		failed.Error = evidence.Reason
		if err := store.savePrompt(failed); err != nil {
			return err
		}
		sequence.setRecoveryAttempts(prompt.Name, failed.RecoveryAttempts)
		sequence.setRecoveryMode(prompt.Name, failed.RecoveryMode)
		return nil
	}
	artifact, err := isolateRecoveryChanges(repo, home, sequence.SequenceID, prompt, failed)
	if err != nil {
		if artifact != "" {
			failed.RecoveryArtifact = artifact
			_ = store.savePrompt(failed)
			sequence.setRecoveryArtifact(prompt.Name, artifact)
		}
		return fmt.Errorf("resume recovery blocked; inspect retained changes and resolve them before retrying%s: %w", recoveryArtifactSuffix(artifact), err)
	}
	if artifact == "" {
		return nil
	}
	failed.RecoveryArtifact = artifact
	failed.Error = "retained slice changes isolated before resume; retrying only this slice"
	if err := store.savePrompt(failed); err != nil {
		return err
	}
	sequence.setRecoveryArtifact(prompt.Name, artifact)
	for i := range sequence.Items {
		if sequence.Items[i].PromptName == prompt.Name {
			sequence.Items[i].Error = failed.Error
			break
		}
	}
	sequence.touch()
	return nil
}

func recoveryArtifactSuffix(artifact string) string {
	if artifact == "" {
		return ""
	}
	return "; recovery artifact: " + artifact
}
