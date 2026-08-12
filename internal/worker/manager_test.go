package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine/codex"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/state"
	"promptgrinder/internal/terminal"
	"promptgrinder/internal/testsupport"
)

func TestLaunchCreatesAndPersistsWorker(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nacceptance_criteria:\n  - exact output\nallowed_paths:\n  - internal/**\nforbidden_paths:\n  - secrets/**\nvalidation: go test ./...\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:       store,
		Engine:      codex.Engine{},
		EngineName:  "codex",
		Executable:  "promptgrinder",
		BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Worker.Status != "running" {
		t.Fatalf("status = %q, want running", result.Worker.Status)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata["acceptance_criteria"] == nil {
		t.Fatalf("metadata = %#v", loaded.Metadata)
	}
	for _, key := range []string{"acceptance_criteria", "allowed_paths", "forbidden_paths", "validation"} {
		if _, ok := loaded.ResolvedMetadata[key]; ok {
			t.Fatalf("semantic key %q became runtime metadata: %#v", key, loaded.ResolvedMetadata)
		}
	}
	if loaded.EventPath != store.EventPath(loaded.ID) {
		t.Fatalf("event_path = %q, want %q", loaded.EventPath, store.EventPath(loaded.ID))
	}
	if loaded.Capabilities == nil {
		t.Fatalf("capabilities were not persisted: %#v", loaded)
	}
	events, err := store.ReadEvents(loaded.ID, state.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, events.Events, state.EventWorkerCreated, state.EventEngineSelected, state.EventEngineValidated, state.EventEngineCommandBuilt, state.EventWorkerStarted)
	for _, event := range events.Events {
		if event.Engine != "codex" {
			t.Fatalf("event missing effective engine name: %#v", event)
		}
	}
	prompt, err := os.ReadFile(loaded.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "# Task Semantics (v2)\n\n## Acceptance Criteria\n\n- exact output\n\n## Allowed Paths\n\n- internal/**\n\n## Forbidden Paths\n\n- secrets/**\n\n## Validation\n\n- go test ./...\n\n# Task\n"
	if string(prompt) != wantPrompt {
		t.Fatalf("prompt = %q", string(prompt))
	}
}

func TestLaunchPersistsLaunchFailure(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:       store,
		Engine:      codex.Engine{},
		EngineName:  "codex",
		Executable:  "promptgrinder",
		BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: testExecutorFactory(store, terminal.StaticFailure{Err: errors.New("boom")}),
	}

	result := manager.Launch(task)
	if result.Err == nil {
		t.Fatal("expected launch error")
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "launch_failed" {
		t.Fatalf("status = %q, want launch_failed", loaded.Status)
	}
}

func TestLaunchEngineOverrideIsPersistedInResolvedMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: shell\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:          store,
		Engine:         codex.Engine{},
		EngineName:     "codex",
		EngineOverride: "codex",
		Executable:     "promptgrinder",
		BaseConfig:     config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor:    testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Engine != "codex" {
		t.Fatalf("engine = %q, want codex", loaded.Engine)
	}
	if loaded.RequestedEngine != "shell" || loaded.EngineOverride != "codex" || !loaded.EngineOverridden {
		t.Fatalf("override fields = requested:%q override:%q overridden:%t", loaded.RequestedEngine, loaded.EngineOverride, loaded.EngineOverridden)
	}
	originalEngine := loaded.Metadata["engine"].(map[string]any)
	if originalEngine["name"] != "shell" {
		t.Fatalf("metadata = %#v", loaded.Metadata)
	}
	resolvedEngine := loaded.ResolvedMetadata["engine"].(map[string]any)
	if resolvedEngine["name"] != "codex" {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
}

func TestLaunchSandboxOverrideWinsOverPromptMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: codex\n  sandbox: read-only\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:           store,
		Engine:          codex.Engine{},
		EngineName:      "codex",
		SandboxOverride: "danger-full-access",
		Executable:      "promptgrinder",
		BaseConfig:      config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor:     testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedEngine := loaded.ResolvedMetadata["engine"].(map[string]any)
	if resolvedEngine["sandbox"] != "danger-full-access" {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
	originalEngine := loaded.Metadata["engine"].(map[string]any)
	if originalEngine["sandbox"] != "read-only" {
		t.Fatalf("original metadata was mutated: %#v", loaded.Metadata)
	}
}

func TestValidateRejectsInvalidSandboxOverrideBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:           store,
		Engine:          codex.Engine{},
		EngineName:      "codex",
		SandboxOverride: "unrestricted-ish",
		Executable:      "promptgrinder",
		BaseConfig:      config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t)},
	}

	_, err := manager.Validate(task)
	if err == nil || !strings.Contains(err.Error(), "unsupported sandbox value") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(store.WorkersDir); !os.IsNotExist(statErr) {
		t.Fatalf("validation created worker state: %v", statErr)
	}
}

func TestLaunchPersistsResolvedV2CodexMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "tasks", "task.md")
	if err := os.MkdirAll(filepath.Dir(task), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: codex\n  model: gpt-5.5\n  profile: backend\n  sandbox: read-only\n  approval: on-request\n  web_search: true\n  images:\n    - screenshot.png\nworking_directory: backend\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:       store,
		Engine:      codex.Engine{},
		EngineName:  "codex",
		Executable:  "promptgrinder",
		BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedEngine := loaded.ResolvedMetadata["engine"].(map[string]any)
	if resolvedEngine["sandbox"] != "read-only" || resolvedEngine["approval"] != "on-request" || resolvedEngine["model"] != "gpt-5.5" {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
	if resolvedEngine["web_search"] != true {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
	images := resolvedEngine["images"].([]any)
	if len(images) != 1 || images[0] != filepath.Join(root, "tasks", "screenshot.png") {
		t.Fatalf("images = %#v", images)
	}
}

func TestLaunchPersistsLegacyCodexMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: codex\n  model: gpt-5.5\n  profile: backend\nsandbox: read-only\napproval: on-request\nweb_search: true\nimages:\n  - screenshot.png\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:       store,
		Engine:      codex.Engine{},
		EngineName:  "codex",
		Executable:  "promptgrinder",
		BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata["sandbox"] != "read-only" || loaded.Metadata["approval"] != "on-request" {
		t.Fatalf("metadata = %#v", loaded.Metadata)
	}
	resolvedEngine := loaded.ResolvedMetadata["engine"].(map[string]any)
	if resolvedEngine["sandbox"] != "read-only" || resolvedEngine["approval"] != "on-request" {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
}

func TestValidateStopsBeforeLaunchWhenCodexCannotSelectModel(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: codex\n  model: not-selectable\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{Store: store, Engine: codex.Engine{}, EngineName: "codex", BaseConfig: config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t)}}
	_, err := manager.Validate(task)
	if err == nil || !strings.Contains(err.Error(), `model "not-selectable" is not selectable`) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(store.WorkersDir); !os.IsNotExist(statErr) {
		t.Fatalf("model preflight created worker state: %v", statErr)
	}
}

func TestRoleModelDefaultsSelectLowestConfiguredCostThatHasCapabilities(t *testing.T) {
	root := t.TempDir()
	rolePath := filepath.Join(root, ".promptgrinder", "roles", "documentation.yaml")
	if err := os.MkdirAll(filepath.Dir(rolePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolePath, []byte("id: documentation\nruntime:\n  max_cost: high\n  capabilities: [code]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, ".promptgrinder", "models.yaml")
	if err := os.WriteFile(policyPath, []byte("version: 1\nmodels:\n  - id: gpt-5\n    cost: low\n    capabilities: [text]\n  - id: gpt-5.5\n    cost: medium\n    capabilities: [text, code]\n  - id: gpt-5.6-sol\n    cost: high\n    capabilities: [text, code]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nrole: documentation\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{Store: store, Engine: codex.Engine{}, EngineName: "codex", Executable: "promptgrinder", BaseConfig: config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"}, NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{})}
	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	resolved := result.Worker.ResolvedMetadata["engine"].(map[string]any)
	if resolved["model"] != "gpt-5.5" {
		t.Fatalf("resolved engine = %#v", resolved)
	}
	selection := result.Worker.ResolvedMetadata["model_selection"].(map[string]any)
	if selection["cost"] != "medium" {
		t.Fatalf("model selection = %#v", selection)
	}
}

func TestPromptModelOverridesRoleModelDefault(t *testing.T) {
	root := t.TempDir()
	rolePath := filepath.Join(root, ".promptgrinder", "roles", "documentation.yaml")
	if err := os.MkdirAll(filepath.Dir(rolePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolePath, []byte("id: documentation\nruntime:\n  model: gpt-5.6-sol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nrole: documentation\nengine:\n  name: codex\n  model: gpt-5\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{Store: store, Engine: codex.Engine{}, EngineName: "codex", Executable: "promptgrinder", BaseConfig: config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"}, NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{})}
	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if got := result.Worker.ResolvedMetadata["engine"].(map[string]any)["model"]; got != "gpt-5" {
		t.Fatalf("resolved model = %#v", got)
	}
}

func TestLaunchPrefersV2CodexMetadataOverLegacy(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: codex\n  sandbox: read-only\n  approval: on-request\n  web_search: false\n  images:\n    - engine.png\nsandbox: danger-full-access\napproval: never\nweb_search: true\nimages:\n  - legacy.png\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:       store,
		Engine:      codex.Engine{},
		EngineName:  "codex",
		Executable:  "promptgrinder",
		BaseConfig:  config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t), TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: testExecutorFactory(store, terminal.DryRunAdapter{}),
	}

	result := manager.Launch(task)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	loaded, err := store.Load(result.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedEngine := loaded.ResolvedMetadata["engine"].(map[string]any)
	if resolvedEngine["sandbox"] != "read-only" || resolvedEngine["approval"] != "on-request" || resolvedEngine["web_search"] != false {
		t.Fatalf("resolved_metadata = %#v", loaded.ResolvedMetadata)
	}
	images := resolvedEngine["images"].([]any)
	if len(images) != 1 || images[0] != filepath.Join(root, "engine.png") {
		t.Fatalf("images = %#v", images)
	}
	if loaded.Metadata["sandbox"] != "danger-full-access" {
		t.Fatalf("legacy metadata was not preserved: %#v", loaded.Metadata)
	}
}

func TestLaunchRejectsUnknownEngineOverrideBeforeCreatingWorker(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:          store,
		Engine:         codex.Engine{},
		EngineName:     "codex",
		EngineOverride: "missing",
		Executable:     "promptgrinder",
		BaseConfig:     config.Config{Engine: "codex", TerminalAdapter: "terminal", TerminalMode: "dry-run"},
		NewExecutor: func(config.Config) (execution.Executor, error) {
			t.Fatal("unknown override must fail before launch")
			return execution.Executor{}, nil
		},
	}

	result := manager.Launch(task)
	if result.Err == nil {
		t.Fatal("expected unknown engine error")
	}
	if !strings.Contains(result.Err.Error(), "unknown engine") {
		t.Fatalf("err = %v", result.Err)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("worker state was created at %s (err=%v)", store.WorkersDir, err)
	}
}

func TestValidateRejectsInvalidCodexMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nsandbox: bad\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex"},
	}

	plan, err := manager.Validate(task)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if plan.Valid || len(plan.Errors) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	// Validation fails before a durable worker identity exists, so emitting a
	// per-worker engine.validation_failed event would require fabricating a
	// worker record. Launch-time tests cover persisted validation success.
}

func TestValidateBuildsPlanWithoutCreatingWorker(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task, []byte("---\nworking_directory: backend\ntimeout: 10m\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(t.TempDir(), "home"))
	manager := Manager{
		Store:      store,
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t)},
		NewExecutor: func(config.Config) (execution.Executor, error) {
			t.Fatal("validate must not create an executor")
			return execution.Executor{}, nil
		},
	}

	plan, err := manager.Validate(task)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || plan.Engine != "codex" {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.ExecutionPlan["working_directory"] != filepath.Join(root, "backend") {
		t.Fatalf("execution_plan = %#v", plan.ExecutionPlan)
	}
	if plan.ExecutionPlan["timeout"] != "10m0s" {
		t.Fatalf("execution_plan = %#v", plan.ExecutionPlan)
	}
	if plan.ExecutionPlan["worker_would_start"] != false {
		t.Fatalf("execution_plan = %#v", plan.ExecutionPlan)
	}
	if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
		t.Fatalf("validate created worker state at %s (err=%v)", store.WorkersDir, err)
	}
}

func TestValidateRejectsCommonMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nenv:\n  API_TOKEN: 123\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex"},
	}

	plan, err := manager.Validate(task)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if plan.Valid || len(plan.Errors) != 1 || !strings.Contains(plan.Errors[0], "env.API_TOKEN must be a string") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestUnsupportedFrontmatterFailsBeforeWorkerCreation(t *testing.T) {
	for _, test := range []struct{ name, frontmatter, want string }{
		{"unknown", "mystery: true", `unknown top-level key "mystery"`},
		{"depends", "depends_on: previous-task", "depends_on must be a list"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			task := filepath.Join(root, "task.md")
			if err := os.WriteFile(task, []byte("---\n"+test.frontmatter+"\n---\n# Task\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			store := state.NewStore(filepath.Join(t.TempDir(), "home"))
			manager := Manager{Store: store, Engine: codex.Engine{}, EngineName: "codex", BaseConfig: config.Config{Engine: "codex"}}
			result := manager.Launch(task)
			if result.Err == nil || !strings.Contains(result.Err.Error(), test.want) || !strings.Contains(result.Err.Error(), task) {
				t.Fatalf("err = %v", result.Err)
			}
			if _, err := os.Stat(store.WorkersDir); !os.IsNotExist(err) {
				t.Fatalf("worker state created: %v", err)
			}
		})
	}
}

func TestValidateRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nenv:\n  API_KEY: sk-test\n  DESCRIPTION: this-is-a-long-but-public-value\nengine:\n  name: codex\n  model: gpt-5\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex", CodexExecutable: testsupport.FakeCodex(t)},
	}

	plan, err := manager.Validate(task)
	if err != nil {
		t.Fatal(err)
	}
	metadata := plan.ExecutionPlan["metadata"].(map[string]any)
	env := metadata["env"].(map[string]any)
	if env["API_KEY"] != "[redacted]" {
		t.Fatalf("env not redacted: %#v", env)
	}
	if env["DESCRIPTION"] != "this-is-a-long-but-public-value" {
		t.Fatalf("non-secret value redacted: %#v", env)
	}
}

func TestValidateRejectsUnknownEngine(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  name: missing\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex"},
	}

	plan, err := manager.Validate(task)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if plan.Valid || plan.Engine != "missing" || len(plan.Errors) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestValidateRejectsMalformedEngineMetadata(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "task.md")
	if err := os.WriteFile(task, []byte("---\nengine:\n  - codex\n---\n# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Engine:     codex.Engine{},
		EngineName: "codex",
		BaseConfig: config.Config{Engine: "codex"},
	}

	plan, err := manager.Validate(task)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if plan.Valid || len(plan.Errors) != 1 || !strings.Contains(plan.Errors[0], "engine metadata must be a string or map") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestResolveCodexExecutableFailuresAreDeterministic(t *testing.T) {
	t.Run("missing from PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := resolveExecutable("", "codex")
		if err == nil || !strings.Contains(err.Error(), "Codex executable was not found on PATH") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid configured path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-codex")
		_, err := resolveExecutable(missing, "codex")
		if err == nil || !strings.Contains(err.Error(), "configured engine.codex.executable") {
			t.Fatalf("err = %v", err)
		}
	})
}

func testExecutorFactory(store state.Store, adapter terminal.TerminalAdapter) ExecutorFactory {
	return func(cfg config.Config) (execution.Executor, error) {
		return execution.Executor{Store: store, Terminal: adapter}, nil
	}
}

func assertEventTypes(t *testing.T, events []state.Event, want ...string) {
	t.Helper()
	remaining := append([]string{}, want...)
	for _, event := range events {
		if len(remaining) == 0 {
			return
		}
		if event.Type == remaining[0] {
			remaining = remaining[1:]
		}
	}
	if len(remaining) > 0 {
		t.Fatalf("missing event types %v in %#v", remaining, events)
	}
}
