package execution

import (
	"os"

	"promptgrinder/internal/state"
	"promptgrinder/internal/terminal"
)

type Request struct {
	Context     Context
	Worker      state.Worker
	Prompt      []byte
	Script      string
	CommandData map[string]any
}

type Result struct {
	Worker state.Worker
}

type Executor struct {
	Store    state.Store
	Terminal terminal.TerminalAdapter
}

func (e Executor) Execute(request Request) (Result, error) {
	if err := os.MkdirAll(request.Context.Directories.Worker, 0o700); err != nil {
		return Result{Worker: request.Worker}, err
	}
	if err := os.WriteFile(request.Context.Directories.Prompt, request.Prompt, 0o600); err != nil {
		return Result{Worker: request.Worker}, err
	}
	if err := os.Chmod(request.Context.Directories.Prompt, 0o600); err != nil {
		return Result{Worker: request.Worker}, err
	}
	if err := os.WriteFile(request.Context.Directories.Script, []byte(request.Script), 0o700); err != nil {
		return Result{Worker: request.Worker}, err
	}
	if err := os.Chmod(request.Context.Directories.Script, 0o700); err != nil {
		return Result{Worker: request.Worker}, err
	}

	command := e.Terminal.Command(request.Context.Directories.Script)
	if request.CommandData == nil {
		request.CommandData = map[string]any{}
	}
	if _, ok := request.CommandData["engine"]; !ok && request.Worker.Engine != "" {
		request.CommandData["engine"] = request.Worker.Engine
	}
	_ = state.AppendEventForWorker(request.Worker, state.NewEvent(request.Worker.ID, state.EventEngineCommandBuilt, state.SeverityInfo, "Engine command built", request.CommandData))
	_ = state.AppendEventForWorker(request.Worker, state.NewEvent(request.Worker.ID, state.EventTerminalLaunchRequested, state.SeverityInfo, "Terminal launch requested", map[string]any{"command": command, "adapter": e.Terminal.Name()}))
	worker, err := e.Store.MarkStarted(request.Worker.ID, command, e.Terminal.Name())
	if err != nil {
		return Result{Worker: request.Worker}, err
	}
	err = e.Terminal.Launch(request.Context.Directories.Script)
	if err != nil {
		if terminal.IsExecutionError(err) {
			if latest, loadErr := e.Store.Load(request.Worker.ID); loadErr == nil {
				return Result{Worker: latest}, err
			}
			return Result{Worker: worker}, err
		}
		worker.Status = state.StatusLaunchFailed
		worker.TerminalCommand = command
		worker.TerminalAdapter = e.Terminal.Name()
		_ = e.Store.Save(worker)
		_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventTerminalLaunchFailed, state.SeverityError, "Terminal launch failed", map[string]any{"adapter": e.Terminal.Name(), "error": err.Error()}))
		return Result{Worker: worker}, err
	}
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventTerminalLaunchSucceeded, state.SeverityInfo, "Terminal launch succeeded", map[string]any{"command": command, "adapter": e.Terminal.Name()}))
	if latest, loadErr := e.Store.Load(request.Worker.ID); loadErr == nil {
		worker = latest
	}
	return Result{Worker: worker}, nil
}
