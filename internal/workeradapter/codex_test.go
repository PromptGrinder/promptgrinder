package workeradapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/engine/codex"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/state"
	"promptgrinder/internal/testsupport"
	"promptgrinder/internal/worker"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
)

type captureTerminal struct {
	executable string
}

func (t captureTerminal) Name() string                     { return "capture-executable" }
func (t captureTerminal) Command(scriptPath string) string { return scriptPath }
func (t captureTerminal) Launch(scriptPath string) error {
	promptPath := filepath.Join(filepath.Dir(scriptPath), "prompt.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return err
	}
	return exec.Command(t.executable, string(prompt)).Run()
}

func TestCodexAdapterFakeExecutableReceivesExactContext(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "context.txt")
	fake := testsupport.FakeExecutable(t, "codex", "#!/bin/sh\nprintf '%s' \"$1\" > \""+capture+"\"\n")
	cfg := config.Config{
		HomeDir: home, Engine: "codex", CodexExecutable: fake,
		CodexSandbox: "workspace-write", CodexApproval: "never",
	}
	store := state.NewStore(home)
	manager := worker.Manager{
		Store: store, Registry: engine.NewRegistry(codex.Engine{Command: fake}),
		EngineName: "codex", Executable: "/bin/true", BaseConfig: cfg,
		NewExecutor: func(config.Config) (execution.Executor, error) {
			return execution.Executor{Store: store, Terminal: captureTerminal{executable: fake}}, nil
		},
	}
	request := workerlaunch.LaunchRequest{
		Project: workerdomain.Project{ID: "example", Name: "Example"},
		Worker: workerdomain.WorkerDefinition{
			ID: "backend-sonar", DisplayName: "Backend Sonar", ProjectID: "example",
			Role: "Fix backend findings.", Runtime: workerdomain.RuntimeRef{Name: "codex"},
			Policy: workerdomain.WorkerPolicy{
				BranchPrefix: "worker/backend-sonar", DefaultWorktree: ".",
				AllowedPaths: []string{"backend/**"}, ForbiddenPaths: []string{"secrets/**"},
			},
		},
		Repository: repo, Worktree: repo,
		Task:    workerlaunch.TaskContext{ID: "sonar-001", Source: "tasks/sonar-001.md", Instructions: "Fix it."},
		Runtime: workerlaunch.RuntimeConfig{Name: "codex"},
	}
	request.Policy = request.Worker.Policy
	request.Context = workerlaunch.ContextDocument(request)
	adapter := Codex{Manager: manager}
	if err := adapter.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Launch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" {
		t.Fatal("adapter did not persist an execution run reference")
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != request.Context {
		t.Fatalf("fake executable context mismatch\nwant:\n%s\ngot:\n%s", request.Context, got)
	}
	run, err := store.Load(result.RunID)
	if err != nil || run.TaskPath != filepath.Join(repo, "tasks", "sonar-001.md") {
		t.Fatalf("run record = %#v, %v", run, err)
	}
}
