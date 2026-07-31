package workerdomain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func validRegistry() WorkerRegistry {
	return WorkerRegistry{
		Version: SchemaVersion,
		Project: Project{ID: "footybadger", Name: "FootyBadger"},
		Workers: []WorkerDefinition{{
			ID:          "backend-sonar",
			DisplayName: "Backend Sonar Engineer",
			Role:        "Resolve backend static-analysis findings safely.",
			ProjectID:   "footybadger",
			Runtime:     RuntimeRef{Name: "codex"},
			Policy: WorkerPolicy{
				BranchPrefix:    "worker/backend-sonar",
				DefaultWorktree: ".",
				AllowedPaths:    []string{"backend/**"},
				ForbiddenPaths:  []string{"infrastructure/production/**"},
			},
		}},
	}
}

func TestPersistedSchemasRoundTripJSONAndYAML(t *testing.T) {
	values := []any{
		validRegistry(),
		WorkerState{
			Version: SchemaVersion, Revision: 1,
			ProjectID: "footybadger", WorkerID: "backend-sonar",
			Lifecycle: LifecycleExecuting,
			CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
			LifecycleChangedAt: time.Unix(2, 0).UTC(),
			EffectivePolicy:    validRegistry().Workers[0].Policy,
		},
		Task{
			Version: SchemaVersion, ID: "sonar-001",
			ProjectID: "footybadger", WorkerID: "backend-sonar",
			Instructions: "Fix sonar.", ContentSnapshot: "# Fix sonar.\n",
			SourceReference: "tasks/sonar-001.md", Status: TaskStatusAssigned,
			CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}

	for _, value := range values {
		t.Run(reflect.TypeOf(value).Name(), func(t *testing.T) {
			jsonData, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			jsonResult := reflect.New(reflect.TypeOf(value))
			if err := json.Unmarshal(jsonData, jsonResult.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, jsonResult.Elem().Interface()) {
				t.Fatalf("JSON round trip = %#v, want %#v", jsonResult.Elem().Interface(), value)
			}

			yamlData, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			yamlResult := reflect.New(reflect.TypeOf(value))
			if err := yaml.Unmarshal(yamlData, yamlResult.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, yamlResult.Elem().Interface()) {
				t.Fatalf("YAML round trip = %#v, want %#v", yamlResult.Elem().Interface(), value)
			}
		})
	}
}

func TestWorkerRegistryValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkerRegistry)
		want   string
	}{
		{
			name:   "unknown version",
			mutate: func(r *WorkerRegistry) { r.Version = 2 },
			want:   "unsupported",
		},
		{
			name:   "invalid project id",
			mutate: func(r *WorkerRegistry) { r.Project.ID = "Not Stable" },
			want:   "project id",
		},
		{
			name:   "missing project name",
			mutate: func(r *WorkerRegistry) { r.Project.Name = "" },
			want:   "project name",
		},
		{
			name:   "invalid worker id",
			mutate: func(r *WorkerRegistry) { r.Workers[0].ID = "backend_sonar" },
			want:   "worker id",
		},
		{
			name:   "unstable worker id",
			mutate: func(r *WorkerRegistry) { r.Workers[0].ID = "backend--sonar" },
			want:   "worker id",
		},
		{
			name:   "missing display name",
			mutate: func(r *WorkerRegistry) { r.Workers[0].DisplayName = "" },
			want:   "display name",
		},
		{
			name:   "missing role",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Role = "" },
			want:   "role",
		},
		{
			name:   "project mismatch",
			mutate: func(r *WorkerRegistry) { r.Workers[0].ProjectID = "another-project" },
			want:   "does not match",
		},
		{
			name: "duplicate worker",
			mutate: func(r *WorkerRegistry) {
				r.Workers = append(r.Workers, r.Workers[0])
			},
			want: "duplicate worker",
		},
		{
			name:   "invalid runtime",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Runtime.Name = "Codex CLI" },
			want:   "runtime name",
		},
		{
			name:   "absolute worktree",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Policy.DefaultWorktree = "/tmp/repo" },
			want:   "repository-relative",
		},
		{
			name:   "windows absolute worktree",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Policy.DefaultWorktree = `C:\repo` },
			want:   "repository-relative",
		},
		{
			name:   "escaping allowed path",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Policy.AllowedPaths = []string{"../secrets/**"} },
			want:   "escapes the repository",
		},
		{
			name:   "malformed forbidden pattern",
			mutate: func(r *WorkerRegistry) { r.Workers[0].Policy.ForbiddenPaths = []string{"backend/["} },
			want:   "invalid glob",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := validRegistry()
			test.mutate(&registry)
			err := registry.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWorkerRegistryValid(t *testing.T) {
	if err := validRegistry().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStateAndTaskValidation(t *testing.T) {
	state := WorkerState{
		Version: SchemaVersion, Revision: 1,
		ProjectID: "footybadger", WorkerID: "backend-sonar",
		Lifecycle: LifecycleIdle,
		CreatedAt: time.Now(), UpdatedAt: time.Now(), LifecycleChangedAt: time.Now(),
		EffectivePolicy: validRegistry().Workers[0].Policy,
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	state.Version = 99
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("state error = %v", err)
	}

	task := Task{
		Version: SchemaVersion, ID: "sonar-001",
		ProjectID: "footybadger", WorkerID: "backend-sonar",
		Instructions: "Fix sonar.", ContentSnapshot: "# Fix sonar.\n",
		SourceReference: "tasks/sonar-001.md", Status: TaskStatusAssigned,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	task.SourceReference = "../../outside.md"
	if err := task.Validate(); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("task error = %v", err)
	}
}
