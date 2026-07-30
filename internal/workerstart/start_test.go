package workerstart

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
)

type captureLauncher struct {
	preflightErr error
	launchErr    error
	calls        int
	request      workerlaunch.LaunchRequest
	result       workerlaunch.LaunchResult
}

func (l *captureLauncher) Preflight(_ context.Context, request workerlaunch.LaunchRequest) error {
	l.request = request
	return l.preflightErr
}

func (l *captureLauncher) Launch(_ context.Context, request workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	l.calls++
	l.request = request
	return l.result, l.launchErr
}

func TestStartRestoresExactContextAndPersistsReferences(t *testing.T) {
	repo, home := assignedProject(t)
	launcher := &captureLauncher{result: workerlaunch.LaunchResult{
		RuntimeName: "codex", RunID: "wrk_run-one",
		Session: workerlaunch.RuntimeSessionResult{ID: "session-one"},
	}}
	result, err := (Service{
		Home: home, Config: config.Config{},
		Launchers: map[string]workerlaunch.Launcher{"codex": launcher},
	}).Start(context.Background(), filepath.Join(repo, "backend"), "backend-sonar", "")
	if err != nil {
		t.Fatal(err)
	}
	if launcher.calls != 1 || result.State.Lifecycle != workerdomain.LifecycleExecuting {
		t.Fatalf("calls/state = %d/%s", launcher.calls, result.State.Lifecycle)
	}
	if result.State.ActiveRunID != "wrk_run-one" || result.State.RuntimeSession == nil || result.State.RuntimeSession.SessionID != "session-one" {
		t.Fatalf("references not persisted: %#v", result.State)
	}
	if result.State.Worktree != repo || result.State.Branch != "worker/backend-sonar/sonar-001" ||
		result.State.BaseBranch != "main" || result.State.BaseRevision == "" {
		t.Fatalf("worker Git selection not persisted: %#v", result.State)
	}
	task, err := taskstore.New(home).Load("example", "sonar-001")
	if err != nil {
		t.Fatal(err)
	}
	if task.LaunchSetup != "prepared" || task.Worktree != repo || task.Branch != result.State.Branch ||
		task.BaseBranch != result.State.BaseBranch || task.BaseRevision != result.State.BaseRevision {
		t.Fatalf("task Git selection not persisted: %#v", task)
	}
	want := "# PromptGrinder Worker Context\n\nProject: Example (example)\nWorker: Backend Sonar (backend-sonar)\nResponsibility: Fix backend findings.\nRepository: " + repo + "\nWorktree: " + repo + "\nBranch: worker/backend-sonar/sonar-001\nBase branch: main\nBase revision: " + result.Request.BaseRevision + "\nAllowed paths: backend/**\nForbidden paths: secrets/**\nAssigned task: sonar-001\nTask source: tasks/sonar-001.md\nTask instructions: # Fix sonar\n\nDo the exact fix.\n\n"
	if launcher.request.Context != want {
		t.Fatalf("injected context mismatch\nwant:\n%s\ngot:\n%s", want, launcher.request.Context)
	}
}

func TestStartPreflightFailureLaunchesNothing(t *testing.T) {
	repo, home := assignedProject(t)
	launcher := &captureLauncher{preflightErr: errors.New("executable unavailable")}
	_, err := (Service{Home: home, Launchers: map[string]workerlaunch.Launcher{"codex": launcher}}).
		Start(context.Background(), repo, "backend-sonar", "")
	if err == nil || !strings.Contains(err.Error(), "preflight") || launcher.calls != 0 {
		t.Fatalf("err/calls = %v/%d", err, launcher.calls)
	}
	state, loadErr := workerstate.New(home).Load("example", "backend-sonar")
	if loadErr != nil || state.Lifecycle != workerdomain.LifecycleIdle {
		t.Fatalf("state changed on preflight failure: %#v, %v", state, loadErr)
	}
}

func TestStartLaunchFailurePersistsFailedStateAndRun(t *testing.T) {
	repo, home := assignedProject(t)
	launcher := &captureLauncher{
		launchErr: errors.New("terminal refused launch"),
		result:    workerlaunch.LaunchResult{RuntimeName: "codex", RunID: "wrk_failed"},
	}
	result, err := (Service{Home: home, Launchers: map[string]workerlaunch.Launcher{"codex": launcher}}).
		Start(context.Background(), repo, "backend-sonar", "")
	if err == nil || result.State.Lifecycle != workerdomain.LifecycleFailed {
		t.Fatalf("result/error = %#v / %v", result, err)
	}
	if result.State.ActiveRunID != "wrk_failed" || !strings.Contains(result.State.FailureReason, "terminal refused launch") {
		t.Fatalf("failed state lacks precise references: %#v", result.State)
	}
}

func TestStartRejectsRestartWhileExecuting(t *testing.T) {
	repo, home := assignedProject(t)
	store := workerstate.New(home)
	state, _ := store.Load("example", "backend-sonar")
	state, _ = store.Transition(state, workerdomain.LifecycleStarting, "")
	state.ActiveRunID = "wrk_active"
	if _, err := store.Transition(state, workerdomain.LifecycleExecuting, ""); err != nil {
		t.Fatal(err)
	}
	launcher := &captureLauncher{}
	_, err := (Service{Home: home, Launchers: map[string]workerlaunch.Launcher{"codex": launcher}}).
		Start(context.Background(), repo, "backend-sonar", "")
	if err == nil || !strings.Contains(err.Error(), `lifecycle "executing"`) || launcher.calls != 0 {
		t.Fatalf("err/calls = %v/%d", err, launcher.calls)
	}
}

type violatingLauncher struct{}

func (violatingLauncher) Launch(_ context.Context, request workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	if err := os.MkdirAll(filepath.Join(request.Worktree, "secrets"), 0o755); err != nil {
		return workerlaunch.LaunchResult{}, err
	}
	if err := os.WriteFile(filepath.Join(request.Worktree, "secrets", "key.txt"), []byte("retain me"), 0o600); err != nil {
		return workerlaunch.LaunchResult{}, err
	}
	now := time.Now().UTC()
	exit := 0
	return workerlaunch.LaunchResult{
		RuntimeName: "codex", RunID: "wrk_violation",
		Process: workerlaunch.RuntimeProcessResult{FinishedAt: &now, ExitCode: &exit},
	}, nil
}

func TestStartCompletionViolationBlocksEmitsEventAndRetainsChanges(t *testing.T) {
	repo, home := assignedProject(t)
	result, err := (Service{Home: home, Launchers: map[string]workerlaunch.Launcher{"codex": violatingLauncher{}}}).
		Start(context.Background(), repo, "backend-sonar", "")
	if err == nil || result.State.Lifecycle != workerdomain.LifecycleBlocked {
		t.Fatalf("result/error = %#v / %v", result, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(repo, "secrets", "key.txt")); readErr != nil || string(data) != "retain me" {
		t.Fatalf("violating change not retained: %q, %v", data, readErr)
	}
	eventData, readErr := os.ReadFile(filepath.Join(home, "projects", "example", "workers", "backend-sonar", "events.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	event := string(eventData)
	for _, want := range []string{`"type":"worker.path_policy_violated"`, `"checkpoint":"completion"`, `"path":"secrets/key.txt"`, `"rule":"secrets/**"`} {
		if !strings.Contains(event, want) {
			t.Fatalf("event missing %s: %s", want, event)
		}
	}
}

func assignedProject(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	for _, dir := range []string{".ai", "backend", "tasks"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registryData := `version: 1
project: {id: example, name: Example}
workers:
  backend-sonar:
    display_name: Backend Sonar
    role: Fix backend findings.
    runtime: codex
    branch: {prefix: worker/backend-sonar}
    worktree: {default: .}
    paths:
      allowed: [backend/**]
      forbidden: [secrets/**]
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "workers.yaml"), []byte(registryData), 0o644); err != nil {
		t.Fatal(err)
	}
	taskData := "# Fix sonar\n\nDo the exact fix.\n"
	if err := os.WriteFile(filepath.Join(repo, "tasks", "sonar-001.md"), []byte(taskData), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"}, {"config", "user.email", "test@example.com"},
		{"config", "user.name", "PromptGrinder Test"}, {"add", "."},
		{"commit", "-m", "initial"},
	} {
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	registry, err := workerregistry.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Get("backend-sonar")
	if _, err := taskstore.New(home).Assign(repo, definition, "tasks/sonar-001.md"); err != nil {
		t.Fatal(err)
	}
	return repo, home
}
