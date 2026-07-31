// Package workercontrol owns deterministic, persisted Slice 9 controls.
package workercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerstate"
)

var ErrInvalidControl = errors.New("invalid control transition")

type ProcessController interface {
	Stop(context.Context, int, time.Duration) (forced bool, err error)
}

type OSProcessController struct{}

// Stop sends SIGTERM to exactly one recorded PID, waits for graceful exit,
// then sends SIGKILL after timeout. ESRCH means the target is already stopped.
func (OSProcessController) Stop(ctx context.Context, pid int, timeout time.Duration) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return false, err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if err := process.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		case <-timer.C:
			if err := process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
				return true, err
			}
			return true, nil
		}
	}
}

type Service struct {
	Home      string
	Timeout   time.Duration
	Processes ProcessController
	Launchers map[string]workerlaunch.Launcher
	StartNew  func(context.Context, string) error
	Now       func() time.Time
}

type Result struct {
	State      workerdomain.WorkerState `json:"state"`
	Task       workerdomain.Task        `json:"task"`
	Idempotent bool                     `json:"idempotent"`
	Forced     bool                     `json:"forced,omitempty"`
	Resumed    bool                     `json:"resumed_session,omitempty"`
}

func (s Service) Pause(ctx context.Context, state workerdomain.WorkerState) (Result, error) {
	tasks := taskstore.New(s.Home)
	task, err := tasks.Load(state.ProjectID, state.ActiveTaskID)
	if err != nil {
		return Result{}, err
	}
	if state.Lifecycle == workerdomain.LifecyclePaused {
		return Result{State: state, Task: task, Idempotent: true}, nil
	}
	if state.Lifecycle != workerdomain.LifecycleExecuting {
		return Result{}, fmt.Errorf("%w: worker %q cannot pause from %q", ErrInvalidControl, state.WorkerID, state.Lifecycle)
	}
	pid := activePID(task, state.ActiveRunID)
	controller := s.Processes
	if controller == nil {
		controller = OSProcessController{}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	forced, err := controller.Stop(ctx, pid, timeout)
	if err != nil {
		return Result{}, fmt.Errorf("stop worker %q run %q: %w", state.WorkerID, state.ActiveRunID, err)
	}
	task, err = tasks.UpdateControl(task.ProjectID, task.ID, func(t *workerdomain.Task) error {
		t.Status = workerdomain.TaskStatusPaused
		if len(t.Attempts) > 0 {
			t.Attempts[len(t.Attempts)-1].Disposition = map[bool]string{true: "pause_forced", false: "paused"}[forced]
			now := s.now()
			t.Attempts[len(t.Attempts)-1].FinishedAt = &now
		}
		t.ControlRequests = append(t.ControlRequests, request("pause", map[bool]string{true: "forced", false: "graceful"}[forced], s.now()))
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecyclePaused, "")
	if err == nil {
		err = s.event(state, task.ID, "pause", task.ControlRequests[len(task.ControlRequests)-1].Result)
	}
	return Result{State: state, Task: task, Forced: forced}, err
}

func (s Service) Resume(ctx context.Context, state workerdomain.WorkerState) (Result, error) {
	tasks := taskstore.New(s.Home)
	task, err := tasks.Load(state.ProjectID, state.ActiveTaskID)
	if err != nil {
		return Result{}, err
	}
	if state.Lifecycle == workerdomain.LifecycleExecuting {
		return Result{State: state, Task: task, Idempotent: true}, nil
	}
	if state.Lifecycle != workerdomain.LifecyclePaused {
		return Result{}, fmt.Errorf("%w: worker %q cannot resume from %q", ErrInvalidControl, state.WorkerID, state.Lifecycle)
	}
	runtimeName := ""
	if state.RuntimeSession != nil {
		runtimeName = state.RuntimeSession.Runtime
	}
	launcher := s.Launchers[runtimeName]
	caps, advertised := launcher.(workerlaunch.CapabilityProvider)
	resumer, resumable := launcher.(workerlaunch.Resumer)
	if advertised && caps.Capabilities().SessionResume && resumable && state.RuntimeSession != nil {
		// Session resumers require the original launch request. Adapters that
		// support this capability reconstruct it from their durable run record.
		launch, resumeErr := resumer.Resume(ctx, workerlaunch.LaunchRequest{}, state.RuntimeSession.SessionID)
		if resumeErr != nil {
			return Result{}, resumeErr
		}
		state.ActiveRunID = launch.RunID
		state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleStarting, "")
		if err == nil {
			state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleExecuting, "")
		}
		task, _ = tasks.UpdateControl(task.ProjectID, task.ID, func(t *workerdomain.Task) error {
			t.Status = workerdomain.TaskStatusAssigned
			t.AttemptCount++
			t.Attempts = append(t.Attempts, workerdomain.TaskAttempt{Number: len(t.Attempts) + 1, RunID: launch.RunID, Runtime: runtimeName, SessionID: launch.Session.ID, ProcessID: launch.Process.PID, StartedAt: s.now(), Disposition: "executing", Resumed: true})
			t.ControlRequests = append(t.ControlRequests, request("resume", "session_resumed", s.now()))
			return nil
		})
		if err == nil {
			err = s.event(state, task.ID, "resume", "session_resumed")
		}
		return Result{State: state, Task: task, Resumed: true}, err
	}
	state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleIdle, "")
	if err != nil {
		return Result{}, err
	}
	task, err = tasks.UpdateControl(task.ProjectID, task.ID, func(t *workerdomain.Task) error {
		t.Status = workerdomain.TaskStatusAssigned
		t.ControlRequests = append(t.ControlRequests, request("resume", "new_attempt", s.now()))
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if s.StartNew != nil {
		if err := s.StartNew(ctx, state.WorkerID); err != nil {
			return Result{State: state, Task: task}, err
		}
	}
	return Result{State: state, Task: task}, s.event(state, task.ID, "resume", "new_attempt")
}

func (s Service) Retry(ctx context.Context, state workerdomain.WorkerState, task workerdomain.Task) (Result, error) {
	if task.Status != workerdomain.TaskStatusFailed && task.Status != workerdomain.TaskStatusPaused {
		return Result{}, fmt.Errorf("%w: task %q cannot retry from %q", ErrInvalidControl, task.ID, task.Status)
	}
	if state.ActiveTaskID != "" && state.ActiveTaskID != task.ID {
		return Result{}, fmt.Errorf("%w: worker %q has unrelated active task %q", ErrInvalidControl, state.WorkerID, state.ActiveTaskID)
	}
	task, err := taskstore.New(s.Home).UpdateControl(task.ProjectID, task.ID, func(t *workerdomain.Task) error {
		t.Status = workerdomain.TaskStatusAssigned
		t.ControlRequests = append(t.ControlRequests, request("retry", "new_attempt", s.now()))
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	state.ActiveTaskID = task.ID
	if state.Lifecycle == workerdomain.LifecyclePaused {
		state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleIdle, "")
	} else if state.Lifecycle != workerdomain.LifecycleIdle {
		state, err = workerstate.New(s.Home).Reset(state)
		if err == nil {
			state.ActiveTaskID = task.ID
			state, err = workerstate.New(s.Home).Save(state, state.Revision)
		}
	} else {
		state, err = workerstate.New(s.Home).Save(state, state.Revision)
	}
	if err == nil && s.StartNew != nil {
		err = s.StartNew(ctx, state.WorkerID)
	}
	if err == nil {
		err = s.event(state, task.ID, "retry", "new_attempt")
	}
	return Result{State: state, Task: task}, err
}

func (s Service) Cancel(ctx context.Context, state workerdomain.WorkerState, task workerdomain.Task) (Result, error) {
	if task.Status == workerdomain.TaskStatusCanceled {
		return Result{State: state, Task: task, Idempotent: true}, nil
	}
	if state.ActiveTaskID == task.ID && state.Lifecycle == workerdomain.LifecycleExecuting {
		if _, err := s.Pause(ctx, state); err != nil {
			return Result{}, err
		}
		state, _ = workerstate.New(s.Home).Load(state.ProjectID, state.WorkerID)
	}
	if task.Status == workerdomain.TaskStatusPending {
		if _, err := taskqueue.New(s.Home).Remove(task.ProjectID, task.WorkerID, task.ID); err != nil && !errors.Is(err, taskqueue.ErrNotQueued) {
			return Result{}, err
		}
	}
	task, err := taskstore.New(s.Home).UpdateControl(task.ProjectID, task.ID, func(t *workerdomain.Task) error {
		t.Status = workerdomain.TaskStatusCanceled
		t.ControlRequests = append(t.ControlRequests, request("cancel", "canceled", s.now()))
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if state.ActiveTaskID == task.ID {
		state.ActiveTaskID, state.ActiveRunID, state.RuntimeSession = "", "", nil
		if state.Lifecycle == workerdomain.LifecyclePaused {
			state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleIdle, "")
		} else {
			state, err = workerstate.New(s.Home).Save(state, state.Revision)
		}
	}
	if err == nil {
		err = s.event(state, task.ID, "cancel", "canceled")
	}
	return Result{State: state, Task: task}, err
}

func activePID(task workerdomain.Task, runID string) int {
	for i := len(task.Attempts) - 1; i >= 0; i-- {
		if task.Attempts[i].RunID == runID {
			return task.Attempts[i].ProcessID
		}
	}
	return 0
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func request(operation, result string, now time.Time) workerdomain.ControlRequest {
	return workerdomain.ControlRequest{ID: fmt.Sprintf("%s-%d", operation, now.UnixNano()), Operation: operation, RequestedAt: now, CompletedAt: now, Result: result}
}

func (s Service) event(state workerdomain.WorkerState, taskID, operation, result string) error {
	path := filepath.Join(s.Home, "projects", state.ProjectID, "workers", state.WorkerID, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	record := map[string]any{
		"version": 1, "timestamp": s.now(), "type": "control",
		"project_id": state.ProjectID, "worker_id": state.WorkerID, "task_id": taskID,
		"operation": operation, "result": result, "lifecycle": state.Lifecycle, "revision": state.Revision,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
