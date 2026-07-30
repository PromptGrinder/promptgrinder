// Package workeradapter contains runtime-specific adapters for named workers.
package workeradapter

import (
	"context"
	"fmt"
	"path/filepath"

	"promptgrinder/internal/worker"
	"promptgrinder/internal/workerlaunch"
)

// Codex adapts the existing execution-run machinery to the runtime-neutral
// named-worker launcher contract.
type Codex struct {
	Manager worker.Manager
}

func (Codex) Capabilities() workerlaunch.Capabilities {
	return workerlaunch.Capabilities{SessionResume: false}
}

func (c Codex) Preflight(_ context.Context, request workerlaunch.LaunchRequest) error {
	if request.Runtime.Name != "codex" {
		return fmt.Errorf("Codex adapter cannot launch runtime %q", request.Runtime.Name)
	}
	manager := c.manager(request)
	return manager.ValidateContentWithMetadata(taskPath(request), request.Context, metadata(request))
}

func (c Codex) Launch(_ context.Context, request workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	if request.Runtime.Name != "codex" {
		return workerlaunch.LaunchResult{}, fmt.Errorf("Codex adapter cannot launch runtime %q", request.Runtime.Name)
	}
	result := c.manager(request).LaunchContentWithMetadata(taskPath(request), request.Context, metadata(request))
	launch := workerlaunch.LaunchResult{
		RuntimeName: "codex",
		RunID:       result.Worker.ID,
		Process: workerlaunch.RuntimeProcessResult{
			PID:        result.Worker.ProcessID,
			StartedAt:  result.Worker.StartTime,
			FinishedAt: result.Worker.FinishTime,
			ExitCode:   result.Worker.ExitCode,
		},
	}
	if result.Worker.EngineResult != nil {
		launch.Session.ID = result.Worker.EngineResult.SessionID
	}
	return launch, result.Err
}

func (c Codex) manager(request workerlaunch.LaunchRequest) worker.Manager {
	manager := c.Manager
	manager.EngineName = "codex"
	manager.EngineOverride = ""
	manager.RepositoryOverride = request.Repository
	return manager
}

func taskPath(request workerlaunch.LaunchRequest) string {
	if request.Task.Source != "" {
		return filepath.Join(request.Repository, filepath.FromSlash(request.Task.Source))
	}
	return filepath.Join(request.Repository, request.Task.ID+".md")
}

func metadata(request workerlaunch.LaunchRequest) map[string]any {
	out := make(map[string]any, len(request.Runtime.Options)+10)
	engine := make(map[string]any, len(request.Runtime.Options)+1)
	engine["name"] = request.Runtime.Name
	for key, value := range request.Runtime.Options {
		engine[key] = value
	}
	out["engine"] = engine
	out["selected_worktree"] = request.Worktree
	out["selected_branch"] = request.Branch
	out["base_branch"] = request.BaseBranch
	out["base_revision"] = request.BaseRevision
	out["named_project_id"] = request.Project.ID
	out["named_worker_id"] = request.Worker.ID
	out["named_task_id"] = request.Task.ID
	out["named_path_policy"] = true
	if relative, err := filepath.Rel(request.Repository, request.Worktree); err == nil && relative != "." {
		out["working_directory"] = relative
	}
	return out
}
