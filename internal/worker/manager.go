package worker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/markdown"
	"promptgrinder/internal/repository"
	"promptgrinder/internal/state"
	"promptgrinder/internal/terminal"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerpathpolicy"
)

type ExecutorFactory func(config.Config) (execution.Executor, error)
type TerminalAdapterSelector func(string, string) (terminal.TerminalAdapter, error)

type Manager struct {
	Store                   state.Store
	Engine                  engine.Engine
	Registry                engine.Registry
	EngineName              string
	EngineOverride          string
	SandboxOverride         string
	RepositoryOverride      string
	CodexSessionID          string
	CaptureCodexSession     bool
	Executable              string
	UseRepoConfig           bool
	BaseConfig              config.Config
	NewExecutor             ExecutorFactory
	TerminalAdapterOverride string
	TerminalModeOverride    string
	SelectTerminalAdapter   TerminalAdapterSelector
}

type LaunchResult struct {
	Worker state.Worker
	Err    error
}

func (m Manager) Launch(taskPath string) LaunchResult {
	absTask, err := filepath.Abs(taskPath)
	if err != nil {
		return LaunchResult{Err: err}
	}
	data, err := os.ReadFile(absTask)
	if err != nil {
		return LaunchResult{Err: err}
	}
	return m.launchData(absTask, data, nil)
}

func (m Manager) LaunchContent(taskPath, content string) LaunchResult {
	absTask, err := filepath.Abs(taskPath)
	if err != nil {
		return LaunchResult{Err: err}
	}
	return m.launchData(absTask, []byte(content), nil)
}

// LaunchContentWithMetadata launches a persisted prompt with
// orchestration-owned metadata instead of Markdown frontmatter.
func (m Manager) LaunchContentWithMetadata(taskPath, content string, metadata map[string]any) LaunchResult {
	absTask, err := filepath.Abs(taskPath)
	if err != nil {
		return LaunchResult{Err: err}
	}
	return m.launchData(absTask, []byte(content), metadata)
}

// ValidateContentWithMetadata performs launch preflight without creating a run
// record or starting a process.
func (m Manager) ValidateContentWithMetadata(taskPath, content string, metadata map[string]any) error {
	absTask, err := filepath.Abs(taskPath)
	if err != nil {
		return err
	}
	task, err := markdown.Parse(content)
	if err != nil {
		return fmt.Errorf("parse task %s: %w", absTask, err)
	}
	if strings.TrimSpace(task.Body) == "" {
		return fmt.Errorf("task prompt is empty: %s", absTask)
	}
	if err := markdown.Validate(task, absTask); err != nil {
		return err
	}
	if metadata == nil {
		metadata = task.Metadata
	}
	repoRootPath := absTask
	if m.RepositoryOverride != "" {
		repoRootPath = m.RepositoryOverride
	}
	repoRoot, err := repository.DetectRoot(repoRootPath)
	if err != nil {
		return err
	}
	if err := validateCommonMetadata(metadata); err != nil {
		return err
	}
	if err := validateExpectedPaths(task, metadata); err != nil {
		return err
	}
	cfg, engineName, resolved, adapter, err := m.resolveLaunchInputs(repoRoot, absTask, metadata)
	if err != nil {
		return err
	}
	ctx, err := validationContext(repoRoot, absTask, engineName, resolved, cfg)
	if err != nil {
		return err
	}
	if err := adapter.Validate(ctx); err != nil {
		return err
	}
	_, err = m.executor(cfg)
	return err
}

type ValidationPlan struct {
	Valid          bool           `json:"valid"`
	Engine         string         `json:"engine"`
	Warnings       []string       `json:"warnings"`
	Errors         []string       `json:"errors"`
	ExecutionPlan  map[string]any `json:"execution_plan"`
	RenderedPrompt string         `json:"-"`
}

func (m Manager) Validate(taskPath string) (ValidationPlan, error) {
	absTask, err := filepath.Abs(taskPath)
	if err != nil {
		return invalidValidationPlan("", err), err
	}
	data, err := os.ReadFile(absTask)
	if err != nil {
		return invalidValidationPlan("", err), err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		err = fmt.Errorf("parse task %s: %w", absTask, err)
		return invalidValidationPlan("", err), err
	}
	if strings.TrimSpace(task.Body) == "" {
		err := fmt.Errorf("task prompt is empty: %s", absTask)
		return invalidValidationPlan("", err), err
	}
	if err := markdown.Validate(task, absTask); err != nil {
		return invalidValidationPlan("", err), err
	}
	repoRoot, err := repository.DetectRoot(absTask)
	if err != nil {
		return invalidValidationPlan("", err), err
	}
	if err := validateCommonMetadata(task.Metadata); err != nil {
		return invalidValidationPlan("", err), err
	}
	if err := validateExpectedPaths(task, task.Metadata); err != nil {
		return invalidValidationPlan("", err), err
	}
	cfg, selected, resolved, adapter, err := m.resolveLaunchInputs(repoRoot, absTask, task.Metadata)
	if err != nil {
		return invalidValidationPlan(selected, err), err
	}
	ctx, err := validationContext(repoRoot, absTask, selected, resolved, cfg)
	if err != nil {
		return invalidValidationPlan(selected, err), err
	}
	if err := adapter.Validate(ctx); err != nil {
		return invalidValidationPlan(selected, err), err
	}
	rendered := markdown.Render(task)
	request, err := adapter.Build(ctx, rendered, m.Executable)
	if err != nil {
		return invalidValidationPlan(selected, err), err
	}
	executionPlan := map[string]any{
		"engine":             selected,
		"repository_path":    repoRoot,
		"task_path":          absTask,
		"working_directory":  ctx.WorkingDirectory,
		"timeout":            durationString(ctx.Timeout),
		"metadata":           resolved,
		"config":             validationConfigPlan(cfg),
		"capabilities":       adapter.Describe().Capabilities,
		"command":            request.CommandData,
		"prompt_bytes":       len(request.Prompt),
		"worker_would_start": false,
	}
	if preview, ok := request.CommandData["command_preview"].(string); ok && preview != "" {
		executionPlan["command_preview"] = preview
	}
	return ValidationPlan{
		Valid:          true,
		Engine:         selected,
		Warnings:       markdown.Warnings(task),
		Errors:         []string{},
		ExecutionPlan:  redactValue(executionPlan).(map[string]any),
		RenderedPrompt: string(request.Prompt),
	}, nil
}

func (m Manager) launchData(absTask string, data []byte, suppliedMetadata map[string]any) LaunchResult {
	task, err := markdown.Parse(string(data))
	if err != nil {
		return LaunchResult{Err: fmt.Errorf("parse task %s: %w", absTask, err)}
	}
	if strings.TrimSpace(task.Body) == "" {
		return LaunchResult{Err: fmt.Errorf("task prompt is empty: %s", absTask)}
	}
	if err := markdown.Validate(task, absTask); err != nil {
		return LaunchResult{Err: err}
	}
	repoRootPath := absTask
	if m.RepositoryOverride != "" {
		repoRootPath = m.RepositoryOverride
	}
	repoRoot, err := repository.DetectRoot(repoRootPath)
	if err != nil {
		return LaunchResult{Err: err}
	}
	taskMetadata := task.Metadata
	if suppliedMetadata != nil {
		taskMetadata = suppliedMetadata
	}
	if err := validateCommonMetadata(taskMetadata); err != nil {
		return LaunchResult{Err: err}
	}
	if err := validateExpectedPaths(task, taskMetadata); err != nil {
		return LaunchResult{Err: err}
	}
	cfg, engineName, metadata, adapter, err := m.resolveLaunchInputs(repoRoot, absTask, taskMetadata)
	if err != nil {
		return LaunchResult{Err: err}
	}
	if m.CodexSessionID != "" {
		metadata["codex_session_id"] = m.CodexSessionID
	}
	if m.CaptureCodexSession {
		metadata["codex_capture_session"] = true
	}
	if snapshot, snapshotErr := workerpathpolicy.Capture(repoRoot); snapshotErr == nil {
		if encoded, encodeErr := json.Marshal(snapshot); encodeErr == nil {
			metadata["ordinary_path_baseline"] = string(encoded)
			metadata["ordinary_allowed_paths"] = taskMetadata["allowed_paths"]
			metadata["ordinary_forbidden_paths"] = taskMetadata["forbidden_paths"]
		}
	}
	validateCtx, err := validationContext(repoRoot, absTask, engineName, metadata, cfg)
	if err != nil {
		return LaunchResult{Err: err}
	}
	if err := adapter.Validate(validateCtx); err != nil {
		return LaunchResult{Err: err}
	}
	executor, err := m.executor(cfg)
	if err != nil {
		return LaunchResult{Err: err}
	}

	worker, err := m.createWorker(repoRoot, absTask, engineName, engineNameFromMetadata(taskMetadata), taskMetadata, metadata, adapter.Describe().Capabilities, cfg)
	if err != nil {
		return LaunchResult{Err: err}
	}
	ctx, err := execution.NewContext(worker, cfg, map[string]string{})
	if err != nil {
		return LaunchResult{Worker: worker, Err: err}
	}
	request, err := adapter.Build(ctx, markdown.Render(task), m.Executable)
	if err != nil {
		return LaunchResult{Worker: worker, Err: err}
	}
	result, err := executor.Execute(request)
	return LaunchResult{Worker: result.Worker, Err: err}
}

func (m Manager) resolveLaunchInputs(repoRoot, taskPath string, taskMetadata map[string]any) (config.Config, string, map[string]any, engine.Engine, error) {
	cfg := m.BaseConfig
	engineName := m.defaultEngineName(cfg)
	if m.UseRepoConfig {
		loaded, err := config.LoadWithHome(repoRoot, cfg.HomeDir)
		if err != nil {
			return cfg, engineName, nil, nil, err
		}
		cfg = loaded
		engineName = m.defaultEngineName(cfg)
	}
	if taskEngine := engineNameFromMetadata(taskMetadata); taskEngine != "" {
		engineName = taskEngine
	}
	if m.EngineOverride != "" {
		engineName = m.EngineOverride
	}
	if m.SandboxOverride != "" {
		if engineName != "codex" {
			return cfg, engineName, nil, nil, fmt.Errorf("--sandbox is only supported by the codex engine")
		}
		cfg.CodexSandbox = m.SandboxOverride
	}
	metadata := resolvedMetadata(taskMetadata, cfg, engineName)
	if m.SandboxOverride != "" {
		engineMetadata := metadata["engine"].(map[string]any)
		engineMetadata["sandbox"] = m.SandboxOverride
	}
	adapter, err := m.lookupEngine(engineName)
	if err != nil {
		return cfg, engineName, metadata, adapter, err
	}
	if engineName == "codex" && cfg.CodexExecutable != "" {
		if configurable, ok := adapter.(interface {
			WithCommand(string) engine.Engine
		}); ok {
			adapter = configurable.WithCommand(cfg.CodexExecutable)
		}
	}
	if engineName == "codex" {
		resolvedExecutable, err := resolveExecutable(cfg.CodexExecutable, "codex")
		if err != nil {
			return cfg, engineName, metadata, adapter, err
		}
		cfg.CodexExecutable = resolvedExecutable
		if configurable, ok := adapter.(interface {
			WithCommand(string) engine.Engine
		}); ok {
			adapter = configurable.WithCommand(resolvedExecutable)
		}
	}
	if resolver, ok := adapter.(engine.MetadataResolver); ok {
		ctx, err := validationContext(repoRoot, taskPath, engineName, metadata, cfg)
		if err != nil {
			return cfg, engineName, metadata, adapter, err
		}
		metadata, err = resolver.ResolveMetadata(ctx)
		if err != nil {
			return cfg, engineName, metadata, adapter, err
		}
	}
	return cfg, engineName, metadata, adapter, nil
}

func (m Manager) defaultEngineName(cfg config.Config) string {
	if m.EngineName != "" {
		return m.EngineName
	}
	if cfg.Engine != "" {
		return cfg.Engine
	}
	return "codex"
}

func (m Manager) lookupEngine(name string) (engine.Engine, error) {
	if len(m.Registry.List()) > 0 {
		return m.Registry.Lookup(name)
	}
	if m.Engine != nil && (name == "" || name == m.Engine.Name()) {
		return m.Engine, nil
	}
	return nil, fmt.Errorf("unknown engine: %s", name)
}

func validationContext(repoRoot, taskPath, engineName string, metadata map[string]any, cfg config.Config) (execution.Context, error) {
	environment, err := environmentFromMetadata(metadata)
	if err != nil {
		return execution.Context{}, err
	}
	worker := state.Worker{
		ID:             "validation",
		RepositoryPath: repoRoot,
		TaskPath:       taskPath,
		PromptPath:     filepath.Join(repoRoot, ".promptgrinder-validation-prompt.md"),
		Engine:         engineName,
		Metadata:       metadata,
		RecordPath:     filepath.Join(repoRoot, ".promptgrinder-validation-worker.json"),
		LogPath:        filepath.Join(repoRoot, ".promptgrinder-validation.log"),
	}
	return execution.NewContext(worker, cfg, environment)
}

func engineNameFromMetadata(metadata map[string]any) string {
	switch value := metadata["engine"].(type) {
	case string:
		return value
	case map[string]any:
		if name, ok := value["name"].(string); ok {
			return name
		}
	}
	return ""
}

func resolvedMetadata(metadata map[string]any, cfg config.Config, engineName string) map[string]any {
	out := map[string]any{}
	for key, value := range metadata {
		if isSemanticMetadataKey(key) {
			continue
		}
		out[key] = value
	}
	engineMap := map[string]any{}
	if existing, ok := out["engine"].(map[string]any); ok {
		for key, value := range existing {
			engineMap[key] = value
		}
	}
	engineMap["name"] = engineName
	out["engine"] = engineMap
	if _, ok := out["timeout"]; !ok && cfg.WorkerTimeout > 0 {
		out["timeout"] = cfg.WorkerTimeout.String()
	}
	return out
}

func isSemanticMetadataKey(key string) bool {
	switch key {
	case "acceptance_criteria", "allowed_paths", "forbidden_paths", "expected_paths", "validation":
		return true
	default:
		return false
	}
}

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
}

func invalidValidationPlan(engineName string, err error) ValidationPlan {
	message := ""
	if err != nil {
		message = err.Error()
	}
	errors := []string{}
	if message != "" {
		errors = []string{message}
	}
	return ValidationPlan{
		Valid:         false,
		Engine:        engineName,
		Warnings:      []string{},
		Errors:        errors,
		ExecutionPlan: map[string]any{},
	}
}

func validateCommonMetadata(metadata map[string]any) error {
	values := func(key string) []string {
		items, _ := metadata[key].([]any)
		out := make([]string, 0, len(items))
		for _, item := range items {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	}
	policy := workerdomain.WorkerPolicy{AllowedPaths: values("allowed_paths"), ForbiddenPaths: values("forbidden_paths")}
	if err := workerpathpolicy.ValidatePatterns(policy); err != nil {
		return err
	}
	if expected := values("expected_paths"); len(expected) > 0 {
		violations, err := workerpathpolicy.Violations(policy, expected)
		if err != nil {
			return err
		}
		if len(violations) > 0 {
			return fmt.Errorf("expected_paths are not permitted by the path policy: %s", formatPolicyViolations(violations))
		}
	}
	if err := validateEngineMetadata(metadata["engine"]); err != nil {
		return err
	}
	if value, ok := metadata["working_directory"]; ok && value != nil {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("working_directory must be a string relative to the repository root")
		}
	}
	if value, ok := metadata["timeout"]; ok && value != nil {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("timeout must be a duration string like 30s, 10m, or 2h")
		}
	}
	if value, ok := metadata["labels"]; ok && value != nil {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("labels must be a list of strings")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("labels must contain only strings")
			}
		}
	}
	if _, err := environmentFromMetadata(metadata); err != nil {
		return err
	}
	return nil
}

func validateExpectedPaths(task markdown.Task, metadata map[string]any) error {
	expected, err := markdown.ExpectedPaths(task)
	if err != nil {
		return err
	}
	values := func(key string) []string {
		items, _ := metadata[key].([]any)
		out := make([]string, 0, len(items))
		for _, item := range items {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	}
	policy := workerdomain.WorkerPolicy{AllowedPaths: values("allowed_paths"), ForbiddenPaths: values("forbidden_paths")}
	violations, err := workerpathpolicy.Violations(policy, expected)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("expected_paths are not permitted by the path policy: %s", formatPolicyViolations(violations))
	}
	return nil
}

func formatPolicyViolations(violations []workerpathpolicy.Violation) string {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, fmt.Sprintf("%s (%s)", violation.Path, violation.Reason))
	}
	return strings.Join(parts, ", ")
}

func validateEngineMetadata(value any) error {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		return nil
	case map[string]any:
		if name, ok := typed["name"]; ok && name != nil {
			if _, ok := name.(string); !ok {
				return fmt.Errorf("engine.name must be a string")
			}
		}
		return nil
	default:
		return fmt.Errorf("engine metadata must be a string or map")
	}
}

func environmentFromMetadata(metadata map[string]any) (map[string]string, error) {
	value, ok := metadata["env"]
	if !ok || value == nil {
		return map[string]string{}, nil
	}
	envMap, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("env must be a map of string environment variables")
	}
	out := map[string]string{}
	for key, raw := range envMap {
		if !validEnvironmentName(key) {
			return nil, fmt.Errorf("env key %q must match [A-Za-z_][A-Za-z0-9_]*", key)
		}
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("env.%s must be a string", key)
		}
		out[key] = text
	}
	return out, nil
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func resolveExecutable(configured, fallback string) (string, error) {
	candidate := configured
	if candidate == "" {
		candidate = fallback
	}
	path, err := exec.LookPath(candidate)
	if err != nil {
		if configured != "" {
			return "", fmt.Errorf("configured engine.codex.executable %q is not executable: %w", configured, err)
		}
		return "", fmt.Errorf("Codex executable was not found on PATH; set engine.codex.executable to an absolute path: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Codex executable %q: %w", path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("Codex executable %q is not an executable file", absolute)
	}
	return filepath.Clean(absolute), nil
}

func validationConfigPlan(cfg config.Config) map[string]any {
	return map[string]any{
		"home_dir":                  cfg.HomeDir,
		"engine_default":            cfg.Engine,
		"terminal_adapter":          cfg.TerminalAdapter,
		"terminal_mode":             cfg.TerminalMode,
		"worker_heartbeat_interval": durationString(cfg.WorkerHeartbeatInterval),
		"worker_timeout":            durationString(cfg.WorkerTimeout),
		"engine_codex_sandbox":      cfg.CodexSandbox,
		"engine_codex_approval":     cfg.CodexApproval,
		"engine_codex_executable":   cfg.CodexExecutable,
	}
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range typed {
			if isSecretKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactValue(item)
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "PASS", "KEY", "API_KEY", "PRIVATE", "CREDENTIAL"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

func (m Manager) createWorker(repoRoot, taskPath, engineName, requestedEngine string, metadata, resolved map[string]any, capabilities any, cfg config.Config) (state.Worker, error) {
	if err := m.Store.Ensure(); err != nil {
		return state.Worker{}, err
	}
	id, err := newWorkerID()
	if err != nil {
		return state.Worker{}, err
	}
	dir := m.Store.WorkerDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return state.Worker{}, err
	}
	worker := state.Worker{
		ID:               id,
		RecordPath:       filepath.Join(dir, "worker.json"),
		RepositoryPath:   repoRoot,
		TaskPath:         taskPath,
		PromptPath:       filepath.Join(dir, "prompt.md"),
		Engine:           engineName,
		EngineExecutable: cfg.CodexExecutable,
		RequestedEngine:  requestedEngine,
		EngineOverride:   m.EngineOverride,
		EngineOverridden: m.EngineOverride != "",
		Status:           state.StatusCreated,
		LogPath:          filepath.Join(dir, "worker.log"),
		SummaryPath:      filepath.Join(filepath.Dir(m.Store.WorkersDir), "summaries", "workers", id+".md"),
		EventPath:        filepath.Join(dir, "events.jsonl"),
		CloseOnFinish:    cfg.TerminalCloseOnFinish,
		CloseOnFailure:   cfg.TerminalCloseOnFailure,
		Metadata:         metadata,
		ResolvedMetadata: resolved,
		Capabilities:     capabilities,
	}
	if err := m.Store.Save(worker); err != nil {
		return state.Worker{}, err
	}
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventWorkerCreated, state.SeverityInfo, "Worker created", map[string]any{"task_path": taskPath}))
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineSelected, state.SeverityInfo, "Engine selected", map[string]any{"engine": engineName, "requested_engine": requestedEngine, "engine_override": m.EngineOverride}))
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineValidated, state.SeverityInfo, "Engine metadata validated", map[string]any{"engine": engineName}))
	return worker, nil
}

func (m Manager) executor(cfg config.Config) (execution.Executor, error) {
	if m.TerminalAdapterOverride != "" {
		cfg.TerminalAdapter = m.TerminalAdapterOverride
		cfg.TerminalMode = m.TerminalModeOverride
		if cfg.TerminalMode == "" {
			cfg.TerminalMode = "normal"
		}
	}
	executor := execution.Executor{Store: m.Store}
	if m.NewExecutor != nil {
		var err error
		executor, err = m.NewExecutor(cfg)
		if err != nil {
			return executor, err
		}
	}
	if m.TerminalAdapterOverride != "" {
		selector := m.SelectTerminalAdapter
		if selector == nil {
			selector = terminal.SelectAdapter
		}
		adapter, err := selector(cfg.TerminalAdapter, cfg.TerminalMode)
		if err != nil {
			return execution.Executor{}, err
		}
		executor.Terminal = adapter
	}
	return executor, nil
}

func newWorkerID() (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrk_%s_%s", time.Now().UTC().Format("20060102150405"), hex.EncodeToString(suffix[:])), nil
}
