package execution

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"promptgrinder/internal/config"
	"promptgrinder/internal/state"
)

func TestExecutorWritesFilesAndMarksStarted(t *testing.T) {
	store := state.NewStore(t.TempDir())
	worker := testExecutionWorker(store)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	ctx, err := NewContext(worker, config.Config{Engine: "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := Executor{Store: store, Terminal: recordingAdapter{}}

	result, err := executor.Execute(Request{
		Context: ctx,
		Worker:  worker,
		Prompt:  []byte("# Task\n"),
		Script:  "#!/bin/zsh\n",
		CommandData: map[string]any{
			"command_preview":   "codex exec",
			"sandbox":           "workspace-write",
			"approval":          "never",
			"working_directory": "/repo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Worker.Status != state.StatusRunning {
		t.Fatalf("status = %q", result.Worker.Status)
	}
	if _, err := os.Stat(ctx.Directories.Prompt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ctx.Directories.Script); err != nil {
		t.Fatal(err)
	}
	if result.Worker.TerminalAdapter != "recording" {
		t.Fatalf("terminal adapter = %q", result.Worker.TerminalAdapter)
	}
	events, err := store.ReadEvents(worker.ID, state.EventFilter{Type: state.EventEngineCommandBuilt})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Data["sandbox"] != "workspace-write" {
		t.Fatalf("command event = %#v", events.Events)
	}
}

func TestExecutorMarksLaunchFailed(t *testing.T) {
	store := state.NewStore(t.TempDir())
	worker := testExecutionWorker(store)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	ctx, err := NewContext(worker, config.Config{Engine: "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := Executor{Store: store, Terminal: failingAdapter{}}

	result, err := executor.Execute(Request{Context: ctx, Worker: worker, Prompt: []byte("# Task\n"), Script: "#!/bin/zsh\n"})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Worker.Status != state.StatusLaunchFailed {
		t.Fatalf("status = %q", result.Worker.Status)
	}
	events, readErr := store.ReadEvents(worker.ID, state.EventFilter{Type: state.EventTerminalLaunchFailed})
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events.Events) != 1 || events.Events[0].Data["adapter"] != "failing" {
		t.Fatalf("launch failed event = %#v", events.Events)
	}
}

type recordingAdapter struct{}

func (recordingAdapter) Name() string {
	return "recording"
}

func (recordingAdapter) Command(scriptPath string) string {
	return "/bin/zsh " + scriptPath
}

func (recordingAdapter) Launch(scriptPath string) error {
	return nil
}

type failingAdapter struct{}

func (failingAdapter) Name() string {
	return "failing"
}

func (failingAdapter) Command(scriptPath string) string {
	return "/bin/zsh " + scriptPath
}

func (failingAdapter) Launch(scriptPath string) error {
	return errors.New("launch failed")
}

func testExecutionWorker(store state.Store) state.Worker {
	return state.Worker{
		ID:             "wrk_test",
		RecordPath:     store.RecordPath("wrk_test"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_test"), "prompt.md"),
		Engine:         "codex",
		Status:         state.StatusCreated,
		LogPath:        filepath.Join(store.WorkerDir("wrk_test"), "worker.log"),
		Metadata:       map[string]any{},
	}
}
