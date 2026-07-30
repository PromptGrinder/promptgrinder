// Package workerregistry discovers and loads repository-owned named worker
// definitions. It is read-only and does not interact with execution-run state.
package workerregistry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"promptgrinder/internal/repository"
	"promptgrinder/internal/workerdomain"

	"gopkg.in/yaml.v3"
)

const RegistryPath = ".ai/workers.yaml"

var ErrWorkerNotFound = errors.New("worker definition not found")

// Registry is a validated project-owned worker registry.
type Registry struct {
	Root    string
	Version int
	Project workerdomain.Project
	Workers map[string]workerdomain.WorkerDefinition
}

type registryFile struct {
	Version int                             `yaml:"version"`
	Project workerdomain.Project            `yaml:"project"`
	Workers map[string]workerDefinitionFile `yaml:"workers"`
}

type workerDefinitionFile struct {
	DisplayName string       `yaml:"display_name"`
	Role        string       `yaml:"role"`
	Runtime     string       `yaml:"runtime"`
	Branch      branchFile   `yaml:"branch"`
	Worktree    worktreeFile `yaml:"worktree"`
	Paths       pathsFile    `yaml:"paths"`
}

type branchFile struct {
	Prefix string `yaml:"prefix"`
}

type worktreeFile struct {
	Default string `yaml:"default"`
}

type pathsFile struct {
	Allowed   []string `yaml:"allowed"`
	Forbidden []string `yaml:"forbidden"`
}

// Load discovers the repository containing start and loads its registry.
func Load(start string) (*Registry, error) {
	root, err := repository.DetectRoot(start)
	if err != nil {
		return nil, fmt.Errorf("discover repository: %w", err)
	}
	registryPath := filepath.Join(root, filepath.FromSlash(RegistryPath))
	data, err := os.ReadFile(registryPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("worker registry %s is missing", registryPath)
		}
		return nil, fmt.Errorf("read worker registry %s: %w", registryPath, err)
	}

	var file registryFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse worker registry %s: %w", registryPath, err)
	}

	registry := &Registry{
		Root:    root,
		Version: file.Version,
		Project: file.Project,
		Workers: make(map[string]workerdomain.WorkerDefinition, len(file.Workers)),
	}
	domainRegistry := workerdomain.WorkerRegistry{
		Version: file.Version,
		Project: file.Project,
		Workers: make([]workerdomain.WorkerDefinition, 0, len(file.Workers)),
	}
	for id, item := range file.Workers {
		definition := workerdomain.WorkerDefinition{
			ID:          id,
			DisplayName: item.DisplayName,
			Role:        item.Role,
			ProjectID:   file.Project.ID,
			Runtime:     workerdomain.RuntimeRef{Name: item.Runtime},
			Policy: workerdomain.WorkerPolicy{
				BranchPrefix:    item.Branch.Prefix,
				DefaultWorktree: item.Worktree.Default,
				AllowedPaths:    normalizedPaths(item.Paths.Allowed),
				ForbiddenPaths:  normalizedPaths(item.Paths.Forbidden),
			},
		}
		registry.Workers[id] = definition
		domainRegistry.Workers = append(domainRegistry.Workers, definition)
	}
	if err := domainRegistry.Validate(); err != nil {
		return nil, fmt.Errorf("validate worker registry %s: %w", registryPath, err)
	}
	return registry, nil
}

func normalizedPaths(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ReplaceAll(value, `\`, "/")
	}
	return result
}

// List returns worker definitions in stable ID order.
func (r *Registry) List() []workerdomain.WorkerDefinition {
	workers := make([]workerdomain.WorkerDefinition, 0, len(r.Workers))
	for _, worker := range r.Workers {
		workers = append(workers, worker)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	return workers
}

// Get returns one named worker definition.
func (r *Registry) Get(id string) (workerdomain.WorkerDefinition, error) {
	worker, ok := r.Workers[id]
	if !ok {
		return workerdomain.WorkerDefinition{}, fmt.Errorf("%w: %q", ErrWorkerNotFound, id)
	}
	return worker, nil
}
