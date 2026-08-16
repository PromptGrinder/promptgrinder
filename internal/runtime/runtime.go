package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/engine/codex"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/markdown"
	"promptgrinder/internal/repository"
	"promptgrinder/internal/runfolder"
	"promptgrinder/internal/scheduler"
	"promptgrinder/internal/state"
	"promptgrinder/internal/terminal"
	"promptgrinder/internal/worker"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workeridentity"
	"promptgrinder/internal/workerpathpolicy"
	"promptgrinder/internal/workerstate"
	"promptgrinder/internal/worktree"
)

type Service struct {
	Store    state.Store
	Worker   worker.Manager
	Registry engine.Registry
	Config   config.Config
}

type RunSummary struct {
	Workers        []state.Worker
	Failed         []error
	ValidatedPaths []string
}

type RunOptions struct {
	EngineOverride          string
	SandboxOverride         string
	SharedContext           bool
	DryRun                  bool
	CommitEach              bool
	RequireCleanGit         bool
	RollbackOnFailure       bool
	AllowConcurrentWorktree bool
	OnProgress              func(SharedRunProgress)
}

type SharedRunProgress struct {
	Index    int
	Total    int
	TaskPath string
	WorkerID string
	Scope    string
	Engine   string
	Model    string
	Status   string
	Duration time.Duration
}

const (
	SharedRunStarted   = "started"
	SharedRunSucceeded = "succeeded"
	SharedRunFailed    = "failed"
)

type ReconcileSummary struct {
	Workers    []state.Worker
	Sequences  []runfolder.SequenceState
	Threshold  time.Duration
	MarkFailed bool
}

type EventSummary struct {
	Event state.Event
	Found bool
}

type StatusSummary struct {
	Worker      state.Worker
	EventPath   string
	LatestEvent EventSummary
}

type RunFolderOptions = runfolder.Options
type RunFolderSummary = runfolder.Summary
type RunFolderProgressEvent = runfolder.ProgressEvent
type RunFolderPreflight = runfolder.PreflightResult
type SequenceProgress = runfolder.SequenceProgress
type SequenceState = runfolder.SequenceState

const (
	RunFolderExecutionConfigured = runfolder.ExecutionConfigured
	RunFolderExecutionForeground = runfolder.ExecutionForeground
)

type TerminalCandidate struct {
	WorkerID string `json:"worker_id"`
	Status   string `json:"status"`
	TaskPath string `json:"task_path"`
	Title    string `json:"title"`
}

func NewService(cfg config.Config) Service {
	store := state.NewStore(cfg.HomeDir)
	exe, err := os.Executable()
	if err != nil {
		exe = "promptgrinder"
	} else if absolute, absErr := filepath.Abs(exe); absErr == nil {
		exe = filepath.Clean(absolute)
	}
	executorFactory := func(runCfg config.Config) (execution.Executor, error) {
		if err := terminal.ValidateAvailability(runCfg.TerminalAdapter, runCfg.TerminalMode); err != nil {
			return execution.Executor{}, err
		}
		adapter, err := terminal.SelectAdapter(runCfg.TerminalAdapter, runCfg.TerminalMode)
		if err != nil {
			return execution.Executor{}, err
		}
		return execution.Executor{Store: store, Terminal: adapter}, nil
	}
	registry := engine.NewRegistry(codex.Engine{Command: cfg.CodexExecutable})
	return Service{
		Store:    store,
		Registry: registry,
		Config:   cfg,
		Worker: worker.Manager{
			Store:         store,
			Registry:      registry,
			Executable:    exe,
			UseRepoConfig: true,
			BaseConfig:    cfg,
			NewExecutor:   executorFactory,
		},
	}
}

func (s Service) RunPath(path string) (RunSummary, error) {
	return s.RunPathWithOptions(path, RunOptions{})
}

func (s Service) RunPathWithOptions(path string, options RunOptions) (RunSummary, error) {
	manager := s.Worker
	manager.EngineOverride = options.EngineOverride
	manager.SandboxOverride = options.SandboxOverride
	info, err := os.Stat(path)
	if err != nil {
		return RunSummary{}, fmt.Errorf("task path does not exist: %s", path)
	}
	if info.IsDir() {
		return s.runFolder(path, manager)
	}
	if !scheduler.IsPromptFile(path) {
		return RunSummary{}, fmt.Errorf("task file must use .md or .pg: %s", path)
	}
	result := manager.Launch(path)
	if result.Err != nil {
		return RunSummary{Workers: nonZeroWorkers(result.Worker), Failed: []error{result.Err}}, result.Err
	}
	return RunSummary{Workers: []state.Worker{result.Worker}}, nil
}

func (s Service) RunPathsWithOptions(paths []string, options RunOptions) (RunSummary, error) {
	paths, err := resolveRunFiles(paths)
	if err != nil {
		return RunSummary{}, err
	}
	manager := s.Worker
	manager.EngineOverride = options.EngineOverride
	manager.SandboxOverride = options.SandboxOverride
	for _, path := range paths {
		if _, err := manager.Validate(path); err != nil {
			return RunSummary{ValidatedPaths: paths}, fmt.Errorf("preflight failed for %s: %w", path, err)
		}
	}
	if options.SharedContext {
		sort.Strings(paths)
	}
	summary := RunSummary{ValidatedPaths: append([]string(nil), paths...)}
	if options.DryRun {
		return summary, nil
	}

	if options.SharedContext {
		repoRoot, err := sharedRunRepository(paths, manager.RepositoryOverride)
		if err != nil {
			return summary, err
		}
		lease, err := worktree.Acquire(filepath.Dir(s.Store.WorkersDir), repoRoot, "shared-context run", options.AllowConcurrentWorktree)
		if err != nil {
			return summary, err
		}
		defer lease.Release()
		if options.RequireCleanGit {
			clean, err := sharedGitClean(repoRoot)
			if err != nil {
				return summary, err
			}
			if !clean {
				return summary, fmt.Errorf("shared-context run requires a clean working tree; commit, stash, or use --require-clean-git=false")
			}
		}
		return s.runSharedPaths(paths, options, manager, summary)
	}
	for _, path := range paths {
		result := manager.Launch(path)
		if result.Worker.ID != "" {
			summary.Workers = append(summary.Workers, result.Worker)
		}
		if result.Err != nil {
			summary.Failed = append(summary.Failed, result.Err)
		}
	}
	if len(summary.Failed) > 0 {
		return summary, summary.Failed[0]
	}
	return summary, nil
}

func resolveRunFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one task path is required")
	}
	expanded := []string{}
	for _, path := range paths {
		if strings.ContainsAny(path, "\r\n") {
			return nil, fmt.Errorf("task path or pattern contains a newline; keep each quoted path or glob on one shell line: %q", path)
		}
		if !strings.ContainsAny(path, "*?[") {
			expanded = append(expanded, path)
			continue
		}
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, fmt.Errorf("invalid task pattern %q: %w", path, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("task pattern matched no files: %s", path)
		}
		for _, match := range matches {
			expanded = append(expanded, match)
		}
	}
	files := []string{}
	seen := map[string]bool{}
	for _, path := range expanded {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("task path does not exist: %s", path)
		}
		candidates := []string{path}
		if info.IsDir() {
			candidates, err = scheduler.DiscoverMarkdown(path)
			if err != nil {
				return nil, err
			}
			if len(candidates) == 0 {
				return nil, fmt.Errorf("no Markdown task files found in %s", path)
			}
		}
		for _, candidate := range candidates {
			if !scheduler.IsPromptFile(candidate) {
				return nil, fmt.Errorf("task file must use .md or .pg: %s", candidate)
			}
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				return nil, err
			}
			if !seen[absolute] {
				seen[absolute] = true
				files = append(files, absolute)
			}
		}
	}
	return files, nil
}

func (s Service) runSharedPaths(ordered []string, options RunOptions, manager worker.Manager, summary RunSummary) (RunSummary, error) {
	manager.CaptureCodexSession = true
	sessionID := ""
	previousTask := ""
	previousCompletionReport := ""
	repoRoot, err := sharedRunRepository(ordered, manager.RepositoryOverride)
	if err != nil {
		return summary, err
	}
	checkpointSHA, err := sharedGitSHA(repoRoot)
	if err != nil {
		return summary, err
	}
	rollbackArmed := options.RollbackOnFailure && options.CommitEach && options.RequireCleanGit
	for index, path := range ordered {
		baseline, err := workerpathpolicy.Capture(repoRoot)
		if err != nil {
			return summary, fmt.Errorf("capture worker Git baseline: %w", err)
		}
		if options.CommitEach && len(filterRuntimeStateEntries(baseline.Entries)) != 0 {
			return summary, fmt.Errorf("working tree is dirty; automatic commits require a clean baseline (use --commit-each=false to retain unrelated changes)")
		}
		if rollbackArmed {
			clean, err := sharedGitClean(repoRoot)
			if err != nil {
				return summary, err
			}
			if !clean {
				return summary, fmt.Errorf("working tree changed outside the active prompt; refusing automatic rollback")
			}
		}
		manager.CodexSessionID = sessionID
		result := worker.LaunchResult{}
		if previousCompletionReport == "" {
			result = manager.Launch(path)
		} else {
			content, err := sharedPromptWithHandoff(path, previousTask, previousCompletionReport)
			if err != nil {
				return summary, err
			}
			result = manager.LaunchContent(path, content)
		}
		if result.Worker.ID != "" {
			summary.Workers = append(summary.Workers, result.Worker)
		}
		if result.Err != nil {
			reportSharedRunProgress(options, index, len(ordered), path, result.Worker, SharedRunFailed, workerDuration(result.Worker))
			summary.Failed = append(summary.Failed, result.Err)
			return summary, result.Err
		}
		worker := result.Worker
		reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunStarted, 0)
		if worker.TerminalAdapter == "dry-run" && !state.IsTerminalStatus(worker.Status) {
			err := fmt.Errorf("shared-context run requires workers to finish; dry-run workers do not execute")
			reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
			summary.Failed = append(summary.Failed, err)
			return summary, err
		}
		for !state.IsTerminalStatus(worker.Status) {
			time.Sleep(2 * time.Second)
			var err error
			worker, err = manager.Store.Load(worker.ID)
			if err != nil {
				reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
				summary.Failed = append(summary.Failed, err)
				return summary, err
			}
		}
		summary.Workers[len(summary.Workers)-1] = worker
		if worker.Status != state.StatusSucceeded {
			err := sharedWorkerFailure(path, worker)
			workerStopped := worker.Status == state.StatusFailed || worker.Status == state.StatusLaunchFailed
			err = rollbackSharedFailure(repoRoot, checkpointSHA, rollbackArmed && workerStopped, err)
			reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
			summary.Failed = append(summary.Failed, err)
			return summary, err
		}
		if worker.EngineResult == nil || worker.EngineResult.SessionID == "" {
			err := fmt.Errorf("Codex did not report a session id for %s", path)
			err = rollbackSharedFailure(repoRoot, checkpointSHA, rollbackArmed, err)
			reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
			summary.Failed = append(summary.Failed, err)
			return summary, err
		}
		changes, err := workerpathpolicy.AttributedChanges(repoRoot, baseline)
		if err != nil {
			return summary, fmt.Errorf("attribute worker changes: %w", err)
		}
		changes = filterRuntimeStatePaths(changes)
		policy, err := ordinaryTaskPolicy(path)
		if err != nil {
			return summary, err
		}
		violations, err := workerpathpolicy.Violations(policy, changes)
		if err != nil {
			return summary, err
		}
		if len(violations) != 0 {
			err := fmt.Errorf("path policy violation at completion: %d path(s); changes retained for review", len(violations))
			reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
			summary.Failed = append(summary.Failed, err)
			return summary, err
		}
		if options.CommitEach {
			commitSHA, err := commitSharedChanges(repoRoot, "PromptGrinder: complete "+filepath.Base(path), baseline, changes)
			if err != nil {
				reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunFailed, workerDuration(worker))
				summary.Failed = append(summary.Failed, err)
				return summary, err
			}
			if commitSHA != "" {
				checkpointSHA = commitSHA
			}
			if options.RollbackOnFailure {
				rollbackArmed = true
			}
		}
		reportSharedRunProgress(options, index, len(ordered), path, worker, SharedRunSucceeded, workerDuration(worker))
		sessionID = worker.EngineResult.SessionID
		previousTask = filepath.Base(path)
		previousCompletionReport = worker.EngineResult.Summary
	}
	return summary, nil
}

func sharedPromptWithHandoff(taskPath, previousTask, completionReport string) (string, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`%s

---

# PromptGrinder Shared-Context Prerequisite Handoff

The immediately preceding prompt completed successfully. Treat its structured report below as authoritative prerequisite evidence for this run, alongside repository artifacts and tests.

Previous prompt: %s

%s
`, string(data), previousTask, completionReport), nil
}

func sharedRunRepository(paths []string, repositoryOverride string) (string, error) {
	if repositoryOverride != "" {
		return repository.DetectRoot(repositoryOverride)
	}
	var repoRoot string
	for _, path := range paths {
		root, err := repository.DetectRoot(path)
		if err != nil {
			return "", err
		}
		if repoRoot == "" {
			repoRoot = root
			continue
		}
		if root != repoRoot {
			return "", fmt.Errorf("shared-context prompts must belong to one repository: %s and %s", repoRoot, root)
		}
	}
	if repoRoot == "" {
		return "", fmt.Errorf("shared-context run has no repository")
	}
	return repoRoot, nil
}

func sharedGitClean(repoRoot string) (bool, error) {
	changed, err := sharedGitChangedFiles(repoRoot)
	return len(changed) == 0, err
}

func sharedGitSHA(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func sharedGitChangedFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git status failed: %w", err)
	}
	var changed []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		name := strings.TrimSpace(line[3:])
		if isPromptGrinderStatePath(name) {
			continue
		}
		changed = append(changed, name)
	}
	return changed, nil
}

func commitSharedChanges(repoRoot, message string, attributed ...any) (string, error) {
	var baseline workerpathpolicy.Snapshot
	var changed []string
	if len(attributed) == 2 {
		baseline, _ = attributed[0].(workerpathpolicy.Snapshot)
		changed, _ = attributed[1].([]string)
	} else {
		var err error
		baseline, err = workerpathpolicy.Capture(repoRoot)
		if err != nil {
			return "", err
		}
		changed, err = sharedGitChangedFiles(repoRoot)
		if err != nil {
			return "", err
		}
		// Compatibility for callers that only request committing an already
		// cleanly attributed tree: treat current changes as the approved set.
		baseline.Entries = map[string]string{}
	}
	if len(changed) == 0 {
		return "", nil
	}
	current, err := workerpathpolicy.AttributedChanges(repoRoot, baseline)
	if err != nil || !samePaths(filterRuntimeStatePaths(current), changed) {
		return "", fmt.Errorf("worktree changed during worker attribution; refusing automatic commit")
	}
	args := []string{"-C", repoRoot, "add", "--all", "--"}
	for _, name := range changed {
		args = append(args, ":(literal)"+name)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
	}
	staged, err := runtimeChangedPathSet(repoRoot, "diff", "--cached", "--name-status", "-z", "--find-renames")
	if err != nil || !samePaths(staged, changed) {
		return "", sharedStagedChangeMismatchError(repoRoot, baseline, changed)
	}
	current, err = workerpathpolicy.AttributedChanges(repoRoot, baseline)
	if err != nil || !samePaths(filterRuntimeStatePaths(current), changed) {
		return "", fmt.Errorf("index or worktree changed before commit; refusing automatic commit")
	}
	cmd := exec.Command("git", "-C", repoRoot, "-c", "user.name=PromptGrinder", "-c", "user.email=promptgrinder@example.invalid", "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
	}
	sha, err := sharedGitSHA(repoRoot)
	if err != nil {
		return "", err
	}
	committed, err := runtimeChangedPathSet(repoRoot, "diff", "--name-status", "-z", "--find-renames", baseline.Head, sha)
	if err != nil || !samePaths(committed, changed) {
		return "", fmt.Errorf("created commit does not exactly match approved worker changes")
	}
	return sha, nil
}

func sharedStagedChangeMismatchError(repoRoot string, baseline workerpathpolicy.Snapshot, approved []string) error {
	conflict, err := workerpathpolicy.DetectCommitOwnershipConflict(repoRoot, baseline, approved)
	if err == nil && conflict != nil {
		return conflict
	}
	return fmt.Errorf("staged change set does not exactly match approved worker changes")
}

func ordinaryTaskPolicy(taskPath string) (workerdomain.WorkerPolicy, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return workerdomain.WorkerPolicy{}, err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return workerdomain.WorkerPolicy{}, err
	}
	stringsFor := func(key string) []string {
		var out []string
		if list, ok := task.Metadata[key].([]any); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					out = append(out, value)
				}
			}
		}
		return out
	}
	return workerdomain.WorkerPolicy{AllowedPaths: stringsFor("allowed_paths"), ForbiddenPaths: stringsFor("forbidden_paths")}, nil
}

func filterRuntimeStateEntries(entries map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range entries {
		if !isPromptGrinderStatePath(name) {
			out[name] = value
		}
	}
	return out
}
func filterRuntimeStatePaths(paths []string) []string {
	out := paths[:0]
	for _, name := range paths {
		if !isPromptGrinderStatePath(name) {
			out = append(out, name)
		}
	}
	return out
}
func samePaths(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func runtimeGitPaths(repo string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			paths = append(paths, filepath.ToSlash(name))
		}
	}
	sort.Strings(paths)
	return paths, nil
}
func runtimeChangedPathSet(repo string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	var paths []string
	for i := 0; i < len(fields) && fields[i] != ""; {
		status := fields[i]
		i++
		count := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			count = 2
		}
		for j := 0; j < count && i < len(fields) && fields[i] != ""; j, i = j+1, i+1 {
			paths = append(paths, filepath.ToSlash(fields[i]))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func rollbackSharedFailure(repoRoot, checkpointSHA string, armed bool, cause error) error {
	if !armed {
		return cause
	}
	if err := rollbackSharedChanges(repoRoot, checkpointSHA); err != nil {
		return fmt.Errorf("%w\nRollback: failed: %v", cause, err)
	}
	return fmt.Errorf("%w\nRollback: restored checkpoint %s", cause, checkpointSHA)
}

func sharedWorkerFailure(taskPath string, worker state.Worker) error {
	lines := []string{
		fmt.Sprintf("prompt failed: %s", filepath.Base(taskPath)),
		fmt.Sprintf("Worker: %s", worker.ID),
		fmt.Sprintf("Worker status: %s", worker.Status),
	}
	if worker.EngineResult != nil {
		result := worker.EngineResult
		if result.CompletionStatus != "" || result.NextPromptSafe != nil {
			lines = append(lines, fmt.Sprintf("Completion: STATUS=%s, NEXT_PROMPT_SAFE=%s", valueOrUnknown(result.CompletionStatus), boolValueOrUnknown(result.NextPromptSafe)))
		}
		if reason := completionReportDetail(result.Summary, "SUMMARY"); reason != "" {
			lines = append(lines, "Reason: "+reason)
		}
		if issue := completionReportDetail(result.Summary, "OPEN_ISSUES"); issue != "" {
			lines = append(lines, "Next action: "+issue)
		}
	}
	if worker.LogPath != "" {
		lines = append(lines, "Log: "+worker.LogPath)
	}
	return errors.New(strings.Join(lines, "\n"))
}

func completionReportDetail(summary, section string) string {
	inSection := false
	var details []string
	for _, rawLine := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == section+":" {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasSuffix(line, ":") && strings.ToUpper(line) == line {
			break
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" {
			details = append(details, line)
			if len(details) == 2 {
				break
			}
		}
	}
	return strings.Join(details, " ")
}

func rollbackSharedChanges(repoRoot, checkpointSHA string) error {
	if checkpointSHA == "" {
		return fmt.Errorf("rollback checkpoint is empty")
	}
	if out, err := exec.Command("git", "-C", repoRoot, "reset", "--hard", checkpointSHA).CombinedOutput(); err != nil {
		return fmt.Errorf("git reset failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func isPromptGrinderStatePath(name string) bool {
	name = filepath.ToSlash(strings.TrimSpace(name))
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if part != ".promptgrinder" || i+1 >= len(parts) {
			continue
		}
		rest := strings.Join(parts[i+1:], "/")
		return rest == "run.json" || rest == "summary.md" || strings.HasPrefix(rest, "prompts/")
	}
	return false
}

func reportSharedRunProgress(options RunOptions, index, total int, taskPath string, worker state.Worker, status string, duration time.Duration) {
	if options.OnProgress == nil {
		return
	}
	identity := workeridentity.FromWorker(worker)
	options.OnProgress(SharedRunProgress{
		Index:    index + 1,
		Total:    total,
		TaskPath: taskPath,
		WorkerID: worker.ID,
		Scope:    identity.Scope,
		Engine:   identity.Engine,
		Model:    identity.Model,
		Status:   status,
		Duration: duration,
	})
}

func workerDuration(worker state.Worker) time.Duration {
	if worker.StartTime == nil {
		return 0
	}
	end := time.Now().UTC()
	if worker.FinishTime != nil {
		end = *worker.FinishTime
	}
	if end.Before(*worker.StartTime) {
		return 0
	}
	return end.Sub(*worker.StartTime)
}

func (s Service) Engines() []engine.Descriptor {
	return s.Registry.List()
}

func (s Service) DescribeEngine(name string) (engine.Descriptor, error) {
	adapter, err := s.Registry.Lookup(name)
	if err != nil {
		return engine.Descriptor{}, err
	}
	return adapter.Describe(), nil
}

func (s Service) Defaults() config.DefaultsReport {
	cfg := s.Config
	repoRoot := ""
	if cwd, err := os.Getwd(); err == nil {
		if detected, err := repository.DetectRoot(cwd); err == nil {
			repoRoot = detected
			if loaded, err := config.LoadWithHome(repoRoot, cfg.HomeDir); err == nil {
				cfg = loaded
			}
		}
	}
	return config.Defaults(repoRoot, cfg)
}

func (s Service) Validate(path, engineOverride, repoPath string) (worker.ValidationPlan, error) {
	manager := s.Worker
	manager.EngineOverride = engineOverride
	manager.RepositoryOverride = repoPath
	plan, err := manager.Validate(path)
	if plan.ExecutionPlan == nil {
		plan.ExecutionPlan = map[string]any{}
	}
	if repoPath != "" {
		plan.ExecutionPlan["repository_source"] = "explicit"
	} else {
		plan.ExecutionPlan["repository_source"] = "inferred"
	}
	plan.ExecutionPlan["validation_mode"] = "standalone task; run-folder ordering, dependency, and completion checks are not evaluated"
	if err != nil {
		return plan, err
	}
	repoRoot, _ := plan.ExecutionPlan["repository_path"].(string)
	policy, policyErr := runfolder.InspectTaskPolicy(repoRoot, path)
	if policyErr != nil {
		err = fmt.Errorf("validate role and path policy: %w", policyErr)
		plan.Valid = false
		plan.Errors = append(plan.Errors, err.Error())
		return plan, err
	}
	plan.ExecutionPlan["task_policy"] = policy
	return plan, nil
}

func (s Service) RunPromptFolder(path string, options RunFolderOptions) (RunFolderSummary, error) {
	if options.HomeDir == "" {
		options.HomeDir = filepath.Dir(s.Store.WorkersDir)
	}
	if options.RepoPath == "" {
		options.RepoPath = "."
	}
	preflight, err := s.PreflightRunFolder(path, options)
	if err != nil {
		return RunFolderSummary{}, err
	}
	repoRoot := preflight.Repository
	options.RepoPath = repoRoot
	lease, err := worktree.Acquire(options.HomeDir, repoRoot, "run-folder "+path, options.AllowConcurrentWorktree)
	if err != nil {
		return RunFolderSummary{}, err
	}
	defer lease.Release()
	options.BaseConfig = s.Worker.BaseConfig
	options.UseRepoConfig = s.Worker.UseRepoConfig
	if options.SupervisorID != "" && options.Notifier == nil {
		options.Notifier = runfolder.LocalNotifier{Path: filepath.Join(options.HomeDir, "events", "notifications.jsonl")}
	}
	manager := s.Worker
	manager.EngineOverride = options.EngineOverride
	manager.RepositoryOverride = repoRoot
	switch options.ExecutionPolicy {
	case runfolder.ExecutionConfigured:
	case runfolder.ExecutionForeground:
		manager.TerminalAdapterOverride = "headless"
		manager.TerminalModeOverride = "normal"
	default:
		return RunFolderSummary{}, fmt.Errorf("unknown run-folder execution policy %q", options.ExecutionPolicy)
	}
	return runfolder.Run(path, options, promptLauncher{manager: manager})
}

func (s Service) PreflightRunFolder(path string, options RunFolderOptions) (RunFolderPreflight, error) {
	if options.HomeDir == "" {
		options.HomeDir = filepath.Dir(s.Store.WorkersDir)
	}
	options.BaseConfig = s.Worker.BaseConfig
	options.UseRepoConfig = s.Worker.UseRepoConfig
	preflight, err := runfolder.Preflight(path, options)
	if err != nil {
		return preflight, err
	}
	manager := s.Worker
	manager.EngineOverride = options.EngineOverride
	manager.RepositoryOverride = preflight.Repository
	manager.SkipExecutorValidation = true
	for index, prompt := range preflight.Inspection.Prompts {
		if index < preflight.ResumeIndex {
			continue
		}
		if prompt.Type == runfolder.TypeSpecification {
			continue
		}
		content, err := os.ReadFile(prompt.Path)
		if err != nil {
			return RunFolderPreflight{}, err
		}
		if err := manager.ValidateContentWithMetadata(prompt.Path, string(content), nil); err != nil {
			return RunFolderPreflight{}, fmt.Errorf("run-folder model preflight %s: %w", prompt.Name, err)
		}
	}
	return preflight, nil
}

func (s Service) Sequences() ([]SequenceProgress, error) {
	return runfolder.ListSequences(filepath.Dir(s.Store.WorkersDir))
}

func (s Service) SequenceID(path string, options RunFolderOptions) (string, error) {
	if options.HomeDir == "" {
		options.HomeDir = filepath.Dir(s.Store.WorkersDir)
	}
	options.BaseConfig = s.Worker.BaseConfig
	options.UseRepoConfig = s.Worker.UseRepoConfig
	return runfolder.ResolveSequenceID(path, options)
}

func (s Service) Sequence(sequenceID string) (SequenceState, error) {
	homeDir := filepath.Dir(s.Store.WorkersDir)
	if sequenceID == "current" {
		return runfolder.CurrentSequence(homeDir)
	}
	return runfolder.LoadSequence(homeDir, sequenceID)
}

func (s Service) CancelSequence(sequenceID string) (SequenceState, error) {
	homeDir := filepath.Dir(s.Store.WorkersDir)
	sequence, err := runfolder.LoadSequence(homeDir, sequenceID)
	if err != nil {
		return SequenceState{}, err
	}
	for _, item := range sequence.Items {
		if item.Status == "running" && item.WorkerID != "" {
			if _, cancelErr := s.Cancel(item.WorkerID); cancelErr != nil && !errors.Is(cancelErr, os.ErrNotExist) {
				return SequenceState{}, cancelErr
			}
		}
	}
	cancelled, err := runfolder.CancelSequence(homeDir, sequenceID)
	if err != nil {
		return SequenceState{}, err
	}
	if sequence.Supervisor != nil && sequence.Supervisor.PID > 0 && sequence.Supervisor.PID != os.Getpid() {
		if err := runfolder.StopSequenceSupervisor(homeDir, sequence.Supervisor); err != nil {
			return cancelled, fmt.Errorf("cancel sequence supervisor: %w", err)
		}
		if err := worktree.ReleaseForPID(homeDir, sequence.RepositoryPath, sequence.Supervisor.PID); err != nil {
			return cancelled, fmt.Errorf("release sequence worktree claim: %w", err)
		}
	}
	return cancelled, nil
}

func (s Service) TerminalCandidates() ([]TerminalCandidate, error) {
	workers, err := s.Store.List()
	if err != nil {
		return nil, err
	}
	out := []TerminalCandidate{}
	for _, worker := range workers {
		if worker.TerminalAdapter != "terminal" && worker.TerminalAdapter != "iterm" {
			continue
		}
		if worker.TerminalClosedAt != nil {
			continue
		}
		if worker.ID == "" {
			continue
		}
		out = append(out, TerminalCandidate{
			WorkerID: worker.ID,
			Status:   worker.Status,
			TaskPath: worker.TaskPath,
			Title:    "PromptGrinder: " + worker.ID,
		})
	}
	return out, nil
}

func (s Service) CloseTerminals(workerIDs []string) ([]TerminalCandidate, error) {
	candidates, err := s.TerminalCandidates()
	if err != nil {
		return nil, err
	}
	selected := []TerminalCandidate{}
	selectedIDs := map[string]bool{}
	for _, id := range workerIDs {
		selectedIDs[id] = true
	}
	for _, candidate := range candidates {
		if len(selectedIDs) == 0 || selectedIDs[candidate.WorkerID] {
			selected = append(selected, candidate)
		}
	}
	titles := []string{}
	for _, candidate := range selected {
		titles = append(titles, candidate.Title)
	}
	if len(workerIDs) == 0 {
		titles = append(titles, "__PROMPTGRINDER_ALL__")
	}
	if err := terminal.CloseTerminalTabs(titles); err != nil {
		return selected, err
	}
	for _, candidate := range selected {
		_, _ = s.Store.MarkTerminalClosed(candidate.WorkerID)
	}
	return selected, nil
}

func (s Service) runFolder(path string, manager worker.Manager) (RunSummary, error) {
	files, err := scheduler.DiscoverMarkdown(path)
	if err != nil {
		return RunSummary{}, err
	}
	summary := RunSummary{}
	for _, file := range files {
		result := manager.Launch(file)
		if result.Err != nil {
			summary.Failed = append(summary.Failed, fmt.Errorf("%s: %w", file, result.Err))
			if result.Worker.ID != "" {
				summary.Workers = append(summary.Workers, result.Worker)
			}
			continue
		}
		summary.Workers = append(summary.Workers, result.Worker)
	}
	if len(files) == 0 {
		return summary, fmt.Errorf("no Markdown task files found in %s", path)
	}
	if len(summary.Failed) > 0 {
		return summary, fmt.Errorf("%d worker launch failed", len(summary.Failed))
	}
	return summary, nil
}

func (s Service) List() ([]state.Worker, error) {
	return s.Store.List()
}

func (s Service) Status(id string) (StatusSummary, error) {
	worker, err := s.Store.Load(id)
	if err != nil {
		return StatusSummary{}, err
	}
	latest, err := s.LatestEvent(id)
	if err != nil {
		return StatusSummary{}, err
	}
	return StatusSummary{Worker: worker, EventPath: s.Store.EventPath(id), LatestEvent: latest}, nil
}

func (s Service) LatestEvent(id string) (EventSummary, error) {
	event, ok, err := s.Store.LatestEvent(id)
	return EventSummary{Event: event, Found: ok}, err
}

func (s Service) Events(id string, filter state.EventFilter) (state.EventReadResult, error) {
	return s.Store.ReadEvents(id, filter)
}

func (s Service) GlobalEvents(filter state.EventFilter) (state.EventReadResult, error) {
	return s.Store.ReadGlobalEvents(filter)
}

func (s Service) EventPath(id string) (string, error) {
	if _, err := s.Store.Load(id); err != nil {
		return "", err
	}
	return s.Store.EventPath(id), nil
}

func (s Service) GlobalEventPath() string {
	return s.Store.GlobalEventPath()
}

func (s Service) Logs(id string) (string, error) {
	worker, err := s.Store.Load(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(worker.LogPath)
	if os.IsNotExist(err) {
		return fmt.Sprintf("No log file yet: %s\n", worker.LogPath), nil
	}
	return string(data), err
}

func (s Service) Prune() (int, error) {
	return s.Store.Prune()
}

func (s Service) Complete(id string) (state.Worker, error) {
	return s.Store.Complete(id)
}

func (s Service) Fail(id string) (state.Worker, error) {
	return s.Store.Fail(id)
}

func (s Service) Cancel(id string) (state.Worker, error) {
	worker, err := s.Store.Load(id)
	if err != nil {
		return state.Worker{}, err
	}
	if worker.ProcessGroupID > 0 {
		if err := terminateOwnedProcessGroup(worker.ProcessGroupID, 3*time.Second); err != nil {
			return state.Worker{}, fmt.Errorf("cancel worker %s: %w", id, err)
		}
	}
	return s.Store.Cancel(id)
}

func terminateOwnedProcessGroup(pgid int, grace time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal process group %d: %w", pgid, err)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect process group %d: %w", pgid, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pgid, err)
	}
	return nil
}

func (s Service) Reconcile(threshold time.Duration, markFailed bool) (ReconcileSummary, error) {
	workers, err := s.Store.Reconcile(threshold, markFailed, time.Now().UTC())
	if err != nil {
		return ReconcileSummary{Workers: workers, Threshold: threshold, MarkFailed: markFailed}, err
	}
	sequences, err := runfolder.ReconcileSequences(filepath.Dir(s.Store.WorkersDir), threshold, markFailed)
	return ReconcileSummary{Workers: workers, Sequences: sequences, Threshold: threshold, MarkFailed: markFailed}, err
}

func (s Service) FinishWorker(recordPath string, exitCode int) error {
	worker, err := s.Store.LoadPath(recordPath)
	if err != nil {
		return err
	}
	result := state.EngineResult{}
	engineExitCode := exitCode
	resultParsed := false
	adapter, err := s.Registry.Lookup(worker.Engine)
	if err == nil {
		if parser, ok := adapter.(engine.ResultParser); ok {
			resultPath := worker.LogPath
			if worker.Engine == "codex" {
				resultPath = codex.CapturedOutputPath(recordPath)
			}
			logData, readErr := os.ReadFile(resultPath)
			if readErr != nil && resultPath != worker.LogPath {
				logData, readErr = os.ReadFile(worker.LogPath)
			}
			if readErr == nil {
				if ctx, contextErr := execution.NewContext(worker, config.Config{}, nil); contextErr == nil {
					result = parser.ParseResult(ctx, logData)
					resultParsed = !result.Empty()
					if resultParsed || worker.Engine == "codex" {
						result.EngineExitCode = &engineExitCode
					}
				}
			}
		}
	}
	semanticFailure := exitCode == 0 && result.RejectsContinuation()
	malformedSuccess := exitCode == 0 && worker.Engine == "codex" && strings.TrimSpace(result.Summary) == ""
	if malformedSuccess {
		result.CompletionReason = "empty final answer"
		resultParsed = true
	}
	if semanticFailure {
		exitCode = 1
	}
	if malformedSuccess {
		exitCode = 1
	}
	ordinaryPolicyErr := enforceOrdinaryWorkerPathPolicy(worker)
	if ordinaryPolicyErr != nil {
		exitCode = 1
	}
	// Persist the parsed engine result before publishing a terminal worker
	// status. Ordered runners poll worker.json and must never observe
	// succeeded/failed with a stale or missing completion report.
	if resultParsed {
		worker.EngineResult = &result
		if worker.SummaryPath == "" {
			worker.SummaryPath = filepath.Join(filepath.Dir(s.Store.WorkersDir), "summaries", "workers", worker.ID+".md")
		}
		if err := s.Store.Save(worker); err != nil {
			return err
		}
	}
	if err := s.Store.MarkFinished(recordPath, exitCode); err != nil {
		return err
	}
	if err := s.enforceNamedWorkerPathPolicy(worker); err != nil {
		return err
	}
	if ordinaryPolicyErr != nil {
		return ordinaryPolicyErr
	}
	if !resultParsed {
		if malformedSuccess {
			return fmt.Errorf("Codex exited successfully but produced no parseable final message; inspect with promptgrinder logs %s, then retry the task", worker.ID)
		}
		return nil
	}
	worker, err = s.Store.LoadPath(recordPath)
	if err != nil {
		return err
	}
	if result.Summary != "" {
		if err := os.MkdirAll(filepath.Dir(worker.SummaryPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(worker.SummaryPath, []byte(workerSummaryMarkdown(worker, result)), 0o644); err != nil {
			return err
		}
	}
	if err := state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineResultParsed, state.SeverityInfo, "Engine result parsed", engineResultEventData(result))); err != nil {
		return err
	}
	if semanticFailure {
		return fmt.Errorf("engine reported a non-continuable result: STATUS=%s NEXT_PROMPT_SAFE=%s", valueOrUnknown(result.CompletionStatus), boolValueOrUnknown(result.NextPromptSafe))
	}
	if malformedSuccess {
		return fmt.Errorf("Codex exited successfully but produced no parseable final message; inspect with promptgrinder logs %s, then retry the task", worker.ID)
	}
	return nil
}

func enforceOrdinaryWorkerPathPolicy(worker state.Worker) error {
	raw, _ := worker.Metadata["ordinary_path_baseline"].(string)
	if raw == "" {
		return nil
	}
	var baseline workerpathpolicy.Snapshot
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil {
		return fmt.Errorf("load ordinary-worker path baseline: %w", err)
	}
	paths, err := workerpathpolicy.AttributedChanges(worker.RepositoryPath, baseline)
	if err != nil {
		return fmt.Errorf("check ordinary-worker path policy: %w", err)
	}
	values := func(key string) []string {
		var out []string
		if list, ok := worker.Metadata[key].([]any); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					out = append(out, value)
				}
			}
		}
		return out
	}
	policy := workerdomain.WorkerPolicy{AllowedPaths: values("ordinary_allowed_paths"), ForbiddenPaths: values("ordinary_forbidden_paths")}
	violations, err := workerpathpolicy.Violations(policy, paths)
	if err != nil {
		return err
	}
	if len(violations) != 0 {
		return fmt.Errorf("path policy violation at completion: %d path(s); changes retained for review", len(violations))
	}
	return nil
}

func (s Service) enforceNamedWorkerPathPolicy(worker state.Worker) error {
	projectID, _ := worker.Metadata["named_project_id"].(string)
	workerID, _ := worker.Metadata["named_worker_id"].(string)
	taskID, _ := worker.Metadata["named_task_id"].(string)
	enabled, _ := worker.Metadata["named_path_policy"].(bool)
	worktreePath, _ := worker.Metadata["selected_worktree"].(string)
	if !enabled || projectID == "" || workerID == "" || taskID == "" || worktreePath == "" {
		return nil
	}
	baseline, err := workerpathpolicy.LoadSnapshot(s.Config.HomeDir, projectID, workerID)
	if err != nil {
		return fmt.Errorf("load named-worker path-policy snapshot: %w", err)
	}
	paths, err := workerpathpolicy.AttributedChanges(worktreePath, baseline)
	if err != nil {
		return fmt.Errorf("check named-worker path policy at completion: %w", err)
	}
	store := workerstate.New(s.Config.HomeDir)
	namedState, err := store.Load(projectID, workerID)
	if err != nil {
		return err
	}
	violations, err := workerpathpolicy.Violations(namedState.EffectivePolicy, paths)
	if err != nil || len(violations) == 0 {
		return err
	}
	reason := fmt.Sprintf("path policy violation at completion: %d path(s); changes retained for review", len(violations))
	if namedState.Lifecycle == workerdomain.LifecycleExecuting {
		namedState, err = store.Transition(namedState, workerdomain.LifecycleBlocked, reason)
		if err != nil {
			return err
		}
	}
	if err := workerpathpolicy.AppendViolationEvent(s.Config.HomeDir, workerpathpolicy.Event{
		ProjectID: projectID, WorkerID: workerID, TaskID: taskID, RunID: worker.ID,
		Checkpoint: "completion", Violations: violations, Message: reason,
	}); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func boolValueOrUnknown(value *bool) string {
	if value == nil {
		return "unknown"
	}
	if *value {
		return "yes"
	}
	return "no"
}

func workerSummaryMarkdown(worker state.Worker, result state.EngineResult) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# PromptGrinder Worker Summary")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Worker: `%s`\n", worker.ID)
	fmt.Fprintf(&b, "- Task: `%s`\n", worker.TaskPath)
	fmt.Fprintf(&b, "- Status: `%s`\n", worker.Status)
	if result.SessionID != "" {
		fmt.Fprintf(&b, "- Codex session: `%s`\n", result.SessionID)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Codex Summary")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, result.Summary)
	return b.String()
}

func (s Service) Heartbeat(id string) error {
	_, err := s.Store.Heartbeat(id)
	return err
}

func (s Service) RunCodexWorker(recordPath, command string) (int, error) {
	return codex.ExecuteWorker(recordPath, command, os.Stdout, os.Stderr)
}

func engineResultEventData(result state.EngineResult) map[string]any {
	data := map[string]any{"reported_fields": []any{}}
	fields := []any{}
	if result.Summary != "" {
		fields = append(fields, "summary")
	}
	if result.SessionID != "" {
		fields = append(fields, "session_id")
	}
	if result.CompletionStatus != "" {
		fields = append(fields, "completion_status")
	}
	if result.NextPromptSafe != nil {
		fields = append(fields, "next_prompt_safe")
	}
	if result.CompletionReason != "" {
		fields = append(fields, "completion_reason")
	}
	if result.EngineExitCode != nil {
		fields = append(fields, "engine_exit_code")
	}
	if result.TokensInput != nil {
		fields = append(fields, "tokens_input")
	}
	if result.TokensOutput != nil {
		fields = append(fields, "tokens_output")
	}
	if result.TokensTotal != nil {
		fields = append(fields, "tokens_total")
	}
	if result.Cost != nil {
		fields = append(fields, "cost")
	}
	if result.CostCurrency != "" {
		fields = append(fields, "cost_currency")
	}
	if len(result.Diagnostics) > 0 {
		fields = append(fields, "diagnostics")
	}
	data["reported_fields"] = fields
	return data
}

func nonZeroWorkers(w state.Worker) []state.Worker {
	if w.ID == "" {
		return nil
	}
	return []state.Worker{w}
}

type promptLauncher struct {
	manager worker.Manager
}

func (l promptLauncher) LaunchPrompt(path, content, sessionID string) (state.Worker, error) {
	manager := l.manager
	manager.CodexSessionID = sessionID
	manager.CaptureCodexSession = true
	result := manager.LaunchContent(path, content)
	return result.Worker, result.Err
}

func (l promptLauncher) WaitPrompt(worker state.Worker) (state.Worker, error) {
	if worker.TerminalAdapter == "dry-run" {
		return worker, fmt.Errorf("run-folder requires workers to finish before continuing; dry-run workers do not execute")
	}
	for {
		latest, err := l.manager.Store.Load(worker.ID)
		if err != nil {
			return worker, err
		}
		if state.IsTerminalStatus(latest.Status) {
			return latest, nil
		}
		time.Sleep(2 * time.Second)
	}
}
