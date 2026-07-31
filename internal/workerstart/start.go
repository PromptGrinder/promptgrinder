// Package workerstart coordinates authoritative named-worker launch state.
package workerstart

import (
	"context"
	"fmt"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerpathpolicy"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
	"promptgrinder/internal/workerworktree"
	"promptgrinder/internal/worktree"
)

type Service struct {
	Home      string
	Config    config.Config
	Launchers map[string]workerlaunch.Launcher
}

type Result struct {
	Request workerlaunch.LaunchRequest
	Launch  workerlaunch.LaunchResult
	State   workerdomain.WorkerState
}

func (s Service) Start(ctx context.Context, location, workerID, runtimeOverride string) (Result, error) {
	registry, err := workerregistry.Load(location)
	if err != nil {
		return Result{}, fmt.Errorf("discover named-worker project: %w", err)
	}
	definition, err := registry.Get(workerID)
	if err != nil {
		return Result{}, err
	}
	stateStore := workerstate.New(s.Home)
	workerState, err := stateStore.Load(registry.Project.ID, definition.ID)
	if err != nil {
		return Result{}, fmt.Errorf("load state for project %q worker %q: %w", registry.Project.ID, definition.ID, err)
	}
	if workerState.Lifecycle != workerdomain.LifecycleIdle {
		return Result{}, fmt.Errorf("project %q worker %q cannot start from lifecycle %q", registry.Project.ID, definition.ID, workerState.Lifecycle)
	}
	if workerState.ActiveTaskID == "" {
		return Result{}, fmt.Errorf("project %q worker %q has no assigned task", registry.Project.ID, definition.ID)
	}
	task, err := taskstore.New(s.Home).Load(registry.Project.ID, workerState.ActiveTaskID)
	if err != nil {
		return Result{}, fmt.Errorf("restore task %q for project %q worker %q: %w", workerState.ActiveTaskID, registry.Project.ID, definition.ID, err)
	}
	if task.WorkerID != definition.ID || task.ProjectID != registry.Project.ID || task.Status != workerdomain.TaskStatusAssigned {
		return Result{}, fmt.Errorf("assigned task %q does not match project %q worker %q", task.ID, registry.Project.ID, definition.ID)
	}

	// PromptGrinder-owned reconciled policy is authoritative at launch time.
	definition.Policy = workerState.EffectivePolicy
	selection, err := workerworktree.Plan(registry.Root, definition.Policy, task.ID)
	if err != nil {
		return Result{}, fmt.Errorf("plan Git location for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}
	if task.LaunchSetup != "preparing" && task.LaunchSetup != "prepared" {
		if err := workerworktree.ValidateAvailable(registry.Root, selection); err != nil {
			return Result{}, err
		}
		task.Worktree, task.Branch = selection.Worktree, selection.Branch
		task.BaseBranch, task.BaseRevision = selection.BaseBranch, selection.BaseRevision
		task.LaunchSetup = "preparing"
		task, err = taskstore.New(s.Home).SaveLaunchLocation(task)
		if err != nil {
			return Result{}, fmt.Errorf("persist launch setup: %w", err)
		}
	}
	lease, err := worktree.Acquire(s.Home, selection.Worktree, registry.Project.ID+"/"+definition.ID+"/"+task.ID, false)
	if err != nil {
		return Result{}, err
	}
	releaseClaim := true
	defer func() {
		if releaseClaim {
			_ = lease.Release()
		}
	}()
	selection, err = workerworktree.Prepare(registry.Root, definition.Policy, task)
	if err != nil {
		return Result{}, fmt.Errorf("prepare Git location for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}
	task.Worktree, task.Branch = selection.Worktree, selection.Branch
	task.BaseBranch, task.BaseRevision = selection.BaseBranch, selection.BaseRevision
	task.LaunchSetup = "prepared"
	task, err = taskstore.New(s.Home).SaveLaunchLocation(task)
	if err != nil {
		return Result{}, fmt.Errorf("persist prepared Git location: %w", err)
	}
	workerState.Worktree, workerState.Branch = selection.Worktree, selection.Branch
	workerState.BaseBranch, workerState.BaseRevision = selection.BaseBranch, selection.BaseRevision
	workerState, err = stateStore.Save(workerState, workerState.Revision)
	if err != nil {
		return Result{}, fmt.Errorf("persist worker Git location: %w", err)
	}
	request, err := workerlaunch.Build(workerlaunch.BuildOptions{
		Project: registry.Project, Worker: definition, Repository: registry.Root,
		Task: workerlaunch.TaskContext{
			ID: task.ID, Source: task.SourceReference, Instructions: task.Instructions,
		},
		RuntimeOverride: runtimeOverride, RuntimeDefault: s.Config.WorkerRuntime,
		RuntimeOptions: s.Config.RuntimeOptions,
		Branch:         selection.Branch, BaseBranch: selection.BaseBranch, BaseRevision: selection.BaseRevision,
	})
	if err != nil {
		return Result{}, fmt.Errorf("build launch for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}
	launcher, ok := s.Launchers[request.Runtime.Name]
	if !ok {
		return Result{}, fmt.Errorf("project %q worker %q runtime %q is not registered", registry.Project.ID, definition.ID, request.Runtime.Name)
	}
	if err := workerlaunch.Negotiate(launcher, request.Runtime.RequiredCapabilities); err != nil {
		return Result{}, fmt.Errorf("capability negotiation for project %q worker %q task %q runtime %q: %w", registry.Project.ID, definition.ID, task.ID, request.Runtime.Name, err)
	}
	if preflight, ok := launcher.(workerlaunch.Preflighter); ok {
		if err := preflight.Preflight(ctx, request); err != nil {
			return Result{}, fmt.Errorf("preflight project %q worker %q task %q runtime %q: %w", registry.Project.ID, definition.ID, task.ID, request.Runtime.Name, err)
		}
	}
	if err := workerpathpolicy.Validate(definition.Policy); err != nil {
		return Result{}, fmt.Errorf("validate path policy for project %q worker %q: %w", registry.Project.ID, definition.ID, err)
	}
	baseline, err := workerpathpolicy.Capture(request.Worktree)
	if err != nil {
		return Result{}, fmt.Errorf("snapshot Git state for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}
	if err := workerpathpolicy.SaveSnapshot(s.Home, registry.Project.ID, definition.ID, baseline); err != nil {
		return Result{}, fmt.Errorf("persist path-policy snapshot for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}

	workerState, err = stateStore.Transition(workerState, workerdomain.LifecycleStarting, "")
	if err != nil {
		return Result{}, err
	}
	launch, launchErr := launcher.Launch(ctx, request)
	disposition := "executing"
	if launchErr != nil {
		disposition = "launch_failed"
	}
	_, attemptErr := taskstore.New(s.Home).UpdateControl(task.ProjectID, task.ID, func(current *workerdomain.Task) error {
		if launchErr != nil {
			current.Status = workerdomain.TaskStatusFailed
		}
		current.AttemptCount++
		current.Attempts = append(current.Attempts, workerdomain.TaskAttempt{
			Number: len(current.Attempts) + 1, RunID: launch.RunID, Runtime: request.Runtime.Name,
			SessionID: launch.Session.ID, ProcessID: launch.Process.PID, StartedAt: attemptStarted(launch),
			FinishedAt: launch.Process.FinishedAt, Disposition: disposition,
		})
		return nil
	})
	if attemptErr != nil {
		return Result{Request: request, Launch: launch, State: workerState}, fmt.Errorf("persist attempt evidence: %w", attemptErr)
	}
	workerState.ActiveRunID = launch.RunID
	if launch.Session.ID != "" {
		workerState.RuntimeSession = &workerdomain.SessionRef{Runtime: request.Runtime.Name, SessionID: launch.Session.ID}
	}
	if launchErr != nil {
		reason := fmt.Sprintf("launch project %q worker %q task %q runtime %q: %v", registry.Project.ID, definition.ID, task.ID, request.Runtime.Name, launchErr)
		failed, transitionErr := stateStore.Transition(workerState, workerdomain.LifecycleFailed, reason)
		if transitionErr != nil {
			return Result{Request: request, Launch: launch, State: workerState}, fmt.Errorf("%s; persist failed lifecycle: %w", reason, transitionErr)
		}
		return Result{Request: request, Launch: launch, State: failed}, fmt.Errorf("%s", reason)
	}
	if launch.RunID == "" {
		reason := fmt.Sprintf("launch project %q worker %q task %q runtime %q returned no run reference", registry.Project.ID, definition.ID, task.ID, request.Runtime.Name)
		failed, transitionErr := stateStore.Transition(workerState, workerdomain.LifecycleFailed, reason)
		if transitionErr != nil {
			return Result{Request: request, Launch: launch, State: workerState}, fmt.Errorf("%s; persist failed lifecycle: %w", reason, transitionErr)
		}
		return Result{Request: request, Launch: launch, State: failed}, fmt.Errorf("%s", reason)
	}
	if launch.Process.PID > 0 {
		if err := lease.TransferPID(launch.Process.PID); err != nil {
			reason := fmt.Sprintf("persist worktree claim for project %q worker %q task %q: %v", registry.Project.ID, definition.ID, task.ID, err)
			failed, transitionErr := stateStore.Transition(workerState, workerdomain.LifecycleFailed, reason)
			if transitionErr != nil {
				return Result{Request: request, Launch: launch, State: workerState}, fmt.Errorf("%s; persist failed lifecycle: %w", reason, transitionErr)
			}
			return Result{Request: request, Launch: launch, State: failed}, fmt.Errorf("%s", reason)
		}
		releaseClaim = false
	}
	executing, err := stateStore.Transition(workerState, workerdomain.LifecycleExecuting, "")
	if err != nil {
		return Result{Request: request, Launch: launch, State: workerState}, err
	}
	// A launcher that returns a finished process provides the existing safe
	// completion checkpoint. Asynchronous launchers retain the persisted
	// baseline for their completion supervisor to check.
	if launch.Process.FinishedAt != nil {
		paths, checkErr := workerpathpolicy.AttributedChanges(request.Worktree, baseline)
		if checkErr != nil {
			return Result{Request: request, Launch: launch, State: executing}, fmt.Errorf("check completed worker path policy: %w", checkErr)
		}
		violations, checkErr := workerpathpolicy.Violations(definition.Policy, paths)
		if checkErr != nil {
			return Result{Request: request, Launch: launch, State: executing}, fmt.Errorf("evaluate completed worker path policy: %w", checkErr)
		}
		if len(violations) > 0 {
			reason := fmt.Sprintf("path policy violation at completion: %d path(s); changes retained for review", len(violations))
			blocked, transitionErr := stateStore.Transition(executing, workerdomain.LifecycleBlocked, reason)
			eventErr := workerpathpolicy.AppendViolationEvent(s.Home, workerpathpolicy.Event{
				ProjectID: registry.Project.ID, WorkerID: definition.ID, TaskID: task.ID,
				RunID: launch.RunID, Checkpoint: "completion", Violations: violations, Message: reason,
			})
			if transitionErr != nil {
				return Result{Request: request, Launch: launch, State: executing}, fmt.Errorf("%s; persist blocked lifecycle: %w", reason, transitionErr)
			}
			if eventErr != nil {
				return Result{Request: request, Launch: launch, State: blocked}, fmt.Errorf("%s; persist violation event: %w", reason, eventErr)
			}
			return Result{Request: request, Launch: launch, State: blocked}, fmt.Errorf("%s", reason)
		}
	}
	return Result{Request: request, Launch: launch, State: executing}, nil
}

func attemptStarted(launch workerlaunch.LaunchResult) time.Time {
	if launch.Process.StartedAt != nil {
		return launch.Process.StartedAt.UTC()
	}
	return time.Now().UTC()
}
