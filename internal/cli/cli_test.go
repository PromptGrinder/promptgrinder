package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/buildinfo"
	"promptgrinder/internal/config"
	"promptgrinder/internal/discovery"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/firstuse"
	"promptgrinder/internal/roleenhance"
	"promptgrinder/internal/runfolder"
	pgruntime "promptgrinder/internal/runtime"
	"promptgrinder/internal/state"
	"promptgrinder/internal/worker"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerstate"
)

func TestCLIVersion(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "promptgrinder " + buildinfo.String() + "\n"
	if out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}

type ttyBuffer struct {
	bytes.Buffer
}

func (b *ttyBuffer) IsTerminal() bool {
	return true
}

type ttyInput struct{ *strings.Reader }

func (ttyInput) IsTerminal() bool { return true }

func TestCLIHelpUsesPromptGrinder(t *testing.T) {
	service := &fakeService{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, errOut)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("promptgrinder")) {
		t.Fatalf("help = %q", out.String())
	}
}

func TestCLISetupDryRunReportsPreviewInsteadOfAlreadyComplete(t *testing.T) {
	home := filepath.Join(t.TempDir(), "new-home")
	service := &fakeService{defaultsReport: config.DefaultsReport{HomeDir: home}}
	out := &bytes.Buffer{}
	scan := func(context.Context, firstuse.DoctorOptions) firstuse.DoctorReport {
		return firstuse.DoctorReport{OK: true, Checks: []firstuse.Check{{ID: "tool.codex", Status: firstuse.Pass, Summary: "Codex CLI is executable."}}}
	}
	cmd := newRootCommandWithDependencies(service, out, &bytes.Buffer{}, os.Getwd, discovery.Discover, nil, scan)
	cmd.SetArgs([]string{"setup", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Machine capabilities:") || !strings.Contains(out.String(), "tool.codex") || !strings.Contains(out.String(), "Setup proposal:") || !strings.Contains(out.String(), "Setup preview complete; planned changes were not written.") || strings.Contains(out.String(), "already complete") {
		t.Fatalf("setup output = %q", out.String())
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote home: %v", err)
	}
}

func TestCLISetupJSONIncludesMachineCapabilities(t *testing.T) {
	home := filepath.Join(t.TempDir(), "new-home")
	service := &fakeService{defaultsReport: config.DefaultsReport{HomeDir: home}}
	out := &bytes.Buffer{}
	scan := func(context.Context, firstuse.DoctorOptions) firstuse.DoctorReport {
		return firstuse.DoctorReport{OK: true, Checks: []firstuse.Check{{ID: "terminal.available.headless", Status: firstuse.Pass, Summary: "Headless execution is available."}}}
	}
	cmd := newRootCommandWithDependencies(service, out, &bytes.Buffer{}, os.Getwd, discovery.Discover, nil, scan)
	cmd.SetArgs([]string{"setup", "--dry-run", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report firstuse.SetupReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("setup JSON = %q: %v", out.String(), err)
	}
	if report.Capabilities == nil || len(report.Capabilities.Checks) != 1 || report.Capabilities.Checks[0].ID != "terminal.available.headless" {
		t.Fatalf("setup report = %#v", report)
	}
	if strings.Contains(out.String(), "Machine capabilities:") {
		t.Fatalf("JSON output contains human text: %q", out.String())
	}
}

func TestRootWithoutArgumentsGuidesUnconfiguredInstallationToSetup(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{defaultsReport: config.DefaultsReport{HomeDir: filepath.Join(t.TempDir(), "new-home")}}, out, &bytes.Buffer{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Welcome to PromptGrinder.", "has not been configured", "promptgrinder setup", "inspect local runtimes, terminals, Git, shell configuration"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestRootWithoutArgumentsShowsHelpAfterSetup(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{defaultsReport: config.DefaultsReport{UserConfigExists: true}}, out, &bytes.Buffer{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Usage:") || strings.Contains(out.String(), "has not been configured") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLIRunFolderPreflightFailurePrintsReason(t *testing.T) {
	service := &fakeService{runFolderErr: errTest("unsupported numbered prompt name: 50-add-tests.md")}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"--plain", "run-folder", "specs", "--detach=false"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected preflight failure")
	}
	if !strings.Contains(out.String(), "Error: unsupported numbered prompt name: 50-add-tests.md") || !strings.Contains(out.String(), "Result: failed") {
		t.Fatalf("run-folder output = %q", out.String())
	}
}

func TestNamedWorkerLaunchersRegisterRuntimeAdapters(t *testing.T) {
	launchers := namedWorkerLaunchers(pgruntime.Service{})
	for _, name := range []string{"codex", "antigravity"} {
		launcher, ok := launchers[name]
		if !ok {
			t.Fatalf("runtime %q is not registered", name)
		}
		provider, ok := launcher.(workerlaunch.CapabilityProvider)
		if !ok || !provider.Capabilities().Headless {
			t.Fatalf("runtime %q capabilities are unavailable", name)
		}
	}
}

func TestV1PublicRootCommandContract(t *testing.T) {
	cmd := NewRootCommand(&fakeService{}, &bytes.Buffer{}, &bytes.Buffer{})
	var got []string
	for _, child := range cmd.Commands() {
		if child.Hidden || child.Name() == "completion" || child.Name() == "help" {
			continue
		}
		got = append(got, child.Name())
	}
	sort.Strings(got)
	want := []string{
		"cancel", "complete", "defaults", "discover", "doctor", "engines", "events", "fail", "list",
		"logs", "prune", "reconcile", "review", "roles", "run", "run-folder", "scheduler", "sequence",
		"sequences", "setup", "status", "task", "terminals", "validate", "validate-folder", "worker", "workers",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public root commands changed\n got: %q\nwant: %q", got, want)
	}
}

func TestDiscoverCommandPrintsStableSectionsAndUsesInjectedDiscovery(t *testing.T) {
	out := &bytes.Buffer{}
	calledWith := ""
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand(&fakeService{}, out, &bytes.Buffer{}, func() (string, error) { return repo, nil }, func(root string) (discovery.Result, error) {
		calledWith = root
		return discovery.Result{
			Roles: []discovery.Role{{ID: "backend-feature", Name: "Backend Feature"}, {ID: "backend-test", Name: "Backend Test"}},
			Files: []string{".promptgrinder/project.yaml", ".promptgrinder/roles/backend-feature.yaml", ".promptgrinder/roles/backend-test.yaml"},
		}, nil
	})
	cmd.SetArgs([]string{"discover"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calledWith != repo {
		t.Fatalf("discover root = %q, want %q", calledWith, repo)
	}
	want := "Discovered:\n  Backend Feature\n  Backend Test\nGenerated:\n  .promptgrinder/project.yaml\n  .promptgrinder/roles/backend-feature.yaml\n  .promptgrinder/roles/backend-test.yaml\n  .promptgrinder/context/\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestDiscoverCommandOutsideRepositoryIsInvalidInput(t *testing.T) {
	called := false
	errOut := &bytes.Buffer{}
	cmd := newRootCommand(&fakeService{}, &bytes.Buffer{}, errOut, func() (string, error) {
		return t.TempDir(), nil
	}, func(string) (discovery.Result, error) {
		called = true
		return discovery.Result{}, nil
	})
	cmd.SetArgs([]string{"discover"})
	err := cmd.Execute()
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d (error %v)", code, ok, ExitInvalidInput, err)
	}
	if called {
		t.Fatal("discovery was called outside a repository")
	}
	if !strings.Contains(errOut.String(), "Error: current directory is not inside a repository:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestDiscoverCommandReportsGenerationConflict(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	errOut := &bytes.Buffer{}
	cmd := newRootCommand(&fakeService{}, &bytes.Buffer{}, errOut, func() (string, error) {
		return repo, nil
	}, func(string) (discovery.Result, error) {
		return discovery.Result{}, errors.New(`existing discovery target ".promptgrinder/project.yaml" differs from the current repository analysis; no files were changed. Reconcile the file manually, or move the existing .promptgrinder directory aside before running promptgrinder discover again`)
	})
	cmd.SetArgs([]string{"discover"})
	err := cmd.Execute()
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d (error %v)", code, ok, ExitInvalidInput, err)
	}
	want := "Error: discover repository: existing discovery target \".promptgrinder/project.yaml\" differs from the current repository analysis; no files were changed. Reconcile the file manually, or move the existing .promptgrinder directory aside before running promptgrinder discover again\n"
	if errOut.String() != want {
		t.Fatalf("stderr = %q, want %q", errOut.String(), want)
	}
}

type fakeRoleAdvisor struct {
	plan  roleenhance.ReviewPlan
	calls int
}

func (f *fakeRoleAdvisor) Recommend(context.Context, roleenhance.CurrentState, roleenhance.Evidence) (roleenhance.ReviewPlan, error) {
	f.calls++
	return f.plan, nil
}

func roleEnhanceCLIRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".promptgrinder", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".promptgrinder", "project.yaml"), []byte("name: test\nroles: [backend]\ngenerated_by: promptgrinder discover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"), []byte("id: backend\nname: Backend\ndescription: old\ntechnology: [Go]\nallowed_paths: [internal]\nruntime: {preferred: local}\nquality_gates: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("The backend owns APIs.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRolesEnhanceReviewOnlyAndApplySelected(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	path := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	before, _ := os.ReadFile(path)
	advisor := &fakeRoleAdvisor{plan: roleenhance.ReviewPlan{Items: []roleenhance.ReviewItem{{Recommendation: roleenhance.Recommendation{ID: "desc", RoleID: "backend", Operation: roleenhance.OperationSet, Field: "description", Value: "Own APIs", Confidence: roleenhance.ConfidenceHigh, Explanation: "documented ownership", Evidence: []roleenhance.Citation{{Path: "README.md"}}}, OldValue: "old"}}}}
	out := &bytes.Buffer{}
	cmd := newRootCommandWithRoleAdvisor(&fakeService{}, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetArgs([]string{"roles", "enhance"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("default review wrote file")
	}
	for _, want := range []string{"[desc] backend", "Old: \"old\"", "Proposed: \"Own APIs\"", "Confidence: high", "Reason: documented ownership", "README.md", "no files written"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	cmd = newRootCommandWithRoleAdvisor(&fakeService{}, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetArgs([]string{"roles", "enhance", "--apply-selected", "desc"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if !strings.Contains(string(after), "description: Own APIs") {
		t.Fatalf("role=%s", after)
	}
}

func TestRolesEnhanceRejectsAmbiguousFlagsBeforeAdvisor(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	advisor := &fakeRoleAdvisor{}
	cmd := newRootCommandWithRoleAdvisor(&fakeService{}, &bytes.Buffer{}, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetArgs([]string{"roles", "enhance", "--apply-all", "--reject-all"})
	err := cmd.Execute()
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("err=%v code=%d", err, code)
	}
	if advisor.calls != 0 {
		t.Fatal("advisor called for invalid flags")
	}
}

func TestRolesEnhanceInteractiveMenuBranches(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       []string
		wantRole   string
		forbidRole string
	}{
		{name: "save", input: "s\n", want: []string{"Apply safe changes", "Review changes one by one", "Edit recommendations", "Reject all", "saved for later"}, wantRole: "description: old"},
		{name: "invalid retry and apply safe", input: "wat\na\n", want: []string{"Invalid choice", "Applied 0 safe change"}, wantRole: "description: old"},
		{name: "reject", input: "x\ny\n", want: []string{"Review rejected", "No role files were written"}, wantRole: "description: old"},
		{name: "review replacement confirmation", input: "r\na\nn\nq\n", want: []string{"Explicitly approve this replacement", "saved for later"}, wantRole: "description: old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := roleEnhanceCLIRepo(t)
			advisor := interactiveRoleAdvisor()
			out := &ttyBuffer{}
			service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: filepath.Join(t.TempDir(), "home")}}}
			cmd := newRootCommandWithRoleAdvisor(service, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
			cmd.SetIn(ttyInput{strings.NewReader(tt.input)})
			cmd.SetArgs([]string{"--plain", "roles", "enhance"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q:\n%s", want, out.String())
				}
			}
			role, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
			if !strings.Contains(string(role), tt.wantRole) {
				t.Fatalf("role = %s", role)
			}
			if strings.Contains(out.String(), "\x1b[") {
				t.Fatalf("plain output has controls: %q", out.String())
			}
			if advisor.calls != 1 {
				t.Fatalf("advisor calls = %d", advisor.calls)
			}
		})
	}
}

func TestRolesEnhanceInteractiveEditAndEOFPersistWithoutYAML(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	advisor := interactiveRoleAdvisor()
	out := &ttyBuffer{}
	home := filepath.Join(t.TempDir(), "home")
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: home}}}
	cmd := newRootCommandWithRoleAdvisor(service, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetIn(ttyInput{strings.NewReader("e\n1\n[\"invalid\",\"scalar\"]\ne\n1\n\"Own stable APIs\"\n")})
	cmd.SetArgs([]string{"--plain", "roles", "enhance"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Edit rejected", "Edit saved", `Proposed: "Own stable APIs"`, "saved for later"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	role, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
	if !strings.Contains(string(role), "description: old") {
		t.Fatalf("EOF edit wrote YAML: %s", role)
	}
	if advisor.calls != 1 {
		t.Fatalf("advisor calls = %d", advisor.calls)
	}
}

func TestRolesEnhancePipedInputNeverPrompts(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	advisor := interactiveRoleAdvisor()
	out := &ttyBuffer{}
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: filepath.Join(t.TempDir(), "home")}}}
	cmd := newRootCommandWithRoleAdvisor(service, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetIn(strings.NewReader("a\n"))
	cmd.SetArgs([]string{"roles", "enhance"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Choose an action") || !strings.Contains(out.String(), "promptgrinder roles apply") {
		t.Fatalf("piped output = %s", out.String())
	}
	role, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
	if !strings.Contains(string(role), "description: old") {
		t.Fatalf("piped execution wrote YAML: %s", role)
	}
}

func TestRolesEnhanceJSONTTYNeverPrompts(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	advisor := interactiveRoleAdvisor()
	out := &ttyBuffer{}
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: filepath.Join(t.TempDir(), "home")}}}
	cmd := newRootCommandWithRoleAdvisor(service, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetIn(ttyInput{strings.NewReader("a\n")})
	cmd.SetArgs([]string{"roles", "enhance", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || strings.Contains(out.String(), "Choose an action") {
		t.Fatalf("JSON TTY output = %s", out.String())
	}
	role, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
	if !strings.Contains(string(role), "description: old") {
		t.Fatalf("JSON execution wrote YAML: %s", role)
	}
}

func TestRolesEnhanceCanceledContextSavesWithoutYAML(t *testing.T) {
	root := roleEnhanceCLIRepo(t)
	advisor := interactiveRoleAdvisor()
	out := &ttyBuffer{}
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: filepath.Join(t.TempDir(), "home")}}}
	cmd := newRootCommandWithRoleAdvisor(service, out, &bytes.Buffer{}, func() (string, error) { return root, nil }, discovery.Discover, advisor)
	cmd.SetIn(ttyInput{strings.NewReader("a\n")})
	cmd.SetArgs([]string{"roles", "enhance"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "saved for later") {
		t.Fatalf("cancel output = %s", out.String())
	}
	role, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
	if !strings.Contains(string(role), "description: old") {
		t.Fatalf("canceled execution wrote YAML: %s", role)
	}
}

func interactiveRoleAdvisor() *fakeRoleAdvisor {
	return &fakeRoleAdvisor{plan: roleenhance.ReviewPlan{Items: []roleenhance.ReviewItem{
		{Recommendation: roleenhance.Recommendation{ID: "desc", RoleID: "backend", Operation: roleenhance.OperationSet, Field: "description", Value: "Own APIs", Confidence: roleenhance.ConfidenceHigh, Explanation: "documented ownership", Evidence: []roleenhance.Citation{{Path: "README.md"}}}},
	}}}
}

func TestCLIWorkerListAndShowTextAndJSON(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `version: 1
project: {id: example, name: Example}
workers:
  backend:
    display_name: Backend Engineer
    role: Build APIs.
    runtime: codex
    branch: {prefix: worker/backend}
    worktree: {default: .}
    paths:
      allowed: [backend/**]
      forbidden: [backend/secrets/**]
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "workers.yaml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	tests := []struct {
		name string
		args []string
		want []string
		json bool
	}{
		{name: "list text", args: []string{"worker", "list"}, want: []string{"PROJECT\texample\tExample", "backend\tBackend Engineer\tcodex\t."}},
		{name: "show text", args: []string{"worker", "show", "backend"}, want: []string{"Worker: backend", "Allowed paths: backend/**"}},
		{name: "list JSON", args: []string{"worker", "list", "--json"}, want: []string{`"project"`, `"workers"`}, json: true},
		{name: "show JSON", args: []string{"worker", "show", "backend", "--json"}, want: []string{`"project"`, `"worker"`, `"backend"`}, json: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
			cmd.SetArgs(test.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if test.json && !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON: %s", out.String())
			}
			for _, want := range test.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output %q does not contain %q", out.String(), want)
				}
			}
		})
	}
}

func TestCLITaskAssignListShowAndErrors(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `version: 1
project: {id: example, name: Example}
workers:
  backend:
    display_name: Backend Engineer
    role: Build APIs.
    runtime: codex
    branch: {prefix: worker/backend}
    worktree: {default: .}
    paths: {allowed: ["**"], forbidden: [secrets/**]}
  frontend:
    display_name: Frontend Engineer
    role: Build UI.
    runtime: codex
    branch: {prefix: worker/frontend}
    worktree: {default: .}
    paths: {allowed: ["**"], forbidden: [secrets/**]}
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "workers.yaml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "work.md"), []byte("# Build it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: home}}}

	run := func(args ...string) (string, error) {
		out := &bytes.Buffer{}
		cmd := NewRootCommand(service, out, &bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("task", "assign", "backend", "work.md")
	if err != nil || !strings.Contains(out, "Assigned task work to worker backend") {
		t.Fatalf("assign output = %q, error %v", out, err)
	}
	out, err = run("task", "list", "--worker", "backend")
	if err != nil || !strings.Contains(out, "work\tbackend\tassigned\t0\twork.md") {
		t.Fatalf("list output = %q, error %v", out, err)
	}
	out, err = run("task", "show", "work")
	if err != nil || !strings.Contains(out, "Instructions:\n# Build it") {
		t.Fatalf("show output = %q, error %v", out, err)
	}
	out, err = run("task", "show", "work", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"content_snapshot"`) {
		t.Fatalf("JSON show output = %q, error %v", out, err)
	}
	if _, err = run("task", "assign", "backend", "work.md"); err == nil {
		t.Fatal("duplicate assignment succeeded")
	} else if code, ok := ExitCode(err); !ok || code != ExitInvalidTransition {
		t.Fatalf("duplicate exit = %d, %v; error %v", code, ok, err)
	}
	if _, err = run("task", "list", "--worker", "missing"); err == nil {
		t.Fatal("unknown worker filter succeeded")
	} else if code, ok := ExitCode(err); !ok || code != ExitWorkerNotFound {
		t.Fatalf("unknown worker exit = %d, %v; error %v", code, ok, err)
	}
}

func TestCLIWorkerStartDryRunCreatesNoStateOrProcess(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "promptgrinder-home")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `version: 1
project: {id: example, name: Example}
workers:
  backend:
    display_name: Backend Engineer
    role: Build APIs.
    runtime: claude
    branch: {prefix: worker/backend}
    worktree: {default: .}
    paths:
      allowed: [backend/**]
      forbidden: [backend/secrets/**]
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "workers.yaml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{
		HomeDir: home,
		RuntimeOptions: map[string]map[string]any{
			"codex": {"api_key": "must-not-leak", "model": "test-model"},
		},
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "start", "backend", "--dry-run", "--runtime", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Launch plan (dry run)", "Runtime: codex", "api_key: \"[redacted]\"", "No process started. No mutable state created."} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "must-not-leak") {
		t.Fatalf("output leaked secret: %s", output)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("dry-run touched state home %s: %v", home, err)
	}
}

func TestCLIWorkerStatusAndResetTextAndJSON(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(t.TempDir(), "promptgrinder-home")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	registryFile := `version: 1
project: {id: example, name: Example}
workers:
  backend:
    display_name: Backend Engineer
    role: Build APIs.
    runtime: codex
    branch: {prefix: worker/backend}
    worktree: {default: .}
    paths:
      allowed: [backend/**]
      forbidden: [backend/secrets/**]
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "workers.yaml"), []byte(registryFile), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{HomeDir: home}}}

	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "status", "backend"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Project: Example (example)", "Worker: backend", "Lifecycle: idle", "Revision: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output %q missing %q", out.String(), want)
		}
	}
	out.Reset()
	cmd = NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "status", "backend", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var statusDecoded namedWorkerStateJSONOutput
	if err := json.Unmarshal(out.Bytes(), &statusDecoded); err != nil || statusDecoded.State.Lifecycle != workerdomain.LifecycleIdle {
		t.Fatalf("status JSON = %q, decoded %#v, error %v", out.String(), statusDecoded, err)
	}

	store := workerstate.New(home)
	stateValue, err := store.Load("example", "backend")
	if err != nil {
		t.Fatal(err)
	}
	stateValue.Lifecycle = workerdomain.LifecycleFailed
	stateValue.FailureReason = "runtime exited"
	if _, err := store.Save(stateValue, stateValue.Revision); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	cmd = NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "reset", "backend", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded namedWorkerStateJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid reset JSON %q: %v", out.String(), err)
	}
	if decoded.State.Lifecycle != workerdomain.LifecycleIdle || decoded.State.FailureReason != "" {
		t.Fatalf("reset JSON state = %#v", decoded.State)
	}
	revision := decoded.State.Revision

	out.Reset()
	cmd = NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "reset", "backend", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.State.Revision != revision {
		t.Fatalf("repeated reset changed revision: %s, %v", out.String(), err)
	}
	out.Reset()
	cmd = NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"worker", "reset", "backend"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Worker backend is idle (revision") {
		t.Fatalf("reset text output = %q", out.String())
	}
}

func TestV1InternalCommandsAreHiddenFromHelp(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"__run-folder-supervisor", "__worker-finish", "__worker-heartbeat",
		"__terminal-close", "__engine-codex",
	} {
		if strings.Contains(out.String(), name) {
			t.Fatalf("root help exposes internal command %q:\n%s", name, out.String())
		}
	}
}

func TestV1ExitCodeContract(t *testing.T) {
	got := []int{
		ExitSuccess, ExitGeneralError, ExitInvalidInput, ExitWorkerNotFound,
		ExitInvalidTransition, ExitExecutionFailed, ExitTimeout,
		ExitReadinessFailed, ExitSetupFailed,
	}
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exit code contract changed: got %v, want %v", got, want)
	}
	for _, code := range want[1:] {
		if gotCode, ok := ExitCode(StructuredError{Err: context.Canceled, Code: code}); !ok || gotCode != code {
			t.Fatalf("ExitCode(%d) = %d, %t", code, gotCode, ok)
		}
	}
}

func TestV1JSONEnvelopeContract(t *testing.T) {
	tests := []struct {
		name string
		args []string
		keys []string
	}{
		{name: "defaults", args: []string{"defaults", "--json"}, keys: []string{"defaults"}},
		{name: "engines", args: []string{"engines", "--json"}, keys: []string{"engines"}},
		{name: "list", args: []string{"list", "--json"}, keys: []string{"workers"}},
		{name: "sequences", args: []string{"sequences", "--json"}, keys: []string{"sequences"}},
		{name: "terminals", args: []string{"terminals", "--json"}, keys: []string{"terminals"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &object); err != nil {
				t.Fatalf("invalid JSON %q: %v", out.String(), err)
			}
			for _, key := range tt.keys {
				if _, ok := object[key]; !ok {
					t.Fatalf("%s missing stable key %q: %s", tt.name, key, out.String())
				}
			}
		})
	}
}

func TestCLIList(t *testing.T) {
	service := &fakeService{workers: []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("wrk_test")) {
		t.Fatalf("list output = %q", out.String())
	}
}

func TestCLIListJSON(t *testing.T) {
	service := &fakeService{workers: []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, errOut)
	cmd.SetArgs([]string{"list", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Workers []state.Worker `json:"workers"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Workers) != 1 || decoded.Workers[0].ID != "wrk_test" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestCLIListJSONCompact(t *testing.T) {
	service := &fakeService{workers: []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("\n  ")) {
		t.Fatalf("compact json contains indentation: %q", out.String())
	}
	var decoded listJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
}

func TestCLIListJSONDefaultPretty(t *testing.T) {
	service := &fakeService{workers: []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("\n  ")) {
		t.Fatalf("pretty json missing indentation: %q", out.String())
	}
}

func TestCLIEnginesJSON(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{
		Name:        "codex",
		Description: "Codex",
		Capabilities: engine.Capabilities{
			SupportsModel: true,
		},
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded enginesJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Engines) != 1 || !decoded.Engines[0].Capabilities.SupportsModel {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	service := &fakeService{defaultsReport: config.DefaultsReport{
		HomeDir:      home,
		TemplatePath: filepath.Join(home, "templates", "default.yaml"),
		Config: config.Config{
			HomeDir:                 home,
			Engine:                  "codex",
			TerminalAdapter:         "terminal",
			TerminalMode:            "normal",
			WorkerHeartbeatInterval: 30 * time.Second,
			CodexSandbox:            "workspace-write",
			CodexApproval:           "never",
			RunFolderTemplate:       "codex",
			RunFolderCheckpoint:     true,
			RunFolderCommitEach:     true,
		},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"defaults"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Default template", "run_folder.checkpoint: true", "Override order"} {
		if !strings.Contains(output, want) {
			t.Fatalf("defaults output missing %q: %q", want, output)
		}
	}
}

func TestCLIRunFolderUsesConfiguredDefaults(t *testing.T) {
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{
		RunFolderTemplate:         "codex",
		RunFolderRepo:             "/repo",
		RunFolderEngine:           "codex",
		RunFolderCheckpoint:       true,
		RunFolderCommitEach:       true,
		RunFolderRequireCleanGit:  true,
		RunFolderRecoveryAttempts: 2,
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "specs"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !service.runFolderOptions.Checkpoint || !service.runFolderOptions.CommitEach || !service.runFolderOptions.RequireCleanGit || service.runFolderOptions.RecoveryAttempts != 2 {
		t.Fatalf("run folder options = %#v", service.runFolderOptions)
	}
	if service.runFolderOptions.Template != "codex" || service.runFolderOptions.EngineOverride != "codex" || service.runFolderOptions.RepoPath != "/repo" {
		t.Fatalf("run folder options = %#v", service.runFolderOptions)
	}
	if service.runFolderOptions.ExecutionPolicy != pgruntime.RunFolderExecutionForeground {
		t.Fatalf("execution policy = %q", service.runFolderOptions.ExecutionPolicy)
	}
}

func TestCLIRunFolderPassesExplicitResumeSequence(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "specs", "--resume-sequence", "seq_named"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runFolderOptions.ResumeSequence != "seq_named" {
		t.Fatalf("run folder options = %#v", service.runFolderOptions)
	}
}

func TestCLIRunFolderResumeSequenceRejectsOtherResumeModes(t *testing.T) {
	for _, conflicting := range []string{"--resume", "--fresh", "--restart", "--no-resume"} {
		t.Run(conflicting, func(t *testing.T) {
			cmd := NewRootCommand(&fakeService{}, &bytes.Buffer{}, &bytes.Buffer{})
			cmd.SetArgs([]string{"run-folder", "specs", "--resume-sequence", "seq_named", conflicting})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "--resume-sequence is mutually exclusive") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCLIEnginesHuman(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{
		Name:        "codex",
		Description: "Codex CLI",
		Capabilities: engine.Capabilities{
			SupportsModel:    true,
			SupportsApproval: true,
		},
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"codex", "Codex CLI", "supports_model", "supports_approval"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("engines output missing %q: %q", want, output)
		}
	}
}

func TestCLIDescribeEngineJSON(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{
		Name:        "codex",
		Description: "Codex",
		Capabilities: engine.Capabilities{
			SupportsSandbox: true,
		},
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines", "describe", "codex", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded engineJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Engine.Name != "codex" || !decoded.Engine.Capabilities.SupportsSandbox {
		t.Fatalf("decoded = %#v", decoded)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &topLevel); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if _, ok := topLevel["capabilities"]; ok {
		t.Fatalf("describe json has unexpected top-level capabilities: %q", out.String())
	}
}

func TestCLIDescribeEngineHuman(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{
		Name:        "codex",
		Description: "Codex CLI",
		Capabilities: engine.Capabilities{
			SupportsApproval: true,
			SupportsSandbox:  true,
		},
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines", "describe", "codex"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Engine: codex", "Description: Codex CLI", "supports_approval", "supports_sandbox", "metadata"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("describe output missing %q: %q", want, output)
		}
	}
}

func TestCLIDescribeEngineUnknownReturnsInvalidInput(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{Name: "codex", Description: "Codex"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines", "describe", "missing"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestCLIDescribeEngineUnknownJSON(t *testing.T) {
	service := &fakeService{engines: []engine.Descriptor{{Name: "codex", Description: "Codex"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"engines", "describe", "missing", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "validation_error" || decoded.Error.Message == "" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIRunEngineOverride(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "task.md", "--engine", "codex"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runOptions.EngineOverride != "codex" {
		t.Fatalf("runOptions = %#v", service.runOptions)
	}
}

func TestCLIRunSandboxOverride(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "task.md", "--sandbox", "danger-full-access"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runOptions.SandboxOverride != "danger-full-access" {
		t.Fatalf("runOptions = %#v", service.runOptions)
	}
}

func TestCLIRunFolderSandboxOverride(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "tasks", "--sandbox", "danger-full-access", "--detach=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runFolderOptions.SandboxOverride != "danger-full-access" {
		t.Fatalf("runFolderOptions = %#v", service.runFolderOptions)
	}
}

func TestCLIRunAcceptsMultipleFilesWithSharedContext(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "02-a.md", "02-b.md", "--shared-context"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(service.runPaths, ",") != "02-a.md,02-b.md" || !service.runOptions.SharedContext {
		t.Fatalf("paths/options = %#v %#v", service.runPaths, service.runOptions)
	}
	if !service.runOptions.CommitEach || !service.runOptions.RequireCleanGit || !service.runOptions.RollbackOnFailure {
		t.Fatalf("shared-context safety defaults = %#v", service.runOptions)
	}
}

func TestCLIRunAllowsConcurrentWorktreeOverride(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "02-a.md", "--shared-context", "--allow-concurrent-worktree"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !service.runOptions.AllowConcurrentWorktree {
		t.Fatalf("runOptions = %#v", service.runOptions)
	}
}

func TestCLIRunSharedContextAllowsCommitOptOut(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "02-a.md", "--shared-context", "--commit-each=false", "--require-clean-git=false", "--rollback-on-failure=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runOptions.CommitEach || service.runOptions.RequireCleanGit || service.runOptions.RollbackOnFailure {
		t.Fatalf("runOptions = %#v", service.runOptions)
	}
}

func TestCLIRunSharedContextPrintsPromptProgress(t *testing.T) {
	service := &fakeService{runProgress: []pgruntime.SharedRunProgress{
		{Index: 1, Total: 2, TaskPath: "/prompts/02A-task.md", Status: pgruntime.SharedRunStarted},
		{Index: 1, Total: 2, TaskPath: "/prompts/02A-task.md", Status: pgruntime.SharedRunSucceeded, Scope: "slice-policy", Engine: "codex", Model: "gpt-5.6-sol"},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "02-a.md", "02-b.md", "--shared-context", "--plain"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[1/2] 02A-task.md - In progress") ||
		!strings.Contains(out.String(), "✓ [1/2] 02A-task.md|slice-policy|codex/gpt-5.6-sol|<1s") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLIRunDryRunPrintsValidatedPlan(t *testing.T) {
	service := &fakeService{runSummary: pgruntime.RunSummary{ValidatedPaths: []string{"/repo/02A-task.md", "/repo/02B-task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "02[AB]-*.md", "--shared-context", "--dry-run", "--plain"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !service.runOptions.DryRun || !service.runOptions.SharedContext {
		t.Fatalf("options = %#v", service.runOptions)
	}
	if !strings.Contains(out.String(), "Dry run valid: 2 prompt(s), one shared context. No workers created.") || !strings.Contains(out.String(), "/repo/02A-task.md") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLIRunUnknownEngineReturnsExitCode2(t *testing.T) {
	service := &fakeService{runErr: errTest(`invalid engine "missing": unknown engine`)}
	stderr := &bytes.Buffer{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, stderr)
	cmd.SetArgs([]string{"run", "task.md", "--engine", "missing"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
	if service.runOptions.EngineOverride != "missing" {
		t.Fatalf("runOptions = %#v", service.runOptions)
	}
	if !strings.Contains(stderr.String(), `Error: invalid engine "missing": unknown engine`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCLIValidateJSON(t *testing.T) {
	service := &fakeService{validatePlan: worker.ValidationPlan{
		Valid:    true,
		Engine:   "codex",
		Warnings: []string{"frontmatter is oversized"},
		ExecutionPlan: map[string]any{
			"engine": "codex",
		},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "task.md", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded worker.ValidationPlan
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if !decoded.Valid || decoded.Engine != "codex" || len(decoded.Warnings) != 1 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIValidateRenderPrintsExactPromptBytes(t *testing.T) {
	want := "# Task Semantics (v3)\n\n## Validation\n\n- printf '%s\\n' '$HOME; *'\n\nBody  with spaces\n"
	service := &fakeService{validatePlan: worker.ValidationPlan{Valid: true, Engine: "codex", RenderedPrompt: want, ExecutionPlan: map[string]any{}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "--render", "task with spaces.md"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Fatalf("rendered bytes = %q, want %q", out.String(), want)
	}
}

func TestCLIValidateRenderRejectsJSON(t *testing.T) {
	cmd := NewRootCommand(&fakeService{}, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "task.md", "--render", "--json"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v", err)
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d, %t", code, ok)
	}
}

func TestCLIValidateInvalidReturnsExitCode2(t *testing.T) {
	service := &fakeService{
		validatePlan: worker.ValidationPlan{
			Valid:  false,
			Engine: "codex",
			Errors: []string{"unsupported sandbox value"},
		},
		validateErr: errTest("unsupported sandbox value"),
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "task.md"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
	if !bytes.Contains(out.Bytes(), []byte("Valid: false")) || !bytes.Contains(out.Bytes(), []byte("Error: unsupported sandbox value")) {
		t.Fatalf("validate output = %q", out.String())
	}
}

func TestCLIValidateRedactedHumanOutput(t *testing.T) {
	service := &fakeService{validatePlan: worker.ValidationPlan{
		Valid:    true,
		Engine:   "codex",
		Warnings: []string{"frontmatter is oversized"},
		ExecutionPlan: map[string]any{
			"working_directory":  "/repo",
			"command_preview":    "codex exec --cd /repo",
			"worker_would_start": false,
			"metadata": map[string]any{
				"env": map[string]any{"API_KEY": "[redacted]"},
			},
		},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "task.md"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Valid: true", "Engine: codex", "Warning: frontmatter is oversized", "Command: codex exec --cd /repo", "Worker launch: false"} {
		if !strings.Contains(output, want) {
			t.Fatalf("validate output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, "sk-") {
		t.Fatalf("validate output leaked secret: %q", output)
	}
}

func TestCLIValidateExternalTaskFailureShowsStructuredRepositoryHint(t *testing.T) {
	service := &fakeService{
		validatePlan: worker.ValidationPlan{
			Valid:    false,
			Engine:   "codex",
			Warnings: []string{"task is outside a Git repository"},
			Errors:   []string{"validate role and path policy: role file is missing"},
			ExecutionPlan: map[string]any{
				"validation_mode":    "standalone task",
				"repository_path":    "/tmp/tasks",
				"repository_source":  "inferred",
				"task_path":          "/tmp/tasks/25-api.md",
				"working_directory":  "/tmp/tasks",
				"metadata":           map[string]any{"role": "backend-feature"},
				"worker_would_start": false,
			},
		},
		validateErr: errTest("role file is missing"),
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "/tmp/tasks/25-api.md"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"Validation\n", "Target\n", "Policy\n", "Runtime\n", "Result\n", "Next: validate the external task against its target repository", "promptgrinder validate --repo <repository> /tmp/tasks/25-api.md", "promptgrinder validate-folder /tmp/tasks --repo <repository>"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validate output missing %q: %q", want, out.String())
		}
	}
}

func TestCLIValidateUsesExplicitRepositoryAndPrintsPolicy(t *testing.T) {
	service := &fakeService{validatePlan: worker.ValidationPlan{
		Valid:  true,
		Engine: "codex",
		ExecutionPlan: map[string]any{
			"validation_mode":   "standalone task; run-folder ordering, dependency, and completion checks are not evaluated",
			"repository_path":   "/repo",
			"repository_source": "explicit",
			"working_directory": "/repo",
			"task_policy": runfolder.TaskPolicyPreview{
				RoleID:            "backend-feature",
				RolePath:          "/repo/.promptgrinder/roles/backend-feature.yaml",
				RoleAllowedPaths:  []string{"backend/**"},
				SliceAllowedPaths: []string{"backend/src/main/**", "backend/src/test/**"},
				EffectiveRule:     "all declared role and slice path rules apply; the slice cannot widen the role boundary",
			},
			"metadata": map[string]any{
				"model_selection": map[string]any{"model": "gpt-5.6-sol", "cost": "high", "capabilities": []any{"text", "code"}},
				"engine":          map[string]any{"sandbox": "danger-full-access"},
			},
		},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "task.md", "--repo", "/repo"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.validateRepo != "/repo" {
		t.Fatalf("validate repo = %q", service.validateRepo)
	}
	for _, want := range []string{"Repository: /repo (explicit)", "Role: backend-feature", "Role boundary: backend/**", "Slice allowed paths: backend/src/main/**, backend/src/test/**", "Model selection: gpt-5.6-sol (cost: high); capabilities: text, code", "Sandbox: danger-full-access — high-risk local execution requested"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validate output missing %q: %q", want, out.String())
		}
	}
}

func TestCLIValidateFolderRunsPreflightWithoutLaunchingWorkers(t *testing.T) {
	service := &fakeService{sequence: pgruntime.SequenceState{SequenceID: "seq_preflight"}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate-folder", "specs", "--repo", "/repo", "--commit-each", "--recovery-attempts", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runFolderOptions.RepoPath != "/repo" || !service.runFolderOptions.CommitEach || service.runFolderOptions.RecoveryAttempts != 1 {
		t.Fatalf("preflight options = %#v", service.runFolderOptions)
	}
	for _, want := range []string{"Preflight: PASSED", "Valid: true", "Validation scope: full run-folder preflight; no workers will launch", "Sequence ID: seq_preflight", "Worker launch: false", "Result: PASSED — no workers launched."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validate-folder output missing %q: %q", want, out.String())
		}
	}
}

func TestCLIValidateDirectoryUsesCompletePreflight(t *testing.T) {
	folder := t.TempDir()
	service := &fakeService{
		sequence:       pgruntime.SequenceState{SequenceID: "seq_validate"},
		defaultsReport: config.DefaultsReport{Config: config.Config{RunFolderCommitEach: true, RunFolderRequireCleanGit: true}},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", folder, "--repo", "/repo", "--commit-each=false", "--require-clean-git=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.runFolderOptions.RepoPath != "/repo" || service.runFolderOptions.CommitEach || service.runFolderOptions.RequireCleanGit {
		t.Fatalf("preflight repo = %q", service.runFolderOptions.RepoPath)
	}
	for _, want := range []string{"Preflight: PASSED", "Validation scope: complete ordered prompt-folder preflight; no workers will launch", "Sequence ID: seq_validate", "Result: PASSED — no workers launched."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validate folder output missing %q: %q", want, out.String())
		}
	}
}

func TestCLIValidateDirectoryReturnsFailureWhenPromptValidationFails(t *testing.T) {
	folder := t.TempDir()
	service := &fakeService{
		folderPreflight: pgruntime.RunFolderPreflight{
			SequenceID: "seq_invalid",
			Inspection: runfolder.FolderInspection{Prompts: []runfolder.Prompt{{
				Name: "10-implement-api.pg",
				Path: filepath.Join(folder, "10-implement-api.pg"),
				Type: runfolder.TypeImplement,
			}}},
		},
		validatePlan: worker.ValidationPlan{Valid: false, Engine: "codex", ExecutionPlan: map[string]any{}},
		validateErr:  errors.New("model is unavailable"),
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"validate", folder})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "work-order validation failed for: 10-implement-api.pg") {
		t.Fatalf("validate folder error = %v", err)
	}
	for _, want := range []string{"Preflight: FAILED", "[✗] 10-implement-api.pg", "model is unavailable", "Result: FAILED — no workers launched."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validate folder output missing %q: %q", want, out.String())
		}
	}
}

func TestPrintFolderValidationPlanLabelsUnscopedPrompts(t *testing.T) {
	var out bytes.Buffer
	printFolderValidationPlan(&out, folderValidationPlan{
		Valid:          true,
		ValidationMode: "full run-folder preflight; no workers will launch",
		Preflight: pgruntime.RunFolderPreflight{SequenceID: "seq_checklist", Inspection: runfolder.FolderInspection{
			Prompts: []runfolder.Prompt{{Name: "99-final-verify.md", Type: runfolder.TypeVerify}},
		}},
	}, false)
	if got := out.String(); !strings.Contains(got, "[✓] 99-final-verify.md") || !strings.Contains(got, "[✓] Ordered sequence: seq_checklist") || !strings.Contains(got, "Role: unscoped (99-final-verify.md) — no role boundary or role model policy applies") {
		t.Fatalf("unscoped role label missing: %q", got)
	}
}

func TestCLIPrune(t *testing.T) {
	service := &fakeService{pruneCount: 2}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"prune"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Pruned 2 workers.")) {
		t.Fatalf("prune output = %q", out.String())
	}
}

func TestCLIPruneJSON(t *testing.T) {
	service := &fakeService{
		workers:    []state.Worker{{ID: "wrk_done", Status: state.StatusSucceeded, TaskPath: "task.md"}},
		pruneCount: 1,
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"prune", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded pruneJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Count != 1 || len(decoded.PrunedWorkers) != 1 || decoded.PrunedWorkers[0].WorkerID != "wrk_done" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIEventsFilters(t *testing.T) {
	service := &fakeService{eventResult: state.EventReadResult{Events: []state.Event{
		state.NewEvent("wrk_test", state.EventWorkerStarted, state.SeverityInfo, "Worker started", nil),
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "wrk_test", "--type", state.EventWorkerStarted, "--severity", state.SeverityInfo})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.eventFilter.Type != state.EventWorkerStarted || service.eventFilter.Severity != state.SeverityInfo {
		t.Fatalf("filter = %#v", service.eventFilter)
	}
	if !bytes.Contains(out.Bytes(), []byte("worker.started")) {
		t.Fatalf("events output = %q", out.String())
	}
}

func TestCLIEventsJSON(t *testing.T) {
	service := &fakeService{eventResult: state.EventReadResult{Events: []state.Event{
		state.NewEvent("wrk_test", state.EventWorkerStarted, state.SeverityInfo, "Worker started", nil),
	}}}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, errOut)
	cmd.SetArgs([]string{"events", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Events []state.Event `json:"events"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Type != state.EventWorkerStarted {
		t.Fatalf("decoded = %#v", decoded)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestCLIEventsUnknownWorker(t *testing.T) {
	service := &fakeService{eventErr: errTest("worker not found: missing")}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "missing"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLIEventsUnknownWorkerJSON(t *testing.T) {
	service := &fakeService{eventErr: errTest("worker not found: missing")}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "missing", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "worker_not_found" {
		t.Fatalf("error = %#v", decoded.Error)
	}
}

func TestCLIEventsInvalidFilterJSON(t *testing.T) {
	service := &fakeService{}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "wrk_test", "--tail", "-1", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "invalid_filter" {
		t.Fatalf("error = %#v", decoded.Error)
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
}

func TestCLIListWithEvents(t *testing.T) {
	service := &fakeService{
		workers:     []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}},
		latestEvent: pgruntime.EventSummary{Found: true, Event: state.NewEvent("wrk_test", state.EventWorkerHeartbeat, state.SeverityDebug, "Worker heartbeat", nil)},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--events"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("worker.heartbeat")) {
		t.Fatalf("list output = %q", out.String())
	}
}

func TestCLIStatusKnownWorker(t *testing.T) {
	now := time.Date(2026, 6, 29, 18, 0, 0, 0, time.UTC)
	exitCode := 7
	service := &fakeService{
		statusSummary: pgruntime.StatusSummary{
			Worker: state.Worker{
				ID:              "wrk_test",
				Status:          state.StatusFailed,
				TaskPath:        "/repo/task.md",
				RepositoryPath:  "/repo",
				Engine:          "codex",
				TerminalAdapter: "headless",
				CreatedAt:       &now,
				StartTime:       &now,
				FinishTime:      ptrTime(now.Add(time.Minute)),
				LastSeenAt:      ptrTime(now.Add(time.Minute)),
				LogPath:         "/state/workers/wrk_test/worker.log",
				ExitCode:        &exitCode,
				Metadata:        map[string]any{"working_directory": "backend", "timeout": "30s", "labels": []any{"backend", "cleanup"}},
			},
			EventPath:   "/state/workers/wrk_test/events.jsonl",
			LatestEvent: pgruntime.EventSummary{Found: true, Event: state.NewEvent("wrk_test", state.EventWorkerFailed, state.SeverityError, "Worker failed", nil)},
		},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"status", "wrk_test"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Worker: wrk_test", "Status: FAILED", "Terminal Adapter: headless", "Working Directory: /repo/backend", "Latest Event:", "Exit Code: 7"} {
		if !bytes.Contains([]byte(output), []byte(want)) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestCLIStatusJSON(t *testing.T) {
	now := time.Date(2026, 6, 29, 18, 0, 0, 0, time.UTC)
	service := &fakeService{
		statusSummary: pgruntime.StatusSummary{
			Worker: state.Worker{
				ID:             "wrk_test",
				Status:         state.StatusRunning,
				TaskPath:       "/repo/task.md",
				RepositoryPath: "/repo",
				Engine:         "codex",
				CreatedAt:      &now,
				LastSeenAt:     &now,
				Metadata:       map[string]any{},
			},
			EventPath:   "/state/workers/wrk_test/events.jsonl",
			LatestEvent: pgruntime.EventSummary{Found: true, Event: state.NewEvent("wrk_test", state.EventWorkerStarted, state.SeverityInfo, "Worker started", nil)},
		},
	}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, errOut)
	cmd.SetArgs([]string{"status", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Worker      state.Worker `json:"worker"`
		EventPath   string       `json:"event_path"`
		LatestEvent *state.Event `json:"latest_event"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Worker.ID != "wrk_test" || decoded.LatestEvent == nil || decoded.LatestEvent.Type != state.EventWorkerStarted {
		t.Fatalf("decoded = %#v", decoded)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestCLIStatusUnknownWorker(t *testing.T) {
	service := &fakeService{statusErr: errTest("worker not found: missing")}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"status", "missing"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLIStatusUnknownWorkerJSON(t *testing.T) {
	service := &fakeService{statusErr: errTest("worker not found: missing")}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"status", "missing", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "worker_not_found" {
		t.Fatalf("error = %#v", decoded.Error)
	}
}

func TestCLIStatusJSONMissingArgument(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"status", "--json", "--compact"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if decodeErr := json.Unmarshal(out.Bytes(), &decoded); decodeErr != nil {
		t.Fatalf("invalid json %q: %v", out.String(), decodeErr)
	}
	if decoded.Error.Code != "validation_error" {
		t.Fatalf("error = %#v", decoded.Error)
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
}

func TestClassifyInvalidTimeoutAsValidationError(t *testing.T) {
	err := errTest(`invalid timeout "soon": use values like 30s, 10m, or 2h`)
	if got := classifyError(err); got != "validation_error" {
		t.Fatalf("classifyError = %q, want validation_error", got)
	}
	if got := errorExitCode(classifyError(err)); got != ExitInvalidInput {
		t.Fatalf("exit code = %d, want %d", got, ExitInvalidInput)
	}
}

func TestCLILogsJSONExisting(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "worker.log")
	if err := os.WriteFile(logPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{statusSummary: pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", LogPath: logPath}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"logs", "wrk_test", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("\n  ")) {
		t.Fatalf("compact logs json contains indentation: %q", out.String())
	}
	var decoded logsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.WorkerID != "wrk_test" || !decoded.Exists || decoded.Content != "hello\n" || decoded.LogPath != logPath {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLILogsJSONMissing(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "missing.log")
	service := &fakeService{statusSummary: pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", LogPath: logPath}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"logs", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded logsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Exists || decoded.Content != "" || decoded.LogPath != logPath {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLILogsJSONUnknownWorker(t *testing.T) {
	service := &fakeService{statusErr: errTest("worker not found: missing")}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"logs", "missing", "--json", "--compact"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if decodeErr := json.Unmarshal(out.Bytes(), &decoded); decodeErr != nil {
		t.Fatalf("invalid json %q: %v", out.String(), decodeErr)
	}
	if decoded.Error.Code != "worker_not_found" {
		t.Fatalf("error = %#v", decoded.Error)
	}
	if code, ok := ExitCode(err); !ok || code != ExitWorkerNotFound {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitWorkerNotFound)
	}
}

func TestCLIEventsGlobal(t *testing.T) {
	service := &fakeService{globalEventResult: state.EventReadResult{Events: []state.Event{
		state.NewEvent("wrk_test", state.EventWorkerCreated, state.SeverityInfo, "Worker created", nil),
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "--global", "--type", state.EventWorkerCreated})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.globalEventFilter.Type != state.EventWorkerCreated {
		t.Fatalf("filter = %#v", service.globalEventFilter)
	}
	if !bytes.Contains(out.Bytes(), []byte("worker.created")) {
		t.Fatalf("events output = %q", out.String())
	}
}

func TestCLIEventsGlobalJSON(t *testing.T) {
	service := &fakeService{globalEventResult: state.EventReadResult{Events: []state.Event{
		state.NewEvent("wrk_test", state.EventWorkerCreated, state.SeverityInfo, "Worker created", nil),
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"events", "--global", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Events []state.Event `json:"events"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Type != state.EventWorkerCreated {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIEventsFollowStopsOnContext(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "events.jsonl")
	service := &fakeService{eventPath: eventPath}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"events", "wrk_test", "--follow"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestCLIEventsFollowJSONEmitsNDJSON(t *testing.T) {
	event := state.NewEvent("wrk_test", state.EventWorkerStarted, state.SeverityInfo, "Worker started", nil)
	eventPath := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventLine(t, eventPath, event)
	service := &fakeService{eventPath: eventPath}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"events", "wrk_test", "--follow", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("lines = %q", out.String())
	}
	var decoded state.Event
	if err := json.Unmarshal(lines[0], &decoded); err != nil {
		t.Fatalf("invalid ndjson line %q: %v", string(lines[0]), err)
	}
	if decoded.Type != state.EventWorkerStarted {
		t.Fatalf("event = %#v", decoded)
	}
	if bytes.Contains(out.Bytes(), []byte(`"events"`)) || bytes.Contains(out.Bytes(), []byte("TIME")) {
		t.Fatalf("unexpected wrapper/human text: %q", out.String())
	}
}

func TestCLIEventsGlobalFollowJSONEmitsNDJSON(t *testing.T) {
	event := state.NewEvent("wrk_test", state.EventWorkerCreated, state.SeverityInfo, "Worker created", nil)
	eventPath := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventLine(t, eventPath, event)
	service := &fakeService{globalEventPath: eventPath}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"events", "--global", "--follow", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded state.Event
	line := bytes.TrimSpace(out.Bytes())
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("invalid ndjson line %q: %v", string(line), err)
	}
	if decoded.Type != state.EventWorkerCreated {
		t.Fatalf("event = %#v", decoded)
	}
}

func TestPrintAppendedEventsAppliesFilters(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "events.jsonl")
	event := state.NewEvent("wrk_test", state.EventWorkerFailed, state.SeverityError, "Worker failed", nil)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}

	offset, err := printAppendedEvents(eventPath, 0, state.EventFilter{Severity: state.SeverityError}, out, &bytes.Buffer{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if offset == 0 {
		t.Fatal("offset was not advanced")
	}
	if !bytes.Contains(out.Bytes(), []byte("worker.failed")) {
		t.Fatalf("follow output = %q", out.String())
	}
}

func TestPrintAppendedEventsJSONEmitsNDJSON(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "events.jsonl")
	event := state.NewEvent("wrk_test", state.EventWorkerFailed, state.SeverityError, "Worker failed", nil)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}

	if _, err := printAppendedEvents(eventPath, 0, state.EventFilter{Severity: state.SeverityError}, out, &bytes.Buffer{}, true); err != nil {
		t.Fatal(err)
	}
	var decoded state.Event
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &decoded); err != nil {
		t.Fatalf("invalid ndjson line %q: %v", out.String(), err)
	}
	if decoded.Type != state.EventWorkerFailed {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestReadEventsForFollowReturnsScannedOffset(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "events.jsonl")
	first := state.NewEvent("wrk_test", state.EventWorkerStarted, state.SeverityInfo, "Worker started", nil)
	writeEventLine(t, eventPath, first)

	result, offset, err := readEventsForFollow(eventPath, state.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || offset <= 0 {
		t.Fatalf("result=%#v offset=%d", result, offset)
	}
	second := state.NewEvent("wrk_test", state.EventWorkerSucceeded, state.SeverityInfo, "Worker succeeded", nil)
	writeEventLine(t, eventPath, second)
	out := &bytes.Buffer{}
	nextOffset, err := printAppendedEvents(eventPath, offset, state.EventFilter{}, out, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if nextOffset <= offset {
		t.Fatalf("nextOffset=%d offset=%d", nextOffset, offset)
	}
	if bytes.Contains(out.Bytes(), []byte(state.EventWorkerStarted)) {
		t.Fatalf("follow duplicated initial event: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(state.EventWorkerSucceeded)) {
		t.Fatalf("follow missed appended event: %q", out.String())
	}
}

func TestParseDuration(t *testing.T) {
	duration, err := ParseDuration("2h")
	if err != nil {
		t.Fatal(err)
	}
	if duration != 2*time.Hour {
		t.Fatalf("duration = %s", duration)
	}
	if _, err := ParseDuration("nope"); err == nil {
		t.Fatal("expected invalid duration error")
	}
	if _, err := ParseDuration("0s"); err == nil {
		t.Fatal("expected non-positive duration error")
	}
}

func TestCLIComplete(t *testing.T) {
	service := &fakeService{transitionWorker: state.Worker{ID: "wrk_test", Status: state.StatusSucceeded}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"complete", "wrk_test"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Complete wrk_test as succeeded.")) {
		t.Fatalf("complete output = %q", out.String())
	}
}

func TestCLICompleteJSON(t *testing.T) {
	now := time.Now().UTC()
	service := &fakeService{
		statusSummary:    pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", Status: state.StatusRunning}},
		transitionWorker: state.Worker{ID: "wrk_test", Status: state.StatusSucceeded, FinishTime: &now},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"complete", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded lifecycleJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.WorkerID != "wrk_test" || decoded.PreviousStatus != state.StatusRunning || decoded.Status != state.StatusSucceeded {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIFailJSON(t *testing.T) {
	now := time.Now().UTC()
	service := &fakeService{
		statusSummary:    pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", Status: state.StatusRunning}},
		transitionWorker: state.Worker{ID: "wrk_test", Status: state.StatusFailed, FinishTime: &now},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"fail", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded lifecycleJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Status != state.StatusFailed {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLICancelJSON(t *testing.T) {
	now := time.Now().UTC()
	service := &fakeService{
		statusSummary:    pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", Status: state.StatusRunning}},
		transitionWorker: state.Worker{ID: "wrk_test", Status: state.StatusCancelled, FinishTime: &now},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"cancel", "wrk_test", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded lifecycleJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Status != state.StatusCancelled {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLILifecycleUnknownWorkerJSON(t *testing.T) {
	service := &fakeService{statusErr: errTest("worker not found: missing")}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"complete", "missing", "--json"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "worker_not_found" {
		t.Fatalf("error = %#v", decoded.Error)
	}
}

func TestCLILifecycleInvalidTransitionJSON(t *testing.T) {
	service := &fakeService{
		statusSummary: pgruntime.StatusSummary{Worker: state.Worker{ID: "wrk_test", Status: state.StatusSucceeded}},
		transitionErr: errTest("invalid worker transition: succeeded -> failed for wrk_test"),
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"fail", "wrk_test", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "invalid_transition" {
		t.Fatalf("error = %#v", decoded.Error)
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidTransition {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidTransition)
	}
}

func TestCLIReconcile(t *testing.T) {
	service := &fakeService{reconcileWorkers: []state.Worker{{ID: "wrk_stale", Status: state.StatusRunning, TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"reconcile", "--older-than", "30m"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Reconcile: 1 worker(s) stale older than 30m0s.")) {
		t.Fatalf("reconcile output = %q", out.String())
	}
}

func TestCLIReconcileJSON(t *testing.T) {
	service := &fakeService{reconcileWorkers: []state.Worker{{ID: "wrk_stale", Status: state.StatusRunning, TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"reconcile", "--older-than", "30m", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded reconcileJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.OlderThan != "30m0s" || decoded.MarkFailed || len(decoded.StaleWorkers) != 1 || len(decoded.UpdatedWorkers) != 0 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIReconcileMarkFailedJSON(t *testing.T) {
	service := &fakeService{reconcileWorkers: []state.Worker{{ID: "wrk_stale", Status: state.StatusFailed, TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"reconcile", "--older-than", "30m", "--mark-failed", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded reconcileJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if !decoded.MarkFailed || len(decoded.UpdatedWorkers) != 1 || len(decoded.StaleWorkers) != 0 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIReconcileInvalidDurationJSON(t *testing.T) {
	service := &fakeService{}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"reconcile", "--older-than", "bad", "--json"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error")
	}
	var decoded errorJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if decoded.Error.Code != "invalid_duration" {
		t.Fatalf("error = %#v", decoded.Error)
	}
}

func TestCLIRunFileJSON(t *testing.T) {
	service := &fakeService{runSummary: pgruntime.RunSummary{Workers: []state.Worker{{ID: "wrk_test", Status: state.StatusRunning, TaskPath: "task.md", Engine: "codex", TerminalAdapter: "headless"}}}}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, errOut)
	cmd.SetArgs([]string{"run", "task.md", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded runJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Workers) != 1 || decoded.Workers[0].WorkerID != "wrk_test" || len(decoded.Failures) != 0 {
		t.Fatalf("decoded = %#v", decoded)
	}
	if errOut.Len() != 0 || bytes.Contains(out.Bytes(), []byte("Started worker")) {
		t.Fatalf("stdout/stderr discipline failed stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCLIRunInteractiveShowsBannerAndHeader(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("PROMPTGRINDER_PLAIN", "")
	started := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	service := &fakeService{runSummary: pgruntime.RunSummary{Workers: []state.Worker{{
		ID:              "wrk_test",
		Status:          state.StatusRunning,
		TaskPath:        "/repo/prompts/foo.md",
		RepositoryPath:  "/repo",
		Engine:          "codex",
		TerminalAdapter: "terminal",
		StartTime:       &started,
		LogPath:         "/tmp/worker.log",
	}}}}
	out := &ttyBuffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "prompts/foo.md", "--theme", "minimal"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !bytes.Contains(out.Bytes(), []byte("PromptGrinder")) || !bytes.Contains(out.Bytes(), []byte("┌")) {
		t.Fatalf("interactive output missing banner/header: %q", output)
	}
	if !bytes.Contains(out.Bytes(), []byte("Worker: wrk_test")) || !bytes.Contains(out.Bytes(), []byte("Prompt: foo.md")) {
		t.Fatalf("interactive header missing worker details: %q", output)
	}
}

func TestCLIRunNoColorDisablesANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	service := &fakeService{runSummary: pgruntime.RunSummary{Workers: []state.Worker{{
		ID:             "wrk_test",
		Status:         state.StatusRunning,
		TaskPath:       "/repo/prompts/foo.md",
		RepositoryPath: "/repo",
		LogPath:        "/tmp/worker.log",
	}}}}
	out := &ttyBuffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "prompts/foo.md"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("\x1b[")) {
		t.Fatalf("NO_COLOR output contains ANSI escapes: %q", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("┌")) {
		t.Fatalf("NO_COLOR output contains decorative frame: %q", out.String())
	}
}

func TestCLIRunNonTTYDoesNotEmitDecorativeUI(t *testing.T) {
	service := &fakeService{runSummary: pgruntime.RunSummary{Workers: []state.Worker{{
		ID:       "wrk_test",
		Status:   state.StatusRunning,
		TaskPath: "task.md",
		LogPath:  "/tmp/worker.log",
	}}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "task.md"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("┌")) || bytes.Contains(out.Bytes(), []byte("\x1b[")) {
		t.Fatalf("non-TTY output contains decorative UI: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("Started worker wrk_test")) {
		t.Fatalf("non-TTY output missing plain run output: %q", out.String())
	}
}

func TestCLIRunFolderPartialFailureJSON(t *testing.T) {
	service := &fakeService{
		runSummary: pgruntime.RunSummary{
			Workers: []state.Worker{{ID: "wrk_test", Status: state.StatusRunning, TaskPath: "task.md", Engine: "codex", TerminalAdapter: "dry-run"}},
			Failed:  []error{errTest("bad-task.md: launch failed")},
		},
		runErr: errTest("1 worker launch failed"),
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run", "tasks", "--json"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected partial failure error")
	}
	var decoded runJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Workers) != 1 || len(decoded.Failures) != 1 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIRunFolderPrintsAggregateSummary(t *testing.T) {
	service := &fakeService{runFolderSummary: pgruntime.RunFolderSummary{
		Run: runfolder.RunState{Status: "completed", Completed: []string{"10-implement-a.md"}},
		Sequence: &runfolder.SequenceState{
			SequenceID:       "seq_test",
			Status:           "completed",
			TokenUsage:       runfolder.TokenUsage{Available: true, Total: 1234},
			ExecutiveSummary: "- 10-implement-a.md succeeded.",
			Items:            []runfolder.SequenceItem{{PromptName: "10-implement-a.md", Status: "succeeded", TokenUsage: &runfolder.TokenUsage{Available: true, Input: 1000, CachedInput: 800, Output: 234, ReasoningOutput: 111, Total: 1234}}},
		},
		Prompts:  []runfolder.Prompt{{Name: "10-implement-a.md", Type: runfolder.TypeImplement}},
		Adoption: &runfolder.SequenceAdoption{SequenceID: "seq_test", Explicit: true, RetainedPrompts: []string{"00-spec.md"}, RestartAt: "10-implement-a.md", PolicyHashChanges: []runfolder.PolicyHashChange{{PromptName: "00-spec.md", Retained: true}}},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "specs"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Reported token usage: 1234")) || !bytes.Contains(out.Bytes(), []byte("- 10-implement-a.md: 1234 (input: 1000; cached input: 800; output: 234; reasoning output: 111)")) || !bytes.Contains(out.Bytes(), []byte("Executive summary:")) || !bytes.Contains(out.Bytes(), []byte("Adopted sequence: seq_test; retained: 1; restart: 10-implement-a.md")) || !bytes.Contains(out.Bytes(), []byte("Role-policy fingerprint changes recorded: 1")) {
		t.Fatalf("run-folder output = %q", out.String())
	}
}

func TestCLIRunFolderForegroundRendersLifecycleWithoutDuplicateInventory(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("PROMPTGRINDER_PLAIN", "")
	events := []pgruntime.RunFolderProgressEvent{
		{Type: "run.started", SequenceID: "seq_cli", Folder: "specs", Inventory: []runfolder.ProgressPrompt{
			{Name: "00-spec.md", Type: runfolder.TypeSpecification, Status: "pending"},
			{Name: "10-implement.md", Type: runfolder.TypeImplement, Status: "pending"},
		}},
		{Type: "prompt.started", PromptName: "00-spec.md", PromptType: runfolder.TypeSpecification, Status: "running"},
		{Type: "prompt.skipped", PromptName: "00-spec.md", PromptType: runfolder.TypeSpecification, Status: "skipped"},
		{Type: "prompt.started", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "running"},
		{Type: "prompt.succeeded", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "succeeded", WorkerID: "wrk_1", Scope: "slice-policy", Engine: "codex", Model: "gpt-5.6-sol", LogPath: "/tmp/worker.log", Duration: time.Second, Completed: 2, Total: 2},
		{Type: "run.completed", SequenceID: "seq_cli"},
	}
	service := &fakeService{runFolderProgress: events}

	for _, tc := range []struct {
		name string
		out  io.Writer
		args []string
		ansi bool
	}{
		{name: "tty", out: &ttyBuffer{}, args: []string{"run-folder", "specs"}, ansi: true},
		{name: "redirected", out: &bytes.Buffer{}, args: []string{"run-folder", "specs"}},
		{name: "plain tty", out: &ttyBuffer{}, args: []string{"--plain", "run-folder", "specs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand(service, tc.out, &bytes.Buffer{})
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			got := tc.out.(interface{ String() string }).String()
			for _, want := range []string{"Mode: foreground", "Sequence: seq_cli", "00-spec.md [specification] - skipped", "10-implement.md|slice-policy|codex/gpt-5.6-sol|1s", "Result: succeeded"} {
				if !strings.Contains(got, want) {
					t.Fatalf("output missing %q: %q", want, got)
				}
			}
			if !tc.ansi && strings.Count(got, "Prompts:\n") != 1 {
				t.Fatalf("prompt inventory count = %d: %q", strings.Count(got, "Prompts:\n"), got)
			}
			if strings.Contains(got, "\x1b") != tc.ansi {
				t.Fatalf("ANSI presence = %v, want %v: %q", strings.Contains(got, "\x1b"), tc.ansi, got)
			}
		})
	}
}

func TestCLIRunFolderForegroundResumeFailureQuotesFolder(t *testing.T) {
	folder := "tasks/odd path;$(touch nope)'s"
	service := &fakeService{
		runFolderErr: errTest("worker launch failed"),
		runFolderProgress: []pgruntime.RunFolderProgressEvent{
			{Type: "run.started", SequenceID: "seq_resume", Folder: folder, Inventory: []runfolder.ProgressPrompt{
				{Name: "10-done.md", Type: runfolder.TypeImplement, Status: "succeeded"},
				{Name: "20-next.md", Type: runfolder.TypeTest, Status: "pending"},
			}},
			{Type: "prompt.started", PromptName: "20-next.md", PromptType: runfolder.TypeTest, Status: "running"},
			{Type: "prompt.failed", PromptName: "20-next.md", PromptType: runfolder.TypeTest, Status: "failed", WorkerID: "wrk_bad", LogPath: "/tmp/bad.log"},
		},
	}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", folder, "--resume"})
	err := cmd.Execute()
	if code, ok := ExitCode(err); !ok || code != ExitExecutionFailed {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitExecutionFailed)
	}
	got := out.String()
	for _, want := range []string{"10-done.md [implement] - succeeded", "✗ [2/2] 20-next.md|unscoped|unknown-engine/default|<1s", "Result: failed", `Resume: promptgrinder run-folder 'tasks/odd path;$(touch nope)'"'"'s' --resume`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
	if strings.Count(got, "Prompts:\n") != 1 || strings.ContainsAny(got, "\r\x1b") {
		t.Fatalf("failure output is not deterministic: %q", got)
	}
}

func TestCLIRunFolderForegroundNoColorDisablesTTYControls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	out := &ttyBuffer{}
	service := &fakeService{runFolderProgress: []pgruntime.RunFolderProgressEvent{{Type: "run.started", Folder: "specs", Inventory: []runfolder.ProgressPrompt{{Name: "10-a.md", Type: runfolder.TypeImplement, Status: "pending"}}}, {Type: "run.completed"}}}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "specs"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatalf("NO_COLOR output contains controls: %q", out.String())
	}
}

func TestCLIRunFolderEngineOverrideAndUnknownExitCode(t *testing.T) {
	service := &fakeService{runFolderErr: errTest(`invalid engine "missing": unknown engine`)}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "specs", "--engine", "missing"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if code, ok := ExitCode(err); !ok || code != ExitInvalidInput {
		t.Fatalf("exit code = %d %v, want %d", code, ok, ExitInvalidInput)
	}
	if service.runFolderOptions.EngineOverride != "missing" {
		t.Fatalf("runFolderOptions = %#v", service.runFolderOptions)
	}
}

func TestCLIDetachedRunFolderPrintsSequenceInspectionCommand(t *testing.T) {
	home := t.TempDir()
	service := &fakeService{sequence: pgruntime.SequenceState{SequenceID: "seq_detached"}, defaultsReport: config.DefaultsReport{HomeDir: home}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "task folder", "--detach"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Sequence: seq_detached", "Status: promptgrinder sequence seq_detached", "Cancel: promptgrinder sequence cancel seq_detached"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, missing %q", out.String(), want)
		}
	}
}

func TestCLISequenceCancel(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := NewRootCommand(&fakeService{}, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"sequence", "cancel", "seq_abc123"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Sequence seq_abc123 cancelled") || !strings.Contains(out.String(), "checkpoints are preserved") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunFolderHelpShowsInteractiveDetachFormAndDefault(t *testing.T) {
	out := &bytes.Buffer{}
	service := &fakeService{defaultsReport: config.DefaultsReport{Config: config.Config{RunFolderDetach: true}}}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"run-folder", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--detach", "--detach=false", "default true", "--resume-sequence"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, out.String())
		}
	}
}

func TestCLISequences(t *testing.T) {
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	service := &fakeService{sequences: []pgruntime.SequenceProgress{{
		SequenceID:   "seq_abc123",
		Status:       "running",
		Total:        14,
		Succeeded:    10,
		Failed:       0,
		Pending:      4,
		Current:      "11-ranking-cleanup.md",
		LastWorkerID: "wrk_test",
		UpdatedAt:    &now,
	}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"sequences"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("seq_abc123")) || !bytes.Contains(out.Bytes(), []byte("10/14")) {
		t.Fatalf("sequences output = %q", out.String())
	}
}

func TestCLISequencesJSON(t *testing.T) {
	service := &fakeService{sequences: []pgruntime.SequenceProgress{{SequenceID: "seq_abc123", Status: "completed", Total: 1, Succeeded: 1}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"sequences", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded sequencesJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Sequences) != 1 || decoded.Sequences[0].SequenceID != "seq_abc123" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLISequencesFiltersNormalizedFolderWithSpaces(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "task folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	service := &fakeService{sequences: []pgruntime.SequenceProgress{
		{SequenceID: "seq_match", Folder: folder},
		{SequenceID: "seq_other", Folder: filepath.Join(root, "other")},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"sequences", "--folder", "task folder"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "seq_match") || strings.Contains(out.String(), "seq_other") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLISequenceCurrentShowsPromptRows(t *testing.T) {
	promptDir := t.TempDir()
	promptPath := filepath.Join(promptDir, "10-implement-a.md")
	if err := os.WriteFile(promptPath, []byte("# Implement A\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{sequence: pgruntime.SequenceState{
		SequenceID: "seq_current",
		Status:     "running",
		Folder:     promptDir,
		Items: []runfolder.SequenceItem{{
			PromptPath:  promptPath,
			PromptName:  "10-implement-a.md",
			Status:      "running",
			WorkerID:    "wrk_test",
			LogPath:     "/tmp/worker.log",
			ContentHash: "abc",
		}},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"sequence", "current"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.Contains(output, "seq_current") || !strings.Contains(output, "Implement A") || !strings.Contains(output, "wrk_test") {
		t.Fatalf("sequence output = %q", output)
	}
	if service.sequenceID != "current" {
		t.Fatalf("sequenceID = %q", service.sequenceID)
	}
}

func TestCLIWorkers(t *testing.T) {
	service := &fakeService{workers: []state.Worker{{ID: "wrk_test", Status: "running", Engine: "codex", TaskPath: "task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"workers"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrk_test") || !strings.Contains(out.String(), "task.md") {
		t.Fatalf("workers output = %q", out.String())
	}
}

func TestCLITerminalsList(t *testing.T) {
	service := &fakeService{terminals: []pgruntime.TerminalCandidate{{WorkerID: "wrk_test", Status: state.StatusSucceeded, TaskPath: "task.md", Title: "PromptGrinder: task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"terminals"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("wrk_test")) || !bytes.Contains(out.Bytes(), []byte("promptgrinder terminals kill <number>")) {
		t.Fatalf("terminals output = %q", out.String())
	}
}

func TestCLITerminalsKillNumberJSON(t *testing.T) {
	service := &fakeService{terminals: []pgruntime.TerminalCandidate{{WorkerID: "wrk_test", Status: state.StatusSucceeded, TaskPath: "task.md", Title: "PromptGrinder: task.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"terminals", "kill", "1", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded terminalsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Closed) != 1 || decoded.Closed[0].WorkerID != "wrk_test" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLITerminalsKillCommaSeparatedNumbersJSON(t *testing.T) {
	service := &fakeService{terminals: []pgruntime.TerminalCandidate{
		{WorkerID: "wrk_one", Status: state.StatusSucceeded, TaskPath: "one.md", Title: "PromptGrinder: one.md"},
		{WorkerID: "wrk_two", Status: state.StatusSucceeded, TaskPath: "two.md", Title: "PromptGrinder: two.md"},
		{WorkerID: "wrk_three", Status: state.StatusSucceeded, TaskPath: "three.md", Title: "PromptGrinder: three.md"},
	}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"terminals", "kill", "1,3", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded terminalsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Closed) != 2 || decoded.Closed[0].WorkerID != "wrk_one" || decoded.Closed[1].WorkerID != "wrk_three" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLITerminalsKillDuplicateNumbersDeduplicates(t *testing.T) {
	service := &fakeService{terminals: []pgruntime.TerminalCandidate{{WorkerID: "wrk_one", Status: state.StatusSucceeded, TaskPath: "one.md", Title: "PromptGrinder: one.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"terminals", "kill", "1,1", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded terminalsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Closed) != 1 || decoded.Closed[0].WorkerID != "wrk_one" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLITerminalsKillAllJSON(t *testing.T) {
	service := &fakeService{terminals: []pgruntime.TerminalCandidate{{WorkerID: "wrk_one", Status: state.StatusSucceeded, TaskPath: "one.md"}, {WorkerID: "wrk_two", Status: state.StatusSucceeded, TaskPath: "two.md"}}}
	out := &bytes.Buffer{}
	cmd := NewRootCommand(service, out, &bytes.Buffer{})
	cmd.SetArgs([]string{"terminals", "kill", "--all", "--json", "--compact"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded terminalsJSONOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid json %q: %v", out.String(), err)
	}
	if len(decoded.Closed) != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCLIHiddenHeartbeat(t *testing.T) {
	service := &fakeService{}
	cmd := NewRootCommand(service, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"__worker-heartbeat", "wrk_test"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.heartbeatID != "wrk_test" {
		t.Fatalf("heartbeatID = %q", service.heartbeatID)
	}
}

type fakeService struct {
	runSummary        pgruntime.RunSummary
	runErr            error
	runOptions        pgruntime.RunOptions
	runPaths          []string
	runFolderSummary  pgruntime.RunFolderSummary
	runFolderErr      error
	folderPreflight   pgruntime.RunFolderPreflight
	runFolderOptions  pgruntime.RunFolderOptions
	runFolderProgress []pgruntime.RunFolderProgressEvent
	defaultsReport    config.DefaultsReport
	engines           []engine.Descriptor
	validatePlan      worker.ValidationPlan
	validateErr       error
	validateRepo      string
	sequences         []pgruntime.SequenceProgress
	sequence          pgruntime.SequenceState
	sequenceID        string
	terminals         []pgruntime.TerminalCandidate
	closedTerminals   []pgruntime.TerminalCandidate
	workers           []state.Worker
	pruneCount        int
	transitionWorker  state.Worker
	transitionErr     error
	reconcileWorkers  []state.Worker
	heartbeatID       string
	statusSummary     pgruntime.StatusSummary
	statusErr         error
	latestEvent       pgruntime.EventSummary
	eventResult       state.EventReadResult
	globalEventResult state.EventReadResult
	eventFilter       state.EventFilter
	globalEventFilter state.EventFilter
	runProgress       []pgruntime.SharedRunProgress
	eventPath         string
	globalEventPath   string
	eventErr          error
}

func (f *fakeService) RunPath(path string) (pgruntime.RunSummary, error) {
	return f.runSummary, f.runErr
}

func (f *fakeService) RunPathWithOptions(path string, options pgruntime.RunOptions) (pgruntime.RunSummary, error) {
	f.runOptions = options
	return f.runSummary, f.runErr
}

func (f *fakeService) RunPathsWithOptions(paths []string, options pgruntime.RunOptions) (pgruntime.RunSummary, error) {
	f.runPaths = append([]string(nil), paths...)
	f.runOptions = options
	for _, progress := range f.runProgress {
		if options.OnProgress != nil {
			options.OnProgress(progress)
		}
	}
	return f.runSummary, f.runErr
}

func (f *fakeService) RunPromptFolder(path string, options pgruntime.RunFolderOptions) (pgruntime.RunFolderSummary, error) {
	f.runFolderOptions = options
	for _, progress := range f.runFolderProgress {
		if options.Progress != nil {
			options.Progress(progress)
		}
	}
	return f.runFolderSummary, f.runFolderErr
}

func (f *fakeService) PreflightRunFolder(path string, options pgruntime.RunFolderOptions) (pgruntime.RunFolderPreflight, error) {
	f.runFolderOptions = options
	if f.runFolderErr != nil {
		return pgruntime.RunFolderPreflight{}, f.runFolderErr
	}
	preflight := f.folderPreflight
	if preflight.Folder == "" {
		preflight.Folder = path
	}
	if preflight.SequenceID == "" {
		preflight.SequenceID = f.sequence.SequenceID
		if preflight.SequenceID == "" {
			preflight.SequenceID = "seq_test"
		}
	}
	return preflight, nil
}

func (f *fakeService) Engines() []engine.Descriptor {
	if len(f.engines) > 0 {
		return f.engines
	}
	return []engine.Descriptor{{Name: "codex", Description: "Codex"}}
}

func (f *fakeService) DescribeEngine(name string) (engine.Descriptor, error) {
	for _, item := range f.Engines() {
		if item.Name == name {
			return item, nil
		}
	}
	return engine.Descriptor{}, errTest("unknown engine: " + name)
}

func (f *fakeService) Defaults() config.DefaultsReport {
	if f.defaultsReport.Config.RunFolderTemplate == "" {
		f.defaultsReport.Config.RunFolderTemplate = "codex"
	}
	return f.defaultsReport
}

func (f *fakeService) Validate(path, engineOverride, repoPath string) (worker.ValidationPlan, error) {
	f.validateRepo = repoPath
	if f.validatePlan.Engine == "" {
		f.validatePlan = worker.ValidationPlan{Valid: true, Engine: "codex", ExecutionPlan: map[string]any{}}
	}
	if engineOverride != "" {
		f.validatePlan.Engine = engineOverride
	}
	return f.validatePlan, f.validateErr
}

func (f *fakeService) Sequences() ([]pgruntime.SequenceProgress, error) {
	return f.sequences, nil
}

func (f *fakeService) SequenceID(path string, options pgruntime.RunFolderOptions) (string, error) {
	if f.sequence.SequenceID != "" {
		return f.sequence.SequenceID, nil
	}
	return "seq_test", nil
}

func (f *fakeService) Sequence(sequenceID string) (pgruntime.SequenceState, error) {
	f.sequenceID = sequenceID
	if f.sequence.SequenceID == "" {
		return pgruntime.SequenceState{}, errTest("sequence not found")
	}
	return f.sequence, nil
}

func (f *fakeService) CancelSequence(sequenceID string) (pgruntime.SequenceState, error) {
	return pgruntime.SequenceState{SequenceID: sequenceID, Status: "cancelled"}, nil
}

func (f *fakeService) TerminalCandidates() ([]pgruntime.TerminalCandidate, error) {
	return f.terminals, nil
}

func (f *fakeService) CloseTerminals(workerIDs []string) ([]pgruntime.TerminalCandidate, error) {
	if len(f.closedTerminals) > 0 {
		return f.closedTerminals, nil
	}
	if len(workerIDs) > 0 {
		selected := map[string]bool{}
		for _, id := range workerIDs {
			selected[id] = true
		}
		out := []pgruntime.TerminalCandidate{}
		for _, candidate := range f.terminals {
			if selected[candidate.WorkerID] {
				out = append(out, candidate)
			}
		}
		return out, nil
	}
	return f.terminals, nil
}

func (f *fakeService) List() ([]state.Worker, error) {
	return f.workers, nil
}

func (f *fakeService) Status(id string) (pgruntime.StatusSummary, error) {
	return f.statusSummary, f.statusErr
}

func (f *fakeService) LatestEvent(id string) (pgruntime.EventSummary, error) {
	return f.latestEvent, nil
}

func (f *fakeService) Events(id string, filter state.EventFilter) (state.EventReadResult, error) {
	f.eventFilter = filter
	return f.eventResult, f.eventErr
}

func (f *fakeService) GlobalEvents(filter state.EventFilter) (state.EventReadResult, error) {
	f.globalEventFilter = filter
	return f.globalEventResult, nil
}

func (f *fakeService) EventPath(id string) (string, error) {
	if f.eventPath != "" {
		return f.eventPath, nil
	}
	return filepath.Join(os.TempDir(), "promptgrinder-test-events.jsonl"), nil
}

func (f *fakeService) GlobalEventPath() string {
	if f.globalEventPath != "" {
		return f.globalEventPath
	}
	return filepath.Join(os.TempDir(), "promptgrinder-test-global-events.jsonl")
}

func (f *fakeService) Logs(id string) (string, error) {
	return "logs", nil
}

func (f *fakeService) Prune() (int, error) {
	return f.pruneCount, nil
}

func (f *fakeService) Complete(id string) (state.Worker, error) {
	return f.transitionWorker, f.transitionErr
}

func (f *fakeService) Fail(id string) (state.Worker, error) {
	return f.transitionWorker, f.transitionErr
}

func (f *fakeService) Cancel(id string) (state.Worker, error) {
	return f.transitionWorker, f.transitionErr
}

func (f *fakeService) Reconcile(threshold time.Duration, markFailed bool) (pgruntime.ReconcileSummary, error) {
	return pgruntime.ReconcileSummary{Workers: f.reconcileWorkers, Threshold: threshold, MarkFailed: markFailed}, nil
}

func (f *fakeService) FinishWorker(recordPath string, exitCode int) error {
	return nil
}

func (f *fakeService) Heartbeat(id string) error {
	f.heartbeatID = id
	return nil
}

func (f *fakeService) RunCodexWorker(recordPath, command string) (int, error) {
	return 0, nil
}

type errTest string

func (e errTest) Error() string {
	return string(e)
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func writeEventLine(t *testing.T, path string, event state.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}
