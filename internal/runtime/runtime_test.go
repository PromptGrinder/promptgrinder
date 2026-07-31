package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/engine/codex"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/runfolder"
	"promptgrinder/internal/state"
	"promptgrinder/internal/terminal"
	"promptgrinder/internal/testsupport"
	"promptgrinder/internal/worker"
)

type synchronousTerminal struct {
	store     state.Store
	name      string
	exitCodes []int
	mu        sync.Mutex
	launched  []string
}

func (t *synchronousTerminal) Name() string                     { return t.name }
func (t *synchronousTerminal) Command(scriptPath string) string { return scriptPath }
func (t *synchronousTerminal) Launch(scriptPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	workerID := filepath.Base(filepath.Dir(scriptPath))
	t.launched = append(t.launched, workerID)
	exitCode := 0
	if len(t.exitCodes) >= len(t.launched) {
		exitCode = t.exitCodes[len(t.launched)-1]
	}
	if err := t.store.MarkFinished(t.store.RecordPath(workerID), exitCode); err != nil {
		return err
	}
	if exitCode == 0 {
		worker, err := t.store.Load(workerID)
		if err != nil {
			return err
		}
		nextSafe := true
		worker.EngineResult = &state.EngineResult{Summary: "STATUS: PASS\nNEXT_PROMPT_SAFE: yes", CompletionStatus: "PASS", NextPromptSafe: &nextSafe}
		return t.store.Save(worker)
	}
	return nil
}

func (t *synchronousTerminal) launchCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.launched)
}

func TestRunFolderCreatesMultipleWorkers(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "b-task.md")
	writeTask(t, root, "a-task.md")
	writeFile(t, root, "notes.txt")
	writeTask(t, root, ".hidden.md")

	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			EngineName:  "codex",
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}

	summary, err := service.RunPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Workers) != 2 || len(summary.Failed) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	workers, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("workers = %#v", workers)
	}
}

func TestRunPathsPreflightRejectsMissingPathBeforeCreatingWorkers(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "a-task.md")
	writeTask(t, root, "a-task.md")
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{Store: store}

	_, err := service.RunPathsWithOptions([]string{valid, filepath.Join(root, "missing.md")}, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "task path does not exist") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("preflight created worker state: %v", err)
	}
}

func TestRunPathsDryRunRejectsEmptyPromptWithoutCreatingWorkers(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "a-task.md")
	writeFile(t, root, "b-empty.md", "   \n")
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := runtimeTestService(t, store)

	_, err := service.RunPathsWithOptions([]string{filepath.Join(root, "*.md")}, RunOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "task prompt is empty") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("dry run created worker state: %v", err)
	}
}

func TestRunPathsDryRunValidatesAndOrdersSharedPrompts(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "02B-task.md")
	writeTask(t, root, "02A-task.md")
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := runtimeTestService(t, store)

	summary, err := service.RunPathsWithOptions([]string{filepath.Join(root, "02[AB]-*.md")}, RunOptions{DryRun: true, SharedContext: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ValidatedPaths) != 2 || !strings.Contains(summary.ValidatedPaths[0], "02A-task.md") || len(summary.Workers) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("dry run created worker state: %v", err)
	}
}

func TestRunPathsRejectsPatternContainingNewline(t *testing.T) {
	service := Service{Store: state.NewStore(filepath.Join(t.TempDir(), "home"))}
	_, err := service.RunPathsWithOptions([]string{"docs/02A-[2-\n  6]-*.md"}, RunOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "contains a newline") {
		t.Fatalf("err = %v", err)
	}
}

func TestSharedGitCommitCreatesPromptCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFile(t, repo, "existing.txt", "before\n")
	runGit(t, repo, "add", "existing.txt")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	writeFile(t, repo, "feature.txt", "implemented\n")

	sha, err := commitSharedChanges(repo, "PromptGrinder: complete 01A-task.md")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		t.Fatal("missing checkpoint SHA")
	}
	clean, err := sharedGitClean(repo)
	if err != nil || !clean {
		t.Fatalf("clean = %t, err = %v", clean, err)
	}
	out := runGit(t, repo, "log", "-1", "--pretty=%s")
	if strings.TrimSpace(out) != "PromptGrinder: complete 01A-task.md" {
		t.Fatalf("commit subject = %q", out)
	}
}

func TestRollbackSharedChangesRestoresCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFile(t, repo, "existing.txt", "before\n")
	runGit(t, repo, "add", "existing.txt")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "checkpoint")
	checkpoint := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeFile(t, repo, "existing.txt", "interrupted\n")
	writeFile(t, repo, "new-file.txt", "partial\n")

	if err := rollbackSharedChanges(repo, checkpoint); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "existing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatalf("existing file = %q", data)
	}
	data, err = os.ReadFile(filepath.Join(repo, "new-file.txt"))
	if err != nil || string(data) != "partial\n" {
		t.Fatalf("untracked file was not preserved: data=%q err=%v", data, err)
	}
	clean, err := sharedGitClean(repo)
	if err != nil || clean {
		t.Fatalf("clean = %t, err = %v; preserved untracked file should keep tree dirty", clean, err)
	}
}

func TestSharedWorkerFailureIncludesCompletionDetails(t *testing.T) {
	nextSafe := false
	err := sharedWorkerFailure("/prompts/02B-1-task.md", state.Worker{
		ID:      "wrk_failed",
		Status:  state.StatusFailed,
		LogPath: "/logs/worker.log",
		EngineResult: &state.EngineResult{
			CompletionStatus: "BLOCKED",
			NextPromptSafe:   &nextSafe,
			Summary:          "STATUS: BLOCKED\n\nSUMMARY:\n- Required report is missing.\n- Stopped before editing.\n\nOPEN_ISSUES:\n- Run the prerequisite first.\n\nNEXT_PROMPT_SAFE: no",
		},
	})
	for _, want := range []string{
		"prompt failed: 02B-1-task.md",
		"Worker: wrk_failed",
		"Completion: STATUS=BLOCKED, NEXT_PROMPT_SAFE=no",
		"Reason: Required report is missing. Stopped before editing.",
		"Next action: Run the prerequisite first.",
		"Log: /logs/worker.log",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err, want)
		}
	}
}

func TestSharedPromptWithHandoffAppendsPrerequisiteEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "02A-3.md", "---\nworking_directory: backend\n---\n# Active task\n")
	report := "STATUS: PASS\nSUMMARY:\n- Contract completed.\nNEXT_PROMPT_SAFE: yes"

	content, err := sharedPromptWithHandoff(filepath.Join(dir, "02A-3.md"), "02A-2.md", report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"working_directory: backend", "# Active task", "Previous prompt: 02A-2.md", report} {
		if !strings.Contains(content, want) {
			t.Fatalf("content = %q, missing %q", content, want)
		}
	}
}

func TestRunFileEngineOverrideTakesPrecedence(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "task.md", "---\nengine:\n  name: missing\n---\n# Task\n")

	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "other-default", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}

	summary, err := service.RunPathWithOptions(filepath.Join(root, "task.md"), RunOptions{EngineOverride: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Workers) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	loaded, err := store.Load(summary.Workers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Engine != "codex" || loaded.RequestedEngine != "missing" || loaded.EngineOverride != "codex" || !loaded.EngineOverridden {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestRunFolderEngineOverrideTakesPrecedenceForEveryTask(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a-task.md", "---\nengine:\n  name: missing\n---\n# Task A\n")
	writeFile(t, root, "b-task.md", "---\nengine:\n  name: missing\n---\n# Task B\n")

	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "other-default", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}

	summary, err := service.RunPathWithOptions(root, RunOptions{EngineOverride: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Workers) != 2 || len(summary.Failed) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	for _, created := range summary.Workers {
		loaded, err := store.Load(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Engine != "codex" || loaded.RequestedEngine != "missing" || loaded.EngineOverride != "codex" || !loaded.EngineOverridden {
			t.Fatalf("loaded = %#v", loaded)
		}
	}
}

func TestRunPathUnknownEngineOverrideFailsBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "task.md")

	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:      store,
			Engine:     codex.Engine{},
			EngineName: "codex",
			Executable: "promptgrinder",
			BaseConfig: config.Config{Engine: "codex", TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: func(config.Config) (execution.Executor, error) {
				t.Fatal("unknown override must fail before launch")
				return execution.Executor{}, nil
			},
		},
	}

	summary, err := service.RunPathWithOptions(filepath.Join(root, "task.md"), RunOptions{EngineOverride: "missing"})
	if err == nil {
		t.Fatal("expected unknown engine error")
	}
	if len(summary.Workers) != 0 || len(summary.Failed) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("worker state was created at %s (err=%v)", store.WorkersDir, err)
	}
}

func TestRunPromptFolderEngineOverrideTakesPrecedence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writeFile(t, root, "10-implement-a.md", "---\nengine:\n  name: missing\n---\n# Task\n")

	store := state.NewStore(home)
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "other-default", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}

	_, err := service.RunPromptFolder(root, RunFolderOptions{HomeDir: home, EngineOverride: "codex"})
	if err == nil || !strings.Contains(err.Error(), "dry-run workers do not execute") {
		t.Fatalf("err = %v", err)
	}
	workers, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %#v", workers)
	}
	if workers[0].Engine != "codex" || workers[0].RequestedEngine != "missing" || workers[0].EngineOverride != "codex" || !workers[0].EngineOverridden {
		t.Fatalf("worker = %#v", workers[0])
	}
}

func TestRunPromptFolderUsesRepoPathForWorkerRepository(t *testing.T) {
	promptDir := t.TempDir()
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writeFile(t, promptDir, "10-implement-a.md", "# Task\n")
	writeFile(t, repo, ".git", "")

	store := state.NewStore(home)
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}

	_, err := service.RunPromptFolder(promptDir, RunFolderOptions{HomeDir: home, RepoPath: repo, EngineOverride: "codex"})
	if err == nil || !strings.Contains(err.Error(), "dry-run workers do not execute") {
		t.Fatalf("err = %v", err)
	}
	workers, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %#v", workers)
	}
	if workers[0].RepositoryPath != repo {
		t.Fatalf("RepositoryPath = %q, want %q", workers[0].RepositoryPath, repo)
	}
	if workers[0].TaskPath != filepath.Join(promptDir, "10-implement-a.md") {
		t.Fatalf("TaskPath = %q", workers[0].TaskPath)
	}
}

func TestRunPromptFolderForegroundForcesHeadlessAfterRepoConfig(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	fakeCodex := testsupport.FakeCodex(t)
	writeFile(t, repo, ".git", "gitdir: nowhere\n")
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(repo, ".ai", "config.yaml")
	configBytes := []byte("engine:\n  codex:\n    executable: " + strconv.Quote(fakeCodex) + "\nterminal:\n  adapter: iterm\n  mode: normal\n")
	if err := os.WriteFile(configPath, configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "10-implement-first.md", "# First\n")
	writeFile(t, repo, "20-test-second.md", "# Second\n")

	store := state.NewStore(home)
	headless := &synchronousTerminal{store: store, name: "headless"}
	configuredLaunches := 0
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:         store,
			Engine:        codex.Engine{},
			EngineName:    "codex",
			Executable:    "promptgrinder",
			UseRepoConfig: true,
			BaseConfig:    config.Config{Engine: "codex", CodexExecutable: fakeCodex, TerminalAdapter: "terminal", TerminalMode: "normal", HomeDir: home},
			NewExecutor: func(cfg config.Config) (execution.Executor, error) {
				configuredLaunches++
				if cfg.TerminalAdapter != "headless" || cfg.TerminalMode != "normal" {
					t.Fatalf("executor config = %s/%s", cfg.TerminalAdapter, cfg.TerminalMode)
				}
				return execution.Executor{Store: store, Terminal: &synchronousTerminal{store: store, name: cfg.TerminalAdapter}}, nil
			},
			SelectTerminalAdapter: func(adapter, mode string) (terminal.TerminalAdapter, error) {
				if adapter != "headless" || mode != "normal" {
					t.Fatalf("foreground selection = %s/%s", adapter, mode)
				}
				return headless, nil
			},
		},
	}

	summary, err := service.RunPromptFolder(repo, RunFolderOptions{
		HomeDir:         home,
		RepoPath:        repo,
		ExecutionPolicy: RunFolderExecutionForeground,
	})
	if err != nil {
		t.Fatal(err)
	}
	if configuredLaunches != 2 || headless.launchCount() != 2 {
		t.Fatalf("executor calls = %d, headless launches = %d", configuredLaunches, headless.launchCount())
	}
	if summary.Sequence == nil || summary.Sequence.Status != "completed" || len(summary.Sequence.Items) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	for _, item := range summary.Sequence.Items {
		if item.Status != "succeeded" {
			t.Fatalf("item = %#v", item)
		}
	}
	workers, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 || workers[0].TerminalAdapter != "headless" || workers[1].TerminalAdapter != "headless" {
		t.Fatalf("workers = %#v", workers)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(configBytes) {
		t.Fatalf("repository config changed:\n%s", after)
	}
}

func TestRunPromptFolderConfiguredPolicyAndNormalRunRetainAdapter(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writeFile(t, repo, ".git", "gitdir: nowhere\n")
	writeFile(t, repo, "10-implement-folder.md", "# Folder\n")
	writeFile(t, repo, "normal.md", "# Normal\n")
	store := state.NewStore(home)
	configured := &synchronousTerminal{store: store, name: "iterm"}
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store: store, Engine: codex.Engine{}, EngineName: "codex", Executable: "promptgrinder",
			RepositoryOverride: repo,
			BaseConfig:         config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "iterm", TerminalMode: "normal", HomeDir: home},
			NewExecutor:        runtimeTestExecutorFactory(store, configured),
		},
	}

	if _, err := service.RunPromptFolder(repo, RunFolderOptions{HomeDir: home, RepoPath: repo, Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPath(filepath.Join(repo, "normal.md")); err != nil {
		t.Fatal(err)
	}
	if configured.launchCount() != 2 {
		t.Fatalf("configured launches = %d, want 2", configured.launchCount())
	}
}

func TestRunPromptFolderForegroundFailureStopsAndPersistsResumeState(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writeFile(t, repo, ".git", "gitdir: nowhere\n")
	writeFile(t, repo, "10-implement-first.md", "# First\n")
	writeFile(t, repo, "20-test-fails.md", "# Fails\n")
	writeFile(t, repo, "30-verify-never.md", "# Never\n")
	store := state.NewStore(home)
	headless := &synchronousTerminal{store: store, name: "headless", exitCodes: []int{0, 1, 0}}
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store: store, Engine: codex.Engine{}, EngineName: "codex", Executable: "promptgrinder",
			BaseConfig:            config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "normal", HomeDir: home},
			NewExecutor:           runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
			SelectTerminalAdapter: func(string, string) (terminal.TerminalAdapter, error) { return headless, nil },
		},
	}

	summary, err := service.RunPromptFolder(repo, RunFolderOptions{HomeDir: home, RepoPath: repo, ExecutionPolicy: RunFolderExecutionForeground})
	if err == nil {
		t.Fatal("expected prompt failure")
	}
	if headless.launchCount() != 2 {
		t.Fatalf("launches = %d, want exactly two before stop", headless.launchCount())
	}
	if summary.Sequence == nil || summary.Sequence.Status != "failed" {
		t.Fatalf("sequence = %#v", summary.Sequence)
	}
	if summary.Sequence.Items[0].Status != "succeeded" || summary.Sequence.Items[1].Status != "failed" || summary.Sequence.Items[2].Status != "pending" {
		t.Fatalf("items = %#v", summary.Sequence.Items)
	}
	persisted, loadErr := runfolder.LoadSequence(home, summary.Sequence.SequenceID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Status != "failed" || persisted.Items[1].Status != "failed" || persisted.Items[2].Status != "pending" {
		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestRunFolderContinuesAfterLaunchFailure(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "a-task.md")
	writeTask(t, root, "b-task.md")

	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	service := Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			EngineName:  "codex",
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, &failFirstTerminal{}),
		},
	}

	summary, err := service.RunPath(root)
	if err == nil {
		t.Fatal("expected partial failure")
	}
	if len(summary.Workers) != 2 || len(summary.Failed) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	workers, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("workers = %#v", workers)
	}
}

func TestLogsUnknownWorkerReturnsClearError(t *testing.T) {
	service := Service{Store: state.NewStore(filepath.Join(t.TempDir(), "home"))}

	_, err := service.Logs("missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "worker not found: missing" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestFinishWorkerPersistsParsedEngineResult(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store := state.NewStore(home)
	worker := state.Worker{
		ID:             "wrk_parse",
		RecordPath:     store.RecordPath("wrk_parse"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_parse"), "prompt.md"),
		Engine:         "parse-test",
		Status:         state.StatusRunning,
		LogPath:        filepath.Join(store.WorkerDir("wrk_parse"), "worker.log"),
		Metadata:       map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker.LogPath, []byte("tokens: reported by fake adapter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Store:    store,
		Registry: engine.NewRegistry(parseTestEngine{}),
	}

	if err := service.FinishWorker(worker.RecordPath, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EngineResult == nil || loaded.EngineResult.TokensTotal == nil || *loaded.EngineResult.TokensTotal != 42 || loaded.EngineResult.Cost != nil {
		t.Fatalf("engine_result = %#v", loaded.EngineResult)
	}
	summaryData, err := os.ReadFile(loaded.SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summaryData), "## Codex Summary") || !strings.Contains(string(summaryData), "parsed") {
		t.Fatalf("summary = %q", string(summaryData))
	}
	events, err := store.ReadEvents(worker.ID, state.EventFilter{Type: state.EventEngineResultParsed})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Engine != "parse-test" {
		t.Fatalf("result parsed events = %#v", events.Events)
	}
}

func TestFinishWorkerRejectsBlockedCodexCompletion(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store := state.NewStore(home)
	worker := state.Worker{
		ID:             "wrk_blocked",
		RecordPath:     store.RecordPath("wrk_blocked"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_blocked"), "prompt.md"),
		Engine:         "codex",
		Status:         state.StatusRunning,
		LogPath:        filepath.Join(store.WorkerDir("wrk_blocked"), "worker.log"),
		Metadata:       map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	log := "{\"type\":\"thread.started\",\"thread_id\":\"thread-blocked\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"STATUS: BLOCKED\\nCHANGED_FILES:\\n- none\\nNEXT_PROMPT_SAFE: no\"}}\n"
	if err := os.WriteFile(worker.LogPath, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store, Registry: engine.NewRegistry(codex.Engine{})}

	err := service.FinishWorker(worker.RecordPath, 0)
	if err == nil || !strings.Contains(err.Error(), "non-continuable result") {
		t.Fatalf("err = %v", err)
	}
	loaded, err := store.Load(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != state.StatusFailed || loaded.ExitCode == nil || *loaded.ExitCode == 0 {
		t.Fatalf("worker = %#v", loaded)
	}
	if loaded.EngineResult == nil || loaded.EngineResult.CompletionStatus != "BLOCKED" {
		t.Fatalf("engine result = %#v", loaded.EngineResult)
	}
}

func TestFinishWorkerRejectsSuccessfulExitWithoutFinalCodexMessage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store := state.NewStore(home)
	worker := state.Worker{
		ID: "wrk_partial", RecordPath: store.RecordPath("wrk_partial"),
		RepositoryPath: "/repo", TaskPath: "/repo/task.md",
		PromptPath: filepath.Join(store.WorkerDir("wrk_partial"), "prompt.md"),
		Engine:     "codex", Status: state.StatusRunning,
		LogPath:  filepath.Join(store.WorkerDir("wrk_partial"), "worker.log"),
		Metadata: map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex.CapturedOutputPath(worker.RecordPath), []byte("{\"type\":\"thread.started\",\"thread_id\":\"partial\"}\n{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store, Registry: engine.NewRegistry(codex.Engine{})}
	err := service.FinishWorker(worker.RecordPath, 0)
	if err == nil || !strings.Contains(err.Error(), "no parseable final message") {
		t.Fatalf("err = %v", err)
	}
	loaded, loadErr := store.Load(worker.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.Status != state.StatusFailed || loaded.ExitCode == nil || *loaded.ExitCode == 0 {
		t.Fatalf("worker = %#v", loaded)
	}
}

func TestFinishWorkerPrefersCompleteCodexCaptureOverUnflushedLog(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store := state.NewStore(home)
	worker := state.Worker{
		ID:             "wrk_capture",
		RecordPath:     store.RecordPath("wrk_capture"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_capture"), "prompt.md"),
		Engine:         "codex",
		Status:         state.StatusRunning,
		LogPath:        filepath.Join(store.WorkerDir("wrk_capture"), "worker.log"),
		Metadata:       map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	opening := "{\"type\":\"thread.started\",\"thread_id\":\"thread-capture\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Starting work.\"}}\n"
	if err := os.WriteFile(worker.LogPath, []byte(opening), 0o644); err != nil {
		t.Fatal(err)
	}
	complete := opening + "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"STATUS: PASS\\nSUMMARY:\\n- Finished.\\nNEXT_PROMPT_SAFE: yes\"}}\n"
	if err := os.WriteFile(codex.CapturedOutputPath(worker.RecordPath), []byte(complete), 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store, Registry: engine.NewRegistry(codex.Engine{})}

	if err := service.FinishWorker(worker.RecordPath, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EngineResult == nil || loaded.EngineResult.CompletionStatus != "PASS" || !strings.Contains(loaded.EngineResult.Summary, "Finished.") {
		t.Fatalf("engine result = %#v", loaded.EngineResult)
	}
}

func TestTerminalCandidatesSkipClosedWorkers(t *testing.T) {
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	open := state.Worker{
		ID:              "wrk_open",
		RecordPath:      store.RecordPath("wrk_open"),
		TaskPath:        "/repo/open.md",
		TerminalAdapter: "terminal",
		Status:          state.StatusSucceeded,
		Metadata:        map[string]any{},
	}
	if err := store.Save(open); err != nil {
		t.Fatal(err)
	}
	closed := state.Worker{
		ID:              "wrk_closed",
		RecordPath:      store.RecordPath("wrk_closed"),
		TaskPath:        "/repo/closed.md",
		TerminalAdapter: "terminal",
		Status:          state.StatusSucceeded,
		Metadata:        map[string]any{},
	}
	if err := store.Save(closed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkTerminalClosed(closed.ID); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store}
	candidates, err := service.TerminalCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].WorkerID != open.ID {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestCloseTerminalsMarksWorkersClosed(t *testing.T) {
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	worker := state.Worker{
		ID:              "wrk_close",
		RecordPath:      store.RecordPath("wrk_close"),
		TaskPath:        "/repo/task.md",
		TerminalAdapter: "terminal",
		Status:          state.StatusSucceeded,
		Metadata:        map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	var gotTitles []string
	originalClose := terminal.CloseTerminalTabs
	terminal.CloseTerminalTabs = func(titles []string) error {
		gotTitles = append([]string{}, titles...)
		return nil
	}
	defer func() { terminal.CloseTerminalTabs = originalClose }()

	service := Service{Store: store}
	closed, err := service.CloseTerminals([]string{worker.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0].WorkerID != worker.ID {
		t.Fatalf("closed = %#v", closed)
	}
	if len(gotTitles) != 1 || gotTitles[0] != "PromptGrinder: wrk_close" {
		t.Fatalf("titles = %#v", gotTitles)
	}
	loaded, err := store.Load(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TerminalClosedAt == nil || loaded.Status != state.StatusSucceeded {
		t.Fatalf("loaded = %#v", loaded)
	}
	candidates, err := service.TerminalCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestCloseAllTerminalsIncludesITermWildcard(t *testing.T) {
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	var gotTitles []string
	originalClose := terminal.CloseTerminalTabs
	terminal.CloseTerminalTabs = func(titles []string) error {
		gotTitles = append([]string{}, titles...)
		return nil
	}
	defer func() { terminal.CloseTerminalTabs = originalClose }()

	service := Service{Store: store}
	if _, err := service.CloseTerminals(nil); err != nil {
		t.Fatal(err)
	}
	if len(gotTitles) != 1 || gotTitles[0] != "__PROMPTGRINDER_ALL__" {
		t.Fatalf("titles = %#v", gotTitles)
	}
}

type failFirstTerminal struct {
	calls int
}

func (f *failFirstTerminal) Name() string {
	return "fail-first"
}

func (f *failFirstTerminal) Command(scriptPath string) string {
	return "/bin/zsh " + scriptPath
}

func (f *failFirstTerminal) Launch(scriptPath string) error {
	f.calls++
	if f.calls == 1 {
		return errors.New("failed")
	}
	return nil
}

type parseTestEngine struct{}

func (parseTestEngine) Name() string {
	return "parse-test"
}

func (parseTestEngine) Describe() engine.Descriptor {
	return engine.Descriptor{Name: "parse-test", Description: "Parse test engine"}
}

func (parseTestEngine) Validate(ctx execution.Context) error {
	return nil
}

func (parseTestEngine) Build(ctx execution.Context, prompt []byte, executablePath string) (execution.Request, error) {
	return execution.Request{}, nil
}

func (parseTestEngine) ParseResult(ctx execution.Context, log []byte) state.EngineResult {
	tokens := int64(42)
	return state.EngineResult{
		Summary:     "parsed",
		SessionID:   "session-parse",
		TokensTotal: &tokens,
	}
}

func writeTask(t *testing.T, root, name string) {
	t.Helper()
	writeFile(t, root, name)
}

func writeFile(t *testing.T, root, name string, content ...string) {
	t.Helper()
	path := filepath.Join(root, name)
	text := "# Task\n"
	if len(content) > 0 {
		text = content[0]
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", command...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func runtimeTestExecutorFactory(store state.Store, adapter terminal.TerminalAdapter) worker.ExecutorFactory {
	return func(cfg config.Config) (execution.Executor, error) {
		return execution.Executor{Store: store, Terminal: adapter}, nil
	}
}

func runtimeTestService(t *testing.T, store state.Store) Service {
	t.Helper()
	return Service{
		Store: store,
		Worker: worker.Manager{
			Store:       store,
			Engine:      codex.Engine{},
			EngineName:  "codex",
			Executable:  "promptgrinder",
			BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
			NewExecutor: runtimeTestExecutorFactory(store, terminal.DryRunAdapter{}),
		},
	}
}
