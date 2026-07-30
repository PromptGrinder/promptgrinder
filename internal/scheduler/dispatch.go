package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
)

type DecisionEvent struct {
	Version   int            `json:"version"`
	Timestamp time.Time      `json:"timestamp"`
	ProjectID string         `json:"project_id"`
	WorkerID  string         `json:"worker_id,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	Runtime   string         `json:"runtime,omitempty"`
	Decision  string         `json:"decision"`
	Reason    string         `json:"reason"`
	Data      map[string]any `json:"data,omitempty"`
}

type Dispatch struct {
	Worker workerdomain.WorkerDefinition
	Task   workerdomain.Task
}

type Service struct {
	Home     string
	Config   config.Config
	Now      func() time.Time
	Owner    string
	Dispatch func(context.Context, Dispatch) error
}

func (s Service) EventsPath(projectID string) string {
	return filepath.Join(s.Home, "projects", projectID, "scheduler", "events.jsonl")
}

// RunOnce makes at most one dispatch. A project lock serializes the complete
// eligibility/reservation decision across concurrent scheduler processes.
func (s Service) RunOnce(ctx context.Context, registry *workerregistry.Registry) (*Dispatch, error) {
	if registry == nil {
		return nil, fmt.Errorf("worker registry is required")
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	owner := s.Owner
	if owner == "" {
		owner = fmt.Sprintf("scheduler-%d-%d", os.Getpid(), now().UnixNano())
	}
	ttl := s.Config.SchedulerLeaseTTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	unlock, err := s.projectLock(registry.Project.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	states := make(map[string]workerdomain.WorkerState, len(registry.Workers))
	projectRunning := 0
	runtimeRunning := map[string]int{}
	for _, definition := range registry.List() {
		state, loadErr := workerstate.New(s.Home).Ensure(definition)
		if loadErr != nil {
			return nil, loadErr
		}
		states[definition.ID] = state
		if state.Lifecycle == workerdomain.LifecycleStarting || state.Lifecycle == workerdomain.LifecycleExecuting {
			projectRunning++
			runtimeRunning[definition.Runtime.Name]++
		}
	}
	if limit := s.Config.SchedulerProjectConcurrency; limit > 0 && projectRunning >= limit {
		if err := s.event(registry.Project.ID, DecisionEvent{Decision: "deferred", Reason: "project_concurrency_limit", Data: map[string]any{"running": projectRunning, "limit": limit}}); err != nil {
			return nil, err
		}
		return nil, nil
	}

	definitions := registry.List()
	sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	for _, definition := range definitions {
		state := states[definition.ID]
		if state.Lifecycle != workerdomain.LifecycleIdle {
			if err := s.workerEvent(registry.Project.ID, definition, "", "ineligible", "worker_not_idle", map[string]any{"lifecycle": state.Lifecycle}); err != nil {
				return nil, err
			}
			continue
		}
		if state.ActiveTaskID != "" {
			if err := s.workerEvent(registry.Project.ID, definition, state.ActiveTaskID, "ineligible", "worker_has_active_task", nil); err != nil {
				return nil, err
			}
			continue
		}
		if limit := s.Config.SchedulerRuntimeConcurrency[definition.Runtime.Name]; limit > 0 && runtimeRunning[definition.Runtime.Name] >= limit {
			if err := s.workerEvent(registry.Project.ID, definition, "", "deferred", "runtime_concurrency_limit", map[string]any{"running": runtimeRunning[definition.Runtime.Name], "limit": limit}); err != nil {
				return nil, err
			}
			continue
		}
		queueStore := taskqueue.New(s.Home)
		queue, loadErr := queueStore.List(registry.Project.ID, definition.ID)
		if loadErr != nil {
			return nil, loadErr
		}
		if len(queue.Entries) == 0 {
			if err := s.workerEvent(registry.Project.ID, definition, "", "ineligible", "queue_empty", nil); err != nil {
				return nil, err
			}
			continue
		}
		entry, _, acquireErr := queueStore.Acquire(registry.Project.ID, definition.ID, owner, ttl)
		if errors.Is(acquireErr, taskqueue.ErrLeaseHeld) {
			if err := s.workerEvent(registry.Project.ID, definition, queue.Entries[0].TaskID, "deferred", "lease_held", nil); err != nil {
				return nil, err
			}
			continue
		}
		if acquireErr != nil {
			return nil, acquireErr
		}
		task, loadErr := taskstore.New(s.Home).Load(registry.Project.ID, entry.TaskID)
		if loadErr != nil {
			_, _ = queueStore.Release(registry.Project.ID, definition.ID, entry.TaskID, owner)
			return nil, loadErr
		}
		if task.Status != workerdomain.TaskStatusPending || task.WorkerID != definition.ID {
			_, _ = queueStore.Release(registry.Project.ID, definition.ID, entry.TaskID, owner)
			if err := s.workerEvent(registry.Project.ID, definition, entry.TaskID, "rejected", "task_not_pending_or_worker_mismatch", nil); err != nil {
				return nil, err
			}
			continue
		}
		state.ActiveTaskID = task.ID
		if _, saveErr := workerstate.New(s.Home).Save(state, state.Revision); saveErr != nil {
			_, _ = queueStore.Release(registry.Project.ID, definition.ID, task.ID, owner)
			return nil, saveErr
		}
		task, saveErr := taskstore.New(s.Home).SetStatus(registry.Project.ID, task.ID, workerdomain.TaskStatusAssigned)
		if saveErr != nil {
			return nil, saveErr
		}
		if _, commitErr := queueStore.Commit(registry.Project.ID, definition.ID, task.ID, owner); commitErr != nil {
			return nil, commitErr
		}
		dispatch := &Dispatch{Worker: definition, Task: task}
		if err := s.workerEvent(registry.Project.ID, definition, task.ID, "dispatched", "eligible_fifo_head", map[string]any{"lease_owner": owner}); err != nil {
			return dispatch, err
		}
		if s.Dispatch != nil {
			if err := s.Dispatch(ctx, *dispatch); err != nil {
				_ = s.workerEvent(registry.Project.ID, definition, task.ID, "dispatch_failed", err.Error(), nil)
				return dispatch, err
			}
		}
		return dispatch, nil
	}
	return nil, nil
}

func (s Service) workerEvent(projectID string, worker workerdomain.WorkerDefinition, taskID, decision, reason string, data map[string]any) error {
	return s.event(projectID, DecisionEvent{
		WorkerID: worker.ID, TaskID: taskID, Runtime: worker.Runtime.Name,
		Decision: decision, Reason: reason, Data: data,
	})
}

func (s Service) event(projectID string, event DecisionEvent) error {
	if event.Timestamp.IsZero() {
		if s.Now != nil {
			event.Timestamp = s.Now().UTC()
		} else {
			event.Timestamp = time.Now().UTC()
		}
	}
	event.Version, event.ProjectID = 1, projectID
	path := s.EventsPath(projectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (s Service) projectLock(projectID string) (func(), error) {
	dir := filepath.Join(s.Home, "projects", projectID, "scheduler")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".scheduler.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
