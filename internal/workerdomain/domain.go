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
// Later slices add persistence behavior and additional state context.
type WorkerState struct {
	Version   int       `json:"version" yaml:"version"`
	ProjectID string    `json:"project_id" yaml:"project_id"`
	WorkerID  string    `json:"worker_id" yaml:"worker_id"`
	Lifecycle Lifecycle `json:"lifecycle" yaml:"lifecycle"`
}

// Task is the versioned locally persisted assigned-task identity. Task
// lifecycle and snapshots are introduced by the task-assignment slice.
type Task struct {
	Version   int    `json:"version" yaml:"version"`
	ID        string `json:"id" yaml:"id"`
	ProjectID string `json:"project_id" yaml:"project_id"`
	WorkerID  string `json:"worker_id" yaml:"worker_id"`
	Source    string `json:"source" yaml:"source"`
}

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
	if err := validateRepositoryPath("branch prefix", p.BranchPrefix, false); err != nil {
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
	if err := validateRepositoryPath("task source", t.Source, false); err != nil {
		return err
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
