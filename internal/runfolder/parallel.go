package runfolder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"promptgrinder/internal/state"
	"promptgrinder/internal/workeridentity"
)

// validateParallelWorktreePlan keeps the opt-in mode deliberately narrow. A
// lane must be independently attributable, fresh-context, and checkpointable
// before PromptGrinder is allowed to run it beside another lane.
func validateParallelWorktreePlan(prompts []Prompt, options Options) error {
	if !options.Checkpoint || !options.CommitEach || !options.RequireCleanGit {
		return fmt.Errorf("requires --checkpoint, --commit-each, and --require-clean-git")
	}
	for _, prompt := range prompts {
		if prompt.Type == TypeSpecification {
			continue
		}
		if prompt.Lane == "" {
			return fmt.Errorf("%s must declare lane", prompt.Name)
		}
		if prompt.Priority < 1 {
			return fmt.Errorf("%s must declare positive priority", prompt.Name)
		}
		if prompt.ContextMode != ContextFresh {
			return fmt.Errorf("%s must declare context_mode: fresh; parallel lanes cannot share a Codex session", prompt.Name)
		}
		if prompt.GateOutcome != "" {
			return fmt.Errorf("%s declares gate_outcome; capability gates must run in the sequential coordinator flow", prompt.Name)
		}
		policy, err := taskPathPolicy(prompt.Path)
		if err != nil {
			return err
		}
		if len(policy.AllowedPaths) == 0 {
			return fmt.Errorf("%s must declare non-empty allowed_paths", prompt.Name)
		}
	}
	return nil
}

type laneLauncher struct {
	repository string
	launcher   RepositoryLauncher
	waiter     WaitLauncher
}

func (l laneLauncher) LaunchPrompt(path, content, sessionID string) (state.Worker, error) {
	return l.launcher.LaunchPromptInRepository(l.repository, path, content, sessionID)
}

func (l laneLauncher) WaitPrompt(worker state.Worker) (state.Worker, error) {
	if l.waiter == nil {
		return worker, fmt.Errorf("parallel worktree launcher returned unfinished worker %s without a waiter", worker.ID)
	}
	return l.waiter.WaitPrompt(worker)
}

type parallelResult struct {
	prompt      Prompt
	promptState PromptState
	worktree    string
	branch      string
	err         error
}

// runParallelWorktrees executes currently dependency-eligible lanes together.
// Successful lanes are integrated in priority/name order in an isolated
// coordinator worktree. The caller's feature branch is only fast-forwarded
// after every lane has been integrated without conflict.
func runParallelWorktrees(repoRoot, specContext string, prompts []Prompt, options Options, launcher Launcher, sequence *SequenceState, sequenceStore sequenceStore, store folderStore, runState *RunState, summary *Summary) error {
	repositoryLauncher, ok := launcher.(RepositoryLauncher)
	if !ok {
		return fmt.Errorf("parallel worktree execution requires a repository-aware launcher")
	}
	baseSHA, err := gitSHA(repoRoot)
	if err != nil {
		return fmt.Errorf("read parallel worktree baseline: %w", err)
	}
	targetBranch, err := gitCurrentBranch(repoRoot)
	if err != nil {
		return fmt.Errorf("parallel worktree execution requires a checked-out feature branch: %w", err)
	}
	sequence.FeatureBranch = targetBranch
	runKey := safeName(runState.RunID)
	root := filepath.Join(options.HomeDir, "state", "run-folders", sequence.SequenceID, "parallel-worktrees", runKey)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	coordinatorBranch := "promptgrinder/integration/" + safeName(sequence.SequenceID) + "/" + runKey
	coordinatorWorktree := filepath.Join(root, "integration")
	if err := gitWorktreeAdd(repoRoot, coordinatorWorktree, coordinatorBranch, baseSHA); err != nil {
		return fmt.Errorf("create isolated integration worktree: %w", err)
	}

	completed := make(map[string]bool, len(prompts))
	for _, prompt := range prompts {
		if prompt.Type != TypeSpecification {
			continue
		}
		now := time.Now().UTC()
		promptState := PromptState{Prompt: prompt.Name, PromptType: prompt.Type, Status: "completed", StartedAt: &now, FinishedAt: &now}
		if err := store.savePrompt(promptState); err != nil {
			return err
		}
		sequence.mark(prompt.Name, "skipped", state.Worker{}, nil, "")
		completed[prompt.ID] = true
		runState.Completed = append(runState.Completed, prompt.Name)
		emitProgress(options, ProgressEvent{Type: "prompt.skipped", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "skipped", Lane: prompt.Lane, Priority: prompt.Priority, Completed: len(runState.Completed), Total: len(prompts)})
	}
	if err := sequenceStore.save(*sequence); err != nil {
		return err
	}

	remaining := make(map[string]Prompt)
	for _, prompt := range prompts {
		if prompt.Type != TypeSpecification {
			remaining[prompt.ID] = prompt
		}
	}
	for len(remaining) > 0 {
		ready := make([]Prompt, 0)
		for _, prompt := range remaining {
			if dependenciesComplete(prompt, completed) {
				ready = append(ready, prompt)
			}
		}
		if len(ready) == 0 {
			return fmt.Errorf("parallel lane scheduler found no dependency-eligible slice")
		}
		sort.Slice(ready, func(i, j int) bool {
			if ready[i].Priority != ready[j].Priority {
				return ready[i].Priority < ready[j].Priority
			}
			return ready[i].Name < ready[j].Name
		})

		results := make(chan parallelResult, len(ready))
		var workers sync.WaitGroup
		for _, prompt := range ready {
			prompt := prompt
			worktree := filepath.Join(root, safeName(prompt.ID))
			branch := "promptgrinder/lane/" + safeName(sequence.SequenceID) + "/" + runKey + "/" + safeName(prompt.ID)
			if err := gitWorktreeAdd(repoRoot, worktree, branch, coordinatorBranch); err != nil {
				return fmt.Errorf("create lane worktree for %s: %w", prompt.Name, err)
			}
			setParallelItem(sequence, prompt.Name, worktree, branch, "working")
			sequence.mark(prompt.Name, "running", state.Worker{}, nil, "")
			emitProgress(options, ProgressEvent{Type: "prompt.started", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "working", Lane: prompt.Lane, Priority: prompt.Priority, Worktree: worktree, IntegrationState: "working", Completed: len(runState.Completed), Total: len(prompts)})
			workers.Add(1)
			go func() {
				defer workers.Done()
				state, runErr := runPrompt(worktree, prompt, specContext, "", "", options, laneLauncher{repository: worktree, launcher: repositoryLauncher, waiter: waitLauncher(launcher)}, nil)
				results <- parallelResult{prompt: prompt, promptState: state, worktree: worktree, branch: branch, err: runErr}
			}()
		}
		workers.Wait()
		close(results)
		batch := make([]parallelResult, 0, len(ready))
		for result := range results {
			batch = append(batch, result)
		}
		sort.Slice(batch, func(i, j int) bool {
			if batch[i].prompt.Priority != batch[j].prompt.Priority {
				return batch[i].prompt.Priority < batch[j].prompt.Priority
			}
			return batch[i].prompt.Name < batch[j].prompt.Name
		})
		// Every lane in this batch has finished. Persist all terminal outcomes
		// before acting on a failure, otherwise a lower-priority failed result can
		// leave already-completed sibling lanes displayed as still working.
		var firstFailure *parallelResult
		for index := range batch {
			result := &batch[index]
			if result.err != nil {
				if err := persistParallelFailure(*result, options, sequence, store, runState, len(prompts)); err != nil {
					return err
				}
				if firstFailure == nil {
					firstFailure = result
				}
				continue
			}
			if err := store.savePrompt(result.promptState); err != nil {
				return err
			}
			setParallelItem(sequence, result.prompt.Name, result.worktree, result.branch, "waiting-to-merge")
			sequence.mark(result.prompt.Name, "waiting-to-merge", result.promptState.Worker, result.promptState.FinishedAt, "")
			identity := workeridentity.FromWorker(result.promptState.Worker)
			emitProgress(options, ProgressEvent{Type: "prompt.waiting-to-merge", SequenceID: sequence.SequenceID, PromptName: result.prompt.Name, PromptType: result.prompt.Type, Status: "waiting-to-merge", Lane: result.prompt.Lane, Priority: result.prompt.Priority, Worktree: result.worktree, IntegrationState: "waiting-to-merge", WorkerID: result.promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, Terminal: result.promptState.Worker.TerminalAdapter, LogPath: result.promptState.Worker.LogPath, Duration: promptDuration(result.promptState), Completed: len(runState.Completed), Total: len(prompts)})
		}
		sequence.refreshSummary()
		if err := sequenceStore.save(*sequence); err != nil {
			return err
		}
		if firstFailure != nil {
			runState.Status = "failed"
			runState.Current = firstFailure.prompt.Name
			runState.touch()
			if err := store.saveRun(*runState); err != nil {
				return err
			}
			summary.Run = *runState
			summary.Failed = firstFailure.err
			return firstFailure.err
		}
		for _, result := range batch {
			setParallelItem(sequence, result.prompt.Name, result.worktree, result.branch, "integrating")
			if err := gitMerge(coordinatorWorktree, result.branch); err != nil {
				return fmt.Errorf("integrate lane %s in isolated coordinator worktree %s: %w", result.prompt.Name, coordinatorWorktree, err)
			}
			setParallelItem(sequence, result.prompt.Name, result.worktree, result.branch, "integrated")
			sequence.mark(result.prompt.Name, "succeeded", result.promptState.Worker, result.promptState.FinishedAt, "")
			completed[result.prompt.ID] = true
			delete(remaining, result.prompt.ID)
			runState.Completed = append(runState.Completed, result.prompt.Name)
			identity := workeridentity.FromWorker(result.promptState.Worker)
			emitProgress(options, ProgressEvent{Type: "prompt.succeeded", SequenceID: sequence.SequenceID, PromptName: result.prompt.Name, PromptType: result.prompt.Type, Status: "integrated", Lane: result.prompt.Lane, Priority: result.prompt.Priority, Worktree: result.worktree, IntegrationState: "integrated", WorkerID: result.promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, Terminal: result.promptState.Worker.TerminalAdapter, LogPath: result.promptState.Worker.LogPath, Duration: promptDuration(result.promptState), Completed: len(runState.Completed), Total: len(prompts)})
		}
		sequence.refreshSummary()
		if err := sequenceStore.save(*sequence); err != nil {
			return err
		}
		if err := store.saveRun(*runState); err != nil {
			return err
		}
	}
	currentSHA, err := gitSHA(repoRoot)
	if err != nil {
		return err
	}
	if currentSHA != baseSHA {
		return fmt.Errorf("feature branch %s changed while lanes ran; retained isolated integration worktree at %s", targetBranch, coordinatorWorktree)
	}
	if err := gitFastForward(repoRoot, coordinatorBranch); err != nil {
		return fmt.Errorf("fast-forward feature branch %s from isolated coordinator %s: %w", targetBranch, coordinatorWorktree, err)
	}
	runState.Status = "completed"
	runState.Current = ""
	runState.touch()
	sequence.Status = "completed"
	sequence.refreshSummary()
	if err := store.saveRun(*runState); err != nil {
		return err
	}
	if err := sequenceStore.save(*sequence); err != nil {
		return err
	}
	summary.Run = *runState
	return nil
}

func waitLauncher(launcher Launcher) WaitLauncher {
	waiter, _ := launcher.(WaitLauncher)
	return waiter
}

func dependenciesComplete(prompt Prompt, completed map[string]bool) bool {
	for _, dependency := range prompt.DependsOn {
		if !completed[dependency] {
			return false
		}
	}
	return true
}

func setParallelItem(sequence *SequenceState, promptName, worktree, branch, integrationState string) {
	for index := range sequence.Items {
		if sequence.Items[index].PromptName == promptName {
			sequence.Items[index].Worktree = worktree
			sequence.Items[index].LaneBranch = branch
			sequence.Items[index].IntegrationState = integrationState
			return
		}
	}
}

func persistParallelFailure(result parallelResult, options Options, sequence *SequenceState, store folderStore, runState *RunState, total int) error {
	result.promptState.Status = "failed"
	result.promptState.Error = result.err.Error()
	if err := store.savePrompt(result.promptState); err != nil {
		return err
	}
	setParallelItem(sequence, result.prompt.Name, result.worktree, result.branch, "failed")
	sequence.mark(result.prompt.Name, "failed", result.promptState.Worker, result.promptState.FinishedAt, result.err.Error())
	identity := workeridentity.FromWorker(result.promptState.Worker)
	emitProgress(options, ProgressEvent{Type: "prompt.failed", SequenceID: sequence.SequenceID, PromptName: result.prompt.Name, PromptType: result.prompt.Type, Status: "failed", Lane: result.prompt.Lane, Priority: result.prompt.Priority, Worktree: result.worktree, IntegrationState: "failed", WorkerID: result.promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, Terminal: result.promptState.Worker.TerminalAdapter, LogPath: result.promptState.Worker.LogPath, Duration: promptDuration(result.promptState), Reason: result.err.Error(), Completed: len(runState.Completed), Total: total})
	return nil
}

func gitWorktreeAdd(repoRoot, worktree, branch, base string) error {
	command := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, worktree, base)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitCurrentBranch(repoRoot string) (string, error) {
	command := exec.Command("git", "-C", repoRoot, "branch", "--show-current")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" {
		return "", fmt.Errorf("detached HEAD")
	}
	return branch, nil
}

func gitMerge(repoRoot, branch string) error {
	command := exec.Command("git", "-C", repoRoot, "merge", "--no-ff", "--no-edit", branch)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitFastForward(repoRoot, branch string) error {
	command := exec.Command("git", "-C", repoRoot, "merge", "--ff-only", branch)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
