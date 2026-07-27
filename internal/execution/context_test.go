package execution

import (
	"path/filepath"
	"testing"

	"promptgrinder/internal/config"
	"promptgrinder/internal/state"
)

func TestNewContextConstructsExecutionContext(t *testing.T) {
	store := state.NewStore(t.TempDir())
	worker := state.Worker{
		ID:             "wrk_test",
		RecordPath:     store.RecordPath("wrk_test"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_test"), "prompt.md"),
		LogPath:        filepath.Join(store.WorkerDir("wrk_test"), "worker.log"),
		CloseOnFinish:  true,
		Metadata:       map[string]any{"label": "test", "working_directory": "backend"},
	}
	cfg := config.Config{Engine: "codex", TerminalAdapter: "terminal"}

	ctx, err := NewContext(worker, cfg, map[string]string{"A": "B"})
	if err != nil {
		t.Fatal(err)
	}

	if ctx.RepositoryRoot != "/repo" || ctx.TaskPath != "/repo/task.md" || ctx.WorkerID != "wrk_test" {
		t.Fatalf("context = %#v", ctx)
	}
	if ctx.WorkingDirectory != "/repo/backend" {
		t.Fatalf("WorkingDirectory = %q", ctx.WorkingDirectory)
	}
	if ctx.Directories.Script != filepath.Join(store.WorkerDir("wrk_test"), "run.sh") {
		t.Fatalf("script = %q", ctx.Directories.Script)
	}
	if ctx.Environment["A"] != "B" {
		t.Fatalf("environment = %#v", ctx.Environment)
	}
	if !ctx.CloseOnFinish || ctx.CloseOnFailure {
		t.Fatalf("close policy = finish:%t failure:%t", ctx.CloseOnFinish, ctx.CloseOnFailure)
	}
}

func TestNewContextRejectsAbsoluteWorkingDirectory(t *testing.T) {
	store := state.NewStore(t.TempDir())
	worker := state.Worker{
		ID:             "wrk_test",
		RecordPath:     store.RecordPath("wrk_test"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_test"), "prompt.md"),
		LogPath:        filepath.Join(store.WorkerDir("wrk_test"), "worker.log"),
		Metadata:       map[string]any{"working_directory": "/tmp/outside"},
	}

	if _, err := NewContext(worker, config.Config{}, nil); err == nil {
		t.Fatal("expected absolute working_directory to be rejected")
	}
}

func TestNewContextRejectsEscapingWorkingDirectory(t *testing.T) {
	store := state.NewStore(t.TempDir())
	worker := state.Worker{
		ID:             "wrk_test",
		RecordPath:     store.RecordPath("wrk_test"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_test"), "prompt.md"),
		LogPath:        filepath.Join(store.WorkerDir("wrk_test"), "worker.log"),
		Metadata:       map[string]any{"working_directory": "../outside"},
	}

	if _, err := NewContext(worker, config.Config{}, nil); err == nil {
		t.Fatal("expected escaping working_directory to be rejected")
	}
}

func TestTimeoutFromMetadata(t *testing.T) {
	timeout, err := TimeoutFromMetadata(map[string]any{"timeout": "10m"})
	if err != nil {
		t.Fatal(err)
	}
	if timeout.String() != "10m0s" {
		t.Fatalf("timeout = %s", timeout)
	}
	if _, err := TimeoutFromMetadata(map[string]any{"timeout": "bad"}); err == nil {
		t.Fatal("expected invalid timeout error")
	}
	if _, err := TimeoutFromMetadata(map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
