// Package review owns the local, runtime-neutral review handoff lifecycle.
package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
)

var ErrInvalidReview = errors.New("invalid review transition")

// RejectionPolicy is deliberately deterministic: rejected tasks return to the
// tail of the original implementing worker's FIFO. The rejected evidence and
// all prior attempts remain append-only on the task.
const RejectionPolicy = "requeue_original_implementer_fifo_tail"

type Evidence struct {
	Summary      string
	ChangedPaths []string
	Commits      []string
	Validations  []workerdomain.Validation
}

type Service struct {
	Home string
	Now  func() time.Time
}

type Result struct {
	Task            workerdomain.Task          `json:"task"`
	State           workerdomain.WorkerState   `json:"state"`
	Handoff         workerdomain.ReviewHandoff `json:"handoff"`
	RejectionPolicy string                     `json:"rejection_policy,omitempty"`
}

func (s Service) Submit(registry *workerregistry.Registry, taskID, implementerID string, evidence Evidence) (Result, error) {
	if registry == nil {
		return Result{}, fmt.Errorf("worker registry is required")
	}
	if _, err := registry.Get(implementerID); err != nil {
		return Result{}, err
	}
	tasks := taskstore.New(s.Home)
	task, err := tasks.Load(registry.Project.ID, taskID)
	if err != nil {
		return Result{}, err
	}
	if task.WorkerID != implementerID {
		return Result{}, fmt.Errorf("%w: task %q belongs to implementer %q, not %q", ErrInvalidReview, task.ID, task.WorkerID, implementerID)
	}
	state, err := workerstate.New(s.Home).Load(task.ProjectID, implementerID)
	if err != nil {
		return Result{}, err
	}
	if task.Status != workerdomain.TaskStatusAssigned || state.Lifecycle != workerdomain.LifecycleExecuting || state.ActiveTaskID != task.ID {
		return Result{}, fmt.Errorf("%w: task %q and worker %q are not executing together", ErrInvalidReview, task.ID, implementerID)
	}
	if strings.TrimSpace(evidence.Summary) == "" || len(evidence.Validations) == 0 || len(task.Attempts) == 0 {
		return Result{}, fmt.Errorf("%w: summary, validation evidence, and task attempts are required", ErrInvalidReview)
	}
	runtimeEvidence := make([]workerdomain.RuntimeEvidence, len(task.Attempts))
	for i, attempt := range task.Attempts {
		runtimeEvidence[i] = workerdomain.RuntimeEvidence{
			AttemptNumber: attempt.Number, RunID: attempt.RunID, Runtime: attempt.Runtime,
			SessionID: attempt.SessionID, Disposition: attempt.Disposition,
		}
	}
	now := s.now()
	handoff := workerdomain.ReviewHandoff{
		ID: fmt.Sprintf("review-%d", now.UnixNano()), ImplementerID: implementerID,
		WorkerID: implementerID, TaskID: task.ID, Summary: strings.TrimSpace(evidence.Summary),
		ChangedPaths: clone(evidence.ChangedPaths), Commits: clone(evidence.Commits),
		Validations:     append([]workerdomain.Validation(nil), evidence.Validations...),
		TaskAttempts:    append([]workerdomain.TaskAttempt(nil), task.Attempts...),
		RuntimeEvidence: runtimeEvidence, CreatedAt: now, Status: workerdomain.ReviewStatusPending,
	}
	task, err = tasks.UpdateControl(task.ProjectID, task.ID, func(current *workerdomain.Task) error {
		current.Status = workerdomain.TaskStatusReview
		current.ReviewHandoffs = append(current.ReviewHandoffs, handoff)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleAwaitingReview, "")
	if err != nil {
		return Result{}, err
	}
	if err := s.event(state, task.ID, "review.submitted", implementerID, "pending"); err != nil {
		return Result{}, err
	}
	return Result{Task: task, State: state, Handoff: handoff}, nil
}

func (s Service) Inspect(registry *workerregistry.Registry, taskID string) (Result, error) {
	task, state, handoff, err := s.current(registry, taskID)
	return Result{Task: task, State: state, Handoff: handoff}, err
}

func (s Service) Accept(registry *workerregistry.Registry, taskID, reviewerID, reason string) (Result, error) {
	task, state, handoff, err := s.current(registry, taskID)
	if err != nil {
		return Result{}, err
	}
	if err := authorize(registry, handoff, reviewerID, reason); err != nil {
		return Result{}, err
	}
	now := s.now()
	task, err = taskstore.New(s.Home).UpdateControl(task.ProjectID, task.ID, func(current *workerdomain.Task) error {
		item := &current.ReviewHandoffs[len(current.ReviewHandoffs)-1]
		item.Status, item.ReviewerID, item.DecisionReason, item.DecidedAt = workerdomain.ReviewStatusAccepted, reviewerID, strings.TrimSpace(reason), &now
		current.Status = workerdomain.TaskStatusAccepted
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	state.LastCompletedTaskID = task.ID
	state.ActiveTaskID, state.ActiveRunID, state.RuntimeSession = "", "", nil
	state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleIdle, "")
	if err != nil {
		return Result{}, err
	}
	handoff = task.ReviewHandoffs[len(task.ReviewHandoffs)-1]
	if err := s.event(state, task.ID, "review.accepted", reviewerID, "accepted"); err != nil {
		return Result{}, err
	}
	return Result{Task: task, State: state, Handoff: handoff}, nil
}

func (s Service) Reject(registry *workerregistry.Registry, taskID, reviewerID, reason string) (Result, error) {
	task, state, handoff, err := s.current(registry, taskID)
	if err != nil {
		return Result{}, err
	}
	if err := authorize(registry, handoff, reviewerID, reason); err != nil {
		return Result{}, err
	}
	now := s.now()
	task, err = taskstore.New(s.Home).UpdateControl(task.ProjectID, task.ID, func(current *workerdomain.Task) error {
		item := &current.ReviewHandoffs[len(current.ReviewHandoffs)-1]
		item.Status, item.ReviewerID, item.DecisionReason, item.DecidedAt = workerdomain.ReviewStatusRejected, reviewerID, strings.TrimSpace(reason), &now
		current.Status = workerdomain.TaskStatusPending
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := taskqueue.New(s.Home).Enqueue(task.ProjectID, task.WorkerID, task.ID); err != nil {
		return Result{}, err
	}
	state.ActiveTaskID, state.ActiveRunID, state.RuntimeSession = "", "", nil
	state, err = workerstate.New(s.Home).Transition(state, workerdomain.LifecycleIdle, "")
	if err != nil {
		return Result{}, err
	}
	handoff = task.ReviewHandoffs[len(task.ReviewHandoffs)-1]
	if err := s.event(state, task.ID, "review.rejected", reviewerID, RejectionPolicy); err != nil {
		return Result{}, err
	}
	return Result{Task: task, State: state, Handoff: handoff, RejectionPolicy: RejectionPolicy}, nil
}

func (s Service) current(registry *workerregistry.Registry, taskID string) (workerdomain.Task, workerdomain.WorkerState, workerdomain.ReviewHandoff, error) {
	if registry == nil {
		return workerdomain.Task{}, workerdomain.WorkerState{}, workerdomain.ReviewHandoff{}, fmt.Errorf("worker registry is required")
	}
	task, err := taskstore.New(s.Home).Load(registry.Project.ID, taskID)
	if err != nil {
		return workerdomain.Task{}, workerdomain.WorkerState{}, workerdomain.ReviewHandoff{}, err
	}
	if _, err := registry.Get(task.WorkerID); err != nil {
		return workerdomain.Task{}, workerdomain.WorkerState{}, workerdomain.ReviewHandoff{}, err
	}
	state, err := workerstate.New(s.Home).Load(task.ProjectID, task.WorkerID)
	if err != nil {
		return workerdomain.Task{}, workerdomain.WorkerState{}, workerdomain.ReviewHandoff{}, err
	}
	if len(task.ReviewHandoffs) == 0 {
		return workerdomain.Task{}, workerdomain.WorkerState{}, workerdomain.ReviewHandoff{}, fmt.Errorf("%w: task %q has no review handoff", ErrInvalidReview, task.ID)
	}
	handoff := task.ReviewHandoffs[len(task.ReviewHandoffs)-1]
	return task, state, handoff, nil
}

func authorize(registry *workerregistry.Registry, handoff workerdomain.ReviewHandoff, reviewerID, reason string) error {
	if _, err := registry.Get(reviewerID); err != nil {
		return err
	}
	if handoff.Status != workerdomain.ReviewStatusPending {
		return fmt.Errorf("%w: review %q is already %s", ErrInvalidReview, handoff.ID, handoff.Status)
	}
	if registry.Project.RequireSeparateReviewer && reviewerID == handoff.ImplementerID {
		return fmt.Errorf("%w: project %q requires a reviewer separate from implementer %q", ErrInvalidReview, registry.Project.ID, reviewerID)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: decision reason is required", ErrInvalidReview)
	}
	return nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) event(state workerdomain.WorkerState, taskID, eventType, actor, result string) error {
	path := filepath.Join(s.Home, "projects", state.ProjectID, "workers", state.WorkerID, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	record := map[string]any{"version": 1, "timestamp": s.now(), "type": eventType, "project_id": state.ProjectID, "worker_id": state.WorkerID, "task_id": taskID, "actor_worker_id": actor, "result": result, "revision": state.Revision}
	data, err := json.Marshal(record)
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
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func clone(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}
