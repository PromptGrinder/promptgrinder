package workercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerstate"
)

type fakeProcess struct {
	forced bool
	err    error
	pid    int
}

func (p *fakeProcess) Stop(_ context.Context, pid int, _ time.Duration) (bool, error) {
	p.pid = pid
	return p.forced, p.err
}

type fakeResumer struct{ supported bool }

func (fakeResumer) Launch(context.Context, workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	return workerlaunch.LaunchResult{}, nil
}
func (f fakeResumer) Capabilities() workerlaunch.Capabilities {
	return workerlaunch.Capabilities{SessionResume: f.supported}
}
func (fakeResumer) Resume(context.Context, workerlaunch.LaunchRequest, string) (workerlaunch.LaunchResult, error) {
	now := time.Now()
	return workerlaunch.LaunchResult{RuntimeName: "test", RunID: "run-resumed", Session: workerlaunch.RuntimeSessionResult{ID: "session-one"}, Process: workerlaunch.RuntimeProcessResult{PID: 42, StartedAt: &now}}, nil
}

func TestPauseTransitionsIdempotentlyAndRetainsAttempt(t *testing.T) {
	home, state, task := fixture(t, workerdomain.LifecycleExecuting, workerdomain.TaskStatusAssigned)
	process := &fakeProcess{forced: true}
	service := Service{Home: home, Processes: process, Timeout: time.Millisecond}
	result, err := service.Pause(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Lifecycle != workerdomain.LifecyclePaused || !result.Forced || process.pid != 123 {
		t.Fatalf("unexpected pause: %#v pid=%d", result, process.pid)
	}
	if len(result.Task.Attempts) != 1 || result.Task.Attempts[0].Disposition != "pause_forced" || result.Task.Status != workerdomain.TaskStatusPaused {
		t.Fatalf("attempt evidence overwritten: %#v", result.Task)
	}
	again, err := service.Pause(context.Background(), result.State)
	if err != nil || !again.Idempotent || len(again.Task.ControlRequests) != 1 {
		t.Fatalf("idempotent pause: %#v err=%v", again, err)
	}
	loaded, err := taskstore.New(home).Load(task.ProjectID, task.ID)
	if err != nil || len(loaded.Attempts) != 1 {
		t.Fatalf("crash recovery load: %#v err=%v", loaded, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "projects", "project", "workers", "worker", "events.jsonl")); err != nil || len(data) == 0 {
		t.Fatalf("control event missing: %q err=%v", data, err)
	}
}

func TestPauseFailureDoesNotTransition(t *testing.T) {
	home, state, _ := fixture(t, workerdomain.LifecycleExecuting, workerdomain.TaskStatusAssigned)
	_, err := (Service{Home: home, Processes: &fakeProcess{err: errors.New("denied")}}).Pause(context.Background(), state)
	if err == nil {
		t.Fatal("expected stop error")
	}
	loaded, _ := workerstate.New(home).Load(state.ProjectID, state.WorkerID)
	if loaded.Lifecycle != workerdomain.LifecycleExecuting {
		t.Fatalf("unexpected transition: %s", loaded.Lifecycle)
	}
}

func TestResumeUsesOnlyAdvertisedSessionSupport(t *testing.T) {
	home, state, _ := fixture(t, workerdomain.LifecyclePaused, workerdomain.TaskStatusPaused)
	state.RuntimeSession = &workerdomain.SessionRef{Runtime: "test", SessionID: "session-one"}
	state, _ = workerstate.New(home).Save(state, state.Revision)
	result, err := (Service{Home: home, Launchers: map[string]workerlaunch.Launcher{"test": fakeResumer{supported: true}}}).Resume(context.Background(), state)
	if err != nil || !result.Resumed || result.State.Lifecycle != workerdomain.LifecycleExecuting || len(result.Task.Attempts) != 2 {
		t.Fatalf("supported resume: %#v err=%v", result, err)
	}
}

func TestResumeUnsupportedCreatesNewAttemptPath(t *testing.T) {
	home, state, _ := fixture(t, workerdomain.LifecyclePaused, workerdomain.TaskStatusPaused)
	state.RuntimeSession = &workerdomain.SessionRef{Runtime: "test", SessionID: "session-one"}
	state, _ = workerstate.New(home).Save(state, state.Revision)
	started := false
	result, err := (Service{
		Home: home, Launchers: map[string]workerlaunch.Launcher{"test": fakeResumer{supported: false}},
		StartNew: func(context.Context, string) error { started = true; return nil },
	}).Resume(context.Background(), state)
	if err != nil || result.Resumed || !started || result.State.Lifecycle != workerdomain.LifecycleIdle || len(result.Task.Attempts) != 1 {
		t.Fatalf("unsupported resume: %#v started=%t err=%v", result, started, err)
	}
}

func TestCancelIsIdempotentAndRetryRejectsCanceled(t *testing.T) {
	home, state, task := fixture(t, workerdomain.LifecyclePaused, workerdomain.TaskStatusPaused)
	result, err := (Service{Home: home}).Cancel(context.Background(), state, task)
	if err != nil || result.Task.Status != workerdomain.TaskStatusCanceled || result.State.Lifecycle != workerdomain.LifecycleIdle {
		t.Fatalf("cancel: %#v err=%v", result, err)
	}
	again, err := (Service{Home: home}).Cancel(context.Background(), result.State, result.Task)
	if err != nil || !again.Idempotent {
		t.Fatalf("repeat cancel: %#v err=%v", again, err)
	}
	if _, err := (Service{Home: home}).Retry(context.Background(), result.State, result.Task); !errors.Is(err, ErrInvalidControl) {
		t.Fatalf("retry canceled error = %v", err)
	}
}

func TestRetryAppendsRequestWithoutOverwritingAttempt(t *testing.T) {
	home, state, task := fixture(t, workerdomain.LifecyclePaused, workerdomain.TaskStatusPaused)
	started := false
	result, err := (Service{Home: home, StartNew: func(context.Context, string) error {
		started = true
		return nil
	}}).Retry(context.Background(), state, task)
	if err != nil || !started || result.Task.Status != workerdomain.TaskStatusAssigned {
		t.Fatalf("retry: %#v started=%t err=%v", result, started, err)
	}
	if len(result.Task.Attempts) != 1 || result.Task.Attempts[0].RunID != "run-one" || len(result.Task.ControlRequests) != 1 {
		t.Fatalf("prior evidence changed: %#v", result.Task)
	}
}

func fixture(t *testing.T, lifecycle workerdomain.Lifecycle, status workerdomain.TaskStatus) (string, workerdomain.WorkerState, workerdomain.Task) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("# Task\n\nDo it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	def := workerdomain.WorkerDefinition{
		ID: "worker", DisplayName: "Worker", Role: "Test", ProjectID: "project",
		Runtime: workerdomain.RuntimeRef{Name: "test"},
		Policy:  workerdomain.WorkerPolicy{BranchPrefix: "worker", DefaultWorktree: "."},
	}
	task, err := taskstore.New(home).Assign(root, def, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	task, err = taskstore.New(home).UpdateControl(task.ProjectID, task.ID, func(current *workerdomain.Task) error {
		current.Status = status
		current.AttemptCount = 1
		current.Attempts = []workerdomain.TaskAttempt{{Number: 1, RunID: "run-one", Runtime: "test", ProcessID: 123, StartedAt: time.Now().UTC(), Disposition: "executing"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := workerstate.New(home).Load("project", "worker")
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveRunID = "run-one"
	if lifecycle != workerdomain.LifecycleIdle {
		state, err = workerstate.New(home).Transition(state, workerdomain.LifecycleStarting, "")
		if err == nil {
			state, err = workerstate.New(home).Transition(state, workerdomain.LifecycleExecuting, "")
		}
		if err == nil && lifecycle == workerdomain.LifecyclePaused {
			state, err = workerstate.New(home).Transition(state, workerdomain.LifecyclePaused, "")
		}
		if err != nil {
			t.Fatal(err)
		}
	} else {
		state, err = workerstate.New(home).Save(state, state.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	return home, state, task
}
