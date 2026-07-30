// Package workerlaunch converts persistent named-worker definitions into
// runtime-neutral launch requests. It has no dependency on runtime adapters.
package workerlaunch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"promptgrinder/internal/workerdomain"
)

// Launcher is implemented by replaceable runtime adapters.
type Launcher interface {
	Launch(context.Context, LaunchRequest) (LaunchResult, error)
}

// Capabilities are explicitly advertised by an adapter. A missing capability
// must never be inferred from the presence of a runtime session identifier.
type Capabilities struct {
	SessionResume bool `json:"session_resume"`
}

type CapabilityProvider interface {
	Capabilities() Capabilities
}

// Resumer continues a runtime-owned session. Core orchestration calls it only
// when CapabilityProvider explicitly advertises SessionResume.
type Resumer interface {
	Resume(context.Context, LaunchRequest, string) (LaunchResult, error)
}

// Preflighter validates a launch without creating records or processes.
type Preflighter interface {
	Preflight(context.Context, LaunchRequest) error
}

// TaskContext describes the task assigned by PromptGrinder. It is empty until
// the task-assignment slice provides persisted assignments.
type TaskContext struct {
	ID           string `json:"id,omitempty"`
	Source       string `json:"source,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// RuntimeConfig keeps adapter-specific configuration namespaced by symbolic
// runtime name and opaque to the core launch package.
type RuntimeConfig struct {
	Name    string         `json:"name"`
	Options map[string]any `json:"options,omitempty"`
}

// LaunchRequest contains all PromptGrinder-owned context needed by a runtime.
type LaunchRequest struct {
	Project      workerdomain.Project          `json:"project"`
	Worker       workerdomain.WorkerDefinition `json:"worker"`
	Repository   string                        `json:"repository"`
	Worktree     string                        `json:"worktree"`
	Branch       string                        `json:"branch,omitempty"`
	BaseBranch   string                        `json:"base_branch,omitempty"`
	BaseRevision string                        `json:"base_revision,omitempty"`
	Task         TaskContext                   `json:"task"`
	Policy       workerdomain.WorkerPolicy     `json:"policy"`
	Runtime      RuntimeConfig                 `json:"runtime"`
	Context      string                        `json:"context"`
}

// LaunchResult reports runtime facts without granting the runtime ownership of
// named-worker lifecycle.
type LaunchResult struct {
	RuntimeName string               `json:"runtime_name"`
	RunID       string               `json:"run_id"`
	Session     RuntimeSessionResult `json:"session"`
	Process     RuntimeProcessResult `json:"process"`
}

// RuntimeSessionResult contains identifiers reported by a runtime adapter.
type RuntimeSessionResult struct {
	ID string `json:"id,omitempty"`
}

// RuntimeProcessResult contains observable operating-system process facts.
type RuntimeProcessResult struct {
	PID        int        `json:"pid,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BuildOptions are resolved by the CLI before any launcher is selected.
type BuildOptions struct {
	Project         workerdomain.Project
	Worker          workerdomain.WorkerDefinition
	Repository      string
	Task            TaskContext
	RuntimeOverride string
	RuntimeDefault  string
	RuntimeOptions  map[string]map[string]any
	Branch          string
	BaseBranch      string
	BaseRevision    string
}

// Build resolves and validates a deterministic launch request.
func Build(options BuildOptions) (LaunchRequest, error) {
	runtimeName := firstNonEmpty(options.RuntimeOverride, options.Worker.Runtime.Name, options.RuntimeDefault)
	if err := (workerdomain.RuntimeRef{Name: runtimeName}).Validate(); err != nil {
		return LaunchRequest{}, fmt.Errorf("resolve runtime: %w", err)
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return LaunchRequest{}, fmt.Errorf("resolve repository: %w", err)
	}
	repository = filepath.Clean(repository)
	worktree := filepath.Clean(filepath.Join(repository, filepath.FromSlash(options.Worker.Policy.DefaultWorktree)))
	relative, err := filepath.Rel(repository, worktree)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return LaunchRequest{}, fmt.Errorf("worker %q worktree escapes repository", options.Worker.ID)
	}
	info, err := os.Stat(worktree)
	if err != nil {
		return LaunchRequest{}, fmt.Errorf("validate worker %q worktree %s: %w", options.Worker.ID, worktree, err)
	}
	if !info.IsDir() {
		return LaunchRequest{}, fmt.Errorf("validate worker %q worktree %s: not a directory", options.Worker.ID, worktree)
	}
	if options.Project.ID != options.Worker.ProjectID {
		return LaunchRequest{}, fmt.Errorf("worker %q does not belong to project %q", options.Worker.ID, options.Project.ID)
	}
	if err := options.Project.Validate(); err != nil {
		return LaunchRequest{}, err
	}
	if err := options.Worker.Validate(); err != nil {
		return LaunchRequest{}, err
	}
	request := LaunchRequest{
		Project: options.Project, Worker: options.Worker,
		Repository: repository, Worktree: worktree, Task: options.Task,
		Branch: options.Branch, BaseBranch: options.BaseBranch, BaseRevision: options.BaseRevision,
		Policy:  options.Worker.Policy,
		Runtime: RuntimeConfig{Name: runtimeName, Options: cloneMap(options.RuntimeOptions[runtimeName])},
	}
	request.Context = ContextDocument(request)
	return request, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ContextDocument produces stable runtime instructions from a launch request.
func ContextDocument(request LaunchRequest) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# PromptGrinder Worker Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: %s (%s)\n", request.Project.Name, request.Project.ID)
	fmt.Fprintf(&b, "Worker: %s (%s)\n", request.Worker.DisplayName, request.Worker.ID)
	fmt.Fprintf(&b, "Responsibility: %s\n", request.Worker.Role)
	fmt.Fprintf(&b, "Repository: %s\n", request.Repository)
	fmt.Fprintf(&b, "Worktree: %s\n", request.Worktree)
	if request.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", request.Branch)
		fmt.Fprintf(&b, "Base branch: %s\n", request.BaseBranch)
		fmt.Fprintf(&b, "Base revision: %s\n", request.BaseRevision)
	}
	fmt.Fprintf(&b, "Allowed paths: %s\n", pathList(request.Policy.AllowedPaths))
	fmt.Fprintf(&b, "Forbidden paths: %s\n", pathList(request.Policy.ForbiddenPaths))
	if request.Task.ID == "" && request.Task.Source == "" && request.Task.Instructions == "" {
		fmt.Fprintln(&b, "Assigned task: none")
	} else {
		fmt.Fprintf(&b, "Assigned task: %s\n", valueOrNone(request.Task.ID))
		fmt.Fprintf(&b, "Task source: %s\n", valueOrNone(request.Task.Source))
		fmt.Fprintf(&b, "Task instructions: %s\n", valueOrNone(request.Task.Instructions))
	}
	return b.String()
}

func pathList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return strings.Join(copyValues, ", ")
}

func valueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// Redacted returns a copy safe for previews and structured diagnostics.
func Redacted(request LaunchRequest) LaunchRequest {
	request.Runtime.Options = redactMap(request.Runtime.Options)
	return request
}

func redactMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		if isSecretKey(key) {
			result[key] = "[redacted]"
		} else {
			result[key] = redactValue(value)
		}
	}
	return result
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(item)
		}
		return result
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "PASS", "KEY", "PRIVATE", "CREDENTIAL", "AUTH", "COOKIE"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	data, _ := json.Marshal(values)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

// PlanDocument renders the complete reviewable, redacted dry-run plan.
func PlanDocument(request LaunchRequest) string {
	safe := Redacted(request)
	var b strings.Builder
	fmt.Fprintln(&b, "Launch plan (dry run)")
	fmt.Fprintf(&b, "Project: %s (%s)\n", safe.Project.Name, safe.Project.ID)
	fmt.Fprintf(&b, "Worker: %s (%s)\n", safe.Worker.DisplayName, safe.Worker.ID)
	fmt.Fprintf(&b, "Runtime: %s\n", safe.Runtime.Name)
	fmt.Fprintf(&b, "Repository: %s\n", safe.Repository)
	fmt.Fprintf(&b, "Worktree: %s\n", safe.Worktree)
	fmt.Fprintf(&b, "Branch prefix: %s\n", safe.Policy.BranchPrefix)
	fmt.Fprintf(&b, "Allowed paths: %s\n", pathList(safe.Policy.AllowedPaths))
	fmt.Fprintf(&b, "Forbidden paths: %s\n", pathList(safe.Policy.ForbiddenPaths))
	fmt.Fprintf(&b, "Assigned task: %s\n", valueOrNone(safe.Task.ID))
	fmt.Fprintln(&b, "Runtime options:")
	if len(safe.Runtime.Options) == 0 {
		fmt.Fprintln(&b, "  none")
	} else {
		keys := make([]string, 0, len(safe.Runtime.Options))
		for key := range safe.Runtime.Options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			data, _ := json.Marshal(safe.Runtime.Options[key])
			fmt.Fprintf(&b, "  %s: %s\n", key, data)
		}
	}
	fmt.Fprintln(&b, "Injected context:")
	fmt.Fprint(&b, safe.Context)
	fmt.Fprintln(&b, "No process started. No mutable state created.")
	return b.String()
}
