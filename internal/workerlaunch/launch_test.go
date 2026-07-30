package workerlaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"promptgrinder/internal/workerdomain"
)

func testOptions(root string) BuildOptions {
	return BuildOptions{
		Project: workerdomain.Project{ID: "example", Name: "Example"},
		Worker: workerdomain.WorkerDefinition{
			ID: "backend-sonar", DisplayName: "Backend Sonar", ProjectID: "example",
			Role:    "Resolve backend findings safely.",
			Runtime: workerdomain.RuntimeRef{Name: "claude"},
			Policy: workerdomain.WorkerPolicy{
				BranchPrefix: "worker/backend-sonar", DefaultWorktree: ".",
				AllowedPaths:   []string{"services/**", "backend/**"},
				ForbiddenPaths: []string{"secrets/**", "infrastructure/production/**"},
			},
		},
		Repository:     root,
		Task:           TaskContext{ID: "sonar-001", Source: "tasks/sonar-001.md", Instructions: "Fix the reported finding."},
		RuntimeDefault: "local",
		RuntimeOptions: map[string]map[string]any{
			"codex": {"model": "gpt-5", "api_token": "supersecret", "nested": map[string]any{"password": "hidden", "safe": "visible"}},
		},
	}
}

func TestContextDocumentGolden(t *testing.T) {
	request := LaunchRequest{
		Project: workerdomain.Project{ID: "example", Name: "Example"},
		Worker:  testOptions("/repo").Worker, Repository: "/repo", Worktree: "/repo",
		Task: testOptions("/repo").Task, Policy: testOptions("/repo").Worker.Policy,
		Runtime: RuntimeConfig{Name: "claude"},
	}
	assertGolden(t, "testdata/context.golden", ContextDocument(request))
}

func TestPlanDocumentGolden(t *testing.T) {
	options := testOptions("/repo")
	request := LaunchRequest{
		Project: options.Project, Worker: options.Worker, Repository: "/repo", Worktree: "/repo",
		Task: options.Task, Policy: options.Worker.Policy,
		Runtime: RuntimeConfig{Name: "codex", Options: options.RuntimeOptions["codex"]},
	}
	request.Context = ContextDocument(request)
	assertGolden(t, "testdata/plan.golden", PlanDocument(request))
}

func TestBuildRuntimePrecedence(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, override, worker, fallback, want string
	}{
		{name: "override", override: "codex", worker: "claude", fallback: "local", want: "codex"},
		{name: "worker", worker: "claude", fallback: "local", want: "claude"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions(root)
			options.RuntimeOverride = test.override
			options.Worker.Runtime.Name = test.worker
			options.RuntimeDefault = test.fallback
			request, err := Build(options)
			if err != nil {
				t.Fatal(err)
			}
			if request.Runtime.Name != test.want {
				t.Fatalf("runtime = %q, want %q", request.Runtime.Name, test.want)
			}
		})
	}
}

func TestRedactedRecursivelyRemovesSecrets(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RuntimeOverride = "codex"
	request, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanDocument(request)
	if strings.Contains(plan, "supersecret") || strings.Contains(plan, "hidden") {
		t.Fatalf("plan leaked a secret: %s", plan)
	}
	for _, want := range []string{`api_token: "[redacted]"`, `"password":"[redacted]"`, `"safe":"visible"`} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan missing %q: %s", want, plan)
		}
	}
	if request.Runtime.Options["api_token"] != "supersecret" {
		t.Fatal("redaction mutated the launch request")
	}
}

func TestBuildRejectsMissingWorktree(t *testing.T) {
	options := testOptions(t.TempDir())
	options.Worker.Policy.DefaultWorktree = "missing"
	if _, err := Build(options); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("error = %v", err)
	}
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}
