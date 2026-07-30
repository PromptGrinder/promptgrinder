// Package workerdomain defines runtime-neutral contracts for persistent named
// workers. These types are deliberately separate from state.Worker, which is
// an execution-run record created for one invocation.
package workerdomain

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	slugPattern        = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	windowsDrivePrefix = regexp.MustCompile(`^[A-Za-z]:[/\\]`)
)

// Project is the stable identity of one repository-scoped PromptGrinder
// project.
type Project struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

// RuntimeRef selects an execution runtime by symbolic registry key.
type RuntimeRef struct {
	Name string `json:"name" yaml:"name"`
}

// WorkerPolicy contains repository-owned constraints and working-location
// defaults for a persistent named worker.
type WorkerPolicy struct {
	BranchPrefix    string   `json:"branch_prefix" yaml:"branch_prefix"`
	DefaultWorktree string   `json:"default_worktree" yaml:"default_worktree"`
	RequireClean    bool     `json:"require_clean" yaml:"require_clean"`
	AllowedPaths    []string `json:"allowed_paths,omitempty" yaml:"allowed_paths,omitempty"`
	ForbiddenPaths  []string `json:"forbidden_paths,omitempty" yaml:"forbidden_paths,omitempty"`
}

// WorkerDefinition is the repository-owned identity and policy for a
// long-lived project role. Tasks cannot alter these fields.
type WorkerDefinition struct {
	ID          string       `json:"id" yaml:"id"`
	DisplayName string       `json:"display_name" yaml:"display_name"`
	Role        string       `json:"role" yaml:"role"`
	ProjectID   string       `json:"project_id" yaml:"project_id"`
	Runtime     RuntimeRef   `json:"runtime" yaml:"runtime"`
	Policy      WorkerPolicy `json:"policy" yaml:"policy"`
}

// WorkerRegistry is the versioned repository-owned .ai/workers.yaml schema.
type WorkerRegistry struct {
	Version int                `json:"version" yaml:"version"`
	Project Project            `json:"project" yaml:"project"`
	Workers []WorkerDefinition `json:"workers" yaml:"workers"`
}

// Lifecycle is PromptGrinder's authoritative state for a persistent named
// worker, not a status reported authoritatively by a runtime.
type Lifecycle string

const (
	LifecycleIdle           Lifecycle = "idle"
	LifecycleStarting       Lifecycle = "starting"
	LifecycleExecuting      Lifecycle = "executing"
	LifecycleBlocked        Lifecycle = "blocked"
	LifecycleAwaitingReview Lifecycle = "awaiting_review"
	LifecycleFailed         Lifecycle = "failed"
)

// WorkerState is the versioned locally persisted named-worker state contract.
type WorkerState struct {
	Version             int          `json:"version" yaml:"version"`
	Revision            uint64       `json:"revision" yaml:"revision"`
	ProjectID           string       `json:"project_id" yaml:"project_id"`
	WorkerID            string       `json:"worker_id" yaml:"worker_id"`
	Lifecycle           Lifecycle    `json:"lifecycle" yaml:"lifecycle"`
	ActiveTaskID        string       `json:"active_task_id,omitempty" yaml:"active_task_id,omitempty"`
	ActiveRunID         string       `json:"active_run_id,omitempty" yaml:"active_run_id,omitempty"`
	RuntimeSession      *SessionRef  `json:"runtime_session,omitempty" yaml:"runtime_session,omitempty"`
	CreatedAt           time.Time    `json:"created_at" yaml:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at" yaml:"updated_at"`
	LifecycleChangedAt  time.Time    `json:"lifecycle_changed_at" yaml:"lifecycle_changed_at"`
	FailureReason       string       `json:"failure_reason,omitempty" yaml:"failure_reason,omitempty"`
	BlockReason         string       `json:"block_reason,omitempty" yaml:"block_reason,omitempty"`
	LastCompletedTaskID string       `json:"last_completed_task_id,omitempty" yaml:"last_completed_task_id,omitempty"`
	EffectivePolicy     WorkerPolicy `json:"effective_policy" yaml:"effective_policy"`
	Worktree            string       `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	Branch              string       `json:"branch,omitempty" yaml:"branch,omitempty"`
	BaseBranch          string       `json:"base_branch,omitempty" yaml:"base_branch,omitempty"`
	BaseRevision        string       `json:"base_revision,omitempty" yaml:"base_revision,omitempty"`
}

// SessionRef identifies a runtime-owned session without granting the runtime
// authority over PromptGrinder's lifecycle state.
type SessionRef struct {
	Runtime   string `json:"runtime" yaml:"runtime"`
	SessionID string `json:"session_id" yaml:"session_id"`
}

// Task is the versioned locally persisted assigned-task identity. Task
// lifecycle and snapshots are introduced by the task-assignment slice.
type Task struct {
	Version         int        `json:"version" yaml:"version"`
	ID              string     `json:"id" yaml:"id"`
	ProjectID       string     `json:"project_id" yaml:"project_id"`
	WorkerID        string     `json:"worker_id" yaml:"worker_id"`
	Instructions    string     `json:"instructions" yaml:"instructions"`
	ContentSnapshot string     `json:"content_snapshot" yaml:"content_snapshot"`
	SourceReference string     `json:"source_reference" yaml:"source_reference"`
	Status          TaskStatus `json:"status" yaml:"status"`
	AttemptCount    int        `json:"attempt_count" yaml:"attempt_count"`
	CreatedAt       time.Time  `json:"created_at" yaml:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" yaml:"updated_at"`
	Worktree        string     `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	Branch          string     `json:"branch,omitempty" yaml:"branch,omitempty"`
	BaseBranch      string     `json:"base_branch,omitempty" yaml:"base_branch,omitempty"`
	BaseRevision    string     `json:"base_revision,omitempty" yaml:"base_revision,omitempty"`
	LaunchSetup     string     `json:"launch_setup,omitempty" yaml:"launch_setup,omitempty"`
}

type TaskStatus string

const TaskStatusAssigned TaskStatus = "assigned"

func (s TaskStatus) Valid() bool { return s == TaskStatusAssigned }

func ValidateSlug(kind, value string) error {
	if len(value) > 63 || !slugPattern.MatchString(value) {
		return fmt.Errorf("%s %q must be a lowercase project-scoped slug (letters, digits, and single hyphen-separated words; maximum 63 characters)", kind, value)
	}
	return nil
}

func (p Project) Validate() error {
	if err := ValidateSlug("project id", p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("project name is required")
	}
	return nil
}

func (r RuntimeRef) Validate() error {
	if err := ValidateSlug("runtime name", r.Name); err != nil {
		return err
	}
	return nil
}

func (p WorkerPolicy) Validate() error {
	if p.BranchPrefix == "" {
		return fmt.Errorf("branch prefix is required")
	}
	if err := validateBranchPrefix(p.BranchPrefix); err != nil {
		return err
	}
	if p.DefaultWorktree == "" {
		return fmt.Errorf("default worktree is required")
	}
	if err := validateRepositoryPath("default worktree", p.DefaultWorktree, false); err != nil {
		return err
	}
	for _, value := range p.AllowedPaths {
		if err := validateRepositoryPath("allowed path", value, true); err != nil {
			return err
		}
	}
	for _, value := range p.ForbiddenPaths {
		if err := validateRepositoryPath("forbidden path", value, true); err != nil {
			return err
		}
	}
	return nil
}

func validateBranchPrefix(value string) error {
	if err := validateRepositoryPath("branch prefix", value, false); err != nil {
		return err
	}
	if value == "." || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.HasSuffix(value, ".lock") ||
		strings.ContainsAny(value, `\ ~^:?*[]`) {
		return fmt.Errorf("branch prefix %q is invalid", value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("branch prefix %q is invalid", value)
		}
	}
	return nil
}

func (w WorkerDefinition) Validate() error {
	if err := ValidateSlug("worker id", w.ID); err != nil {
		return err
	}
	if strings.TrimSpace(w.DisplayName) == "" {
		return fmt.Errorf("worker %q display name is required", w.ID)
	}
	if strings.TrimSpace(w.Role) == "" {
		return fmt.Errorf("worker %q role is required", w.ID)
	}
	if err := ValidateSlug("project id", w.ProjectID); err != nil {
		return err
	}
	if err := w.Runtime.Validate(); err != nil {
		return fmt.Errorf("worker %q: %w", w.ID, err)
	}
	if err := w.Policy.Validate(); err != nil {
		return fmt.Errorf("worker %q: %w", w.ID, err)
	}
	return nil
}

func (r WorkerRegistry) Validate() error {
	if err := validateVersion("worker registry", r.Version); err != nil {
		return err
	}
	if err := r.Project.Validate(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.Workers))
	for _, worker := range r.Workers {
		if err := worker.Validate(); err != nil {
			return err
		}
		if worker.ProjectID != r.Project.ID {
			return fmt.Errorf("worker %q project id %q does not match registry project %q", worker.ID, worker.ProjectID, r.Project.ID)
		}
		if _, ok := seen[worker.ID]; ok {
			return fmt.Errorf("duplicate worker id %q", worker.ID)
		}
		seen[worker.ID] = struct{}{}
	}
	return nil
}

func (s WorkerState) Validate() error {
	if err := validateVersion("worker state", s.Version); err != nil {
		return err
	}
	if err := ValidateSlug("project id", s.ProjectID); err != nil {
		return err
	}
	if err := ValidateSlug("worker id", s.WorkerID); err != nil {
		return err
	}
	if !s.Lifecycle.Valid() {
		return fmt.Errorf("unknown worker lifecycle %q", s.Lifecycle)
	}
	if s.Revision == 0 {
		return fmt.Errorf("worker state revision must be positive")
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.LifecycleChangedAt.IsZero() {
		return fmt.Errorf("worker state timestamps are required")
	}
	if s.RuntimeSession != nil {
		if err := ValidateSlug("runtime name", s.RuntimeSession.Runtime); err != nil {
			return err
		}
		if strings.TrimSpace(s.RuntimeSession.SessionID) == "" {
			return fmt.Errorf("runtime session id is required")
		}
	}
	if err := s.EffectivePolicy.Validate(); err != nil {
		return fmt.Errorf("effective worker policy: %w", err)
	}
	return nil
}

func (t Task) Validate() error {
	if err := validateVersion("task", t.Version); err != nil {
		return err
	}
	if err := ValidateSlug("task id", t.ID); err != nil {
		return err
	}
	if err := ValidateSlug("project id", t.ProjectID); err != nil {
		return err
	}
	if err := ValidateSlug("worker id", t.WorkerID); err != nil {
		return err
	}
	if strings.TrimSpace(t.Instructions) == "" {
		return fmt.Errorf("task instructions are required")
	}
	if strings.TrimSpace(t.ContentSnapshot) == "" {
		return fmt.Errorf("task content snapshot is required")
	}
	if err := validateRepositoryPath("task source reference", t.SourceReference, false); err != nil {
		return err
	}
	if !t.Status.Valid() {
		return fmt.Errorf("unknown task status %q", t.Status)
	}
	if t.AttemptCount < 0 {
		return fmt.Errorf("task attempt count must not be negative")
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("task timestamps are required")
	}
	return nil
}

func validateVersion(schema string, version int) error {
	if version != SchemaVersion {
		return fmt.Errorf("%s schema version %d is unsupported; want %d", schema, version, SchemaVersion)
	}
	return nil
}

func validateRepositoryPath(kind, value string, allowPattern bool) error {
	if value == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if filepath.IsAbs(value) || path.IsAbs(strings.ReplaceAll(value, `\`, "/")) || windowsDrivePrefix.MatchString(value) {
		return fmt.Errorf("%s %q must be repository-relative", kind, value)
	}
	slashed := strings.ReplaceAll(value, `\`, "/")
	for _, part := range strings.Split(slashed, "/") {
		if part == ".." {
			return fmt.Errorf("%s %q escapes the repository", kind, value)
		}
	}
	if !allowPattern && strings.ContainsAny(value, "*?[") {
		return fmt.Errorf("%s %q must not contain a glob pattern", kind, value)
	}
	if allowPattern {
		if _, err := path.Match(slashed, slashed); err != nil {
			return fmt.Errorf("%s %q has an invalid glob pattern: %w", kind, value, err)
		}
	}
	return nil
}
