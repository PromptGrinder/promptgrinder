// Package workerstart coordinates authoritative named-worker launch state.
package workerstart

import (
	"context"
	"fmt"

	"promptgrinder/internal/config"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
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
	request, err := workerlaunch.Build(workerlaunch.BuildOptions{
		Project: registry.Project, Worker: definition, Repository: registry.Root,
		Task: workerlaunch.TaskContext{
			ID: task.ID, Source: task.SourceReference, Instructions: task.Instructions,
		},
		RuntimeOverride: runtimeOverride, RuntimeDefault: s.Config.WorkerRuntime,
		RuntimeOptions: s.Config.RuntimeOptions,
	})
	if err != nil {
		return Result{}, fmt.Errorf("build launch for project %q worker %q task %q: %w", registry.Project.ID, definition.ID, task.ID, err)
	}
	launcher, ok := s.Launchers[request.Runtime.Name]
	if !ok {
		return Result{}, fmt.Errorf("project %q worker %q runtime %q is not registered", registry.Project.ID, definition.ID, request.Runtime.Name)
	}
	if preflight, ok := launcher.(workerlaunch.Preflighter); ok {
		if err := preflight.Preflight(ctx, request); err != nil {
			return Result{}, fmt.Errorf("preflight project %q worker %q task %q runtime %q: %w", registry.Project.ID, definition.ID, task.ID, request.Runtime.Name, err)
		}
	}

	workerState, err = stateStore.Transition(workerState, workerdomain.LifecycleStarting, "")
	if err != nil {
		return Result{}, err
	}
	launch, launchErr := launcher.Launch(ctx, request)
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
	executing, err := stateStore.Transition(workerState, workerdomain.LifecycleExecuting, "")
	if err != nil {
		return Result{Request: request, Launch: launch, State: workerState}, err
	}
	return Result{Request: request, Launch: launch, State: executing}, nil
}
