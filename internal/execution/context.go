package execution

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/state"
)

type Directories struct {
	Worker string
	Prompt string
	Script string
	Log    string
}

type Context struct {
	RepositoryRoot    string
	TaskPath          string
	WorkerID          string
	WorkingDirectory  string
	Config            config.Config
	Metadata          map[string]any
	Environment       map[string]string
	Directories       Directories
	Timeout           time.Duration
	HeartbeatInterval time.Duration
	CloseOnFinish     bool
	CloseOnFailure    bool
}

func NewContext(worker state.Worker, cfg config.Config, environment map[string]string) (Context, error) {
	workerDir := filepath.Dir(worker.RecordPath)
	workingDirectory := worker.RepositoryPath
	sourceMetadata := worker.Metadata
	if len(worker.ResolvedMetadata) > 0 {
		sourceMetadata = worker.ResolvedMetadata
	}
	if value, ok := sourceMetadata["working_directory"].(string); ok && value != "" {
		resolved, err := resolveWorkingDirectory(worker.RepositoryPath, value)
		if err != nil {
			return Context{}, err
		}
		workingDirectory = resolved
	}
	timeout, err := TimeoutFromMetadata(sourceMetadata)
	if err != nil {
		return Context{}, err
	}
	metadata := cloneAnyMap(sourceMetadata)
	if _, ok := metadata["timeout"]; !ok && cfg.WorkerTimeout > 0 {
		metadata["timeout"] = cfg.WorkerTimeout.String()
	}
	return Context{
		RepositoryRoot:   worker.RepositoryPath,
		TaskPath:         worker.TaskPath,
		WorkerID:         worker.ID,
		WorkingDirectory: workingDirectory,
		Config:           cfg,
		Metadata:         metadata,
		Environment:      cloneMap(environment),
		Directories: Directories{
			Worker: workerDir,
			Prompt: worker.PromptPath,
			Script: filepath.Join(workerDir, "run.sh"),
			Log:    worker.LogPath,
		},
		Timeout:           timeout,
		HeartbeatInterval: cfg.WorkerHeartbeatInterval,
		CloseOnFinish:     worker.CloseOnFinish,
		CloseOnFailure:    worker.CloseOnFailure,
	}, nil
}

func resolveWorkingDirectory(repositoryPath, value string) (string, error) {
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("working_directory must be relative to the repository root: %s", value)
	}
	base := filepath.Clean(repositoryPath)
	target := filepath.Clean(filepath.Join(base, value))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("working_directory must stay within the repository: %s", value)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working_directory must stay within the repository: %s", value)
	}
	return target, nil
}

func TimeoutFromMetadata(metadata map[string]any) (time.Duration, error) {
	value, ok := metadata["timeout"]
	if !ok || value == nil {
		return 0, nil
	}
	text, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("timeout must be a duration string like 30s, 10m, or 2h")
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: use values like 30s, 10m, or 2h", text)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid timeout %q: must be greater than zero", text)
	}
	return duration, nil
}

func cloneMap(input map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		out[key] = value
	}
	return out
}
