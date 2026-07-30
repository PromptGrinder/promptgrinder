package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"promptgrinder/internal/workerdomain"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type Config struct {
	HomeDir                       string                    `json:"home_dir"`
	Engine                        string                    `json:"engine"`
	TerminalAdapter               string                    `json:"terminal_adapter"`
	TerminalMode                  string                    `json:"terminal_mode"`
	TerminalCloseOnFinish         bool                      `json:"terminal_close_on_finish"`
	TerminalCloseOnFailure        bool                      `json:"terminal_close_on_failure"`
	WorkerHeartbeatInterval       time.Duration             `json:"worker_heartbeat_interval"`
	WorkerTimeout                 time.Duration             `json:"worker_timeout"`
	CodexSandbox                  string                    `json:"codex_sandbox"`
	CodexApproval                 string                    `json:"codex_approval"`
	CodexExecutable               string                    `json:"codex_executable,omitempty"`
	RunEngine                     string                    `json:"run_engine"`
	RunFolderRepo                 string                    `json:"run_folder_repo"`
	RunFolderTemplate             string                    `json:"run_folder_template"`
	RunFolderEngine               string                    `json:"run_folder_engine"`
	RunFolderResume               bool                      `json:"run_folder_resume"`
	RunFolderFresh                bool                      `json:"run_folder_fresh"`
	RunFolderRestart              bool                      `json:"run_folder_restart"`
	RunFolderNoResume             bool                      `json:"run_folder_no_resume"`
	RunFolderCheckpoint           bool                      `json:"run_folder_checkpoint"`
	RunFolderCommitEach           bool                      `json:"run_folder_commit_each"`
	RunFolderRequireCleanGit      bool                      `json:"run_folder_require_clean_git"`
	RunFolderIncludeSpecification bool                      `json:"run_folder_include_specification"`
	RunFolderDetach               bool                      `json:"run_folder_detach"`
	WorkerRuntime                 string                    `json:"worker_runtime,omitempty"`
	RuntimeOptions                map[string]map[string]any `json:"runtime_options,omitempty"`
	SchedulerProjectConcurrency   int                       `json:"scheduler_project_concurrency,omitempty"`
	SchedulerRuntimeConcurrency   map[string]int            `json:"scheduler_runtime_concurrency,omitempty"`
	SchedulerLeaseTTL             time.Duration             `json:"scheduler_lease_ttl,omitempty"`
	Warnings                      []string                  `json:"warnings,omitempty"`
}

type DefaultsReport struct {
	HomeDir                string `json:"home_dir"`
	TemplatePath           string `json:"template_path"`
	TemplateExists         bool   `json:"template_exists"`
	UserConfigPath         string `json:"user_config_path"`
	UserConfigExists       bool   `json:"user_config_exists"`
	RepositoryConfigPath   string `json:"repository_config_path"`
	RepositoryConfigExists bool   `json:"repository_config_exists"`
	Config                 Config `json:"config"`
}

func Load(repoRoot string) (Config, error) {
	return LoadWithHome(repoRoot, "")
}

func LoadWithHome(repoRoot, homeOverride string) (Config, error) {
	homeDir, err := ResolveHomeDir(homeOverride)
	if err != nil {
		return Config{}, err
	}

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.SetEnvPrefix("PROMPTGRINDER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("terminal.adapter", "terminal")
	v.SetDefault("terminal.mode", "normal")
	v.SetDefault("terminal.close_on_finish", true)
	v.SetDefault("terminal.close_on_failure", false)
	v.SetDefault("worker.heartbeat_interval", "30s")
	v.SetDefault("scheduler.lease_ttl", "1m")
	v.SetDefault("run_folder.template", "codex")
	v.SetDefault("run_folder.detach", true)
	if err := validateEnvironmentKeys(); err != nil {
		return Config{}, err
	}

	warnings := []string{}
	if err := mergeConfigFile(v, DefaultTemplatePath(homeDir), &warnings); err != nil {
		return Config{}, err
	}

	if err := mergeConfigFile(v, filepath.Join(homeDir, "config.yaml"), &warnings); err != nil {
		return Config{}, err
	}

	if repoRoot != "" {
		if err := mergeConfigFile(v, filepath.Join(repoRoot, ".ai", "config.yaml"), &warnings); err != nil {
			return Config{}, err
		}
	}

	heartbeat, err := ParseDuration(v.GetString("worker.heartbeat_interval"))
	if err != nil {
		return Config{}, err
	}
	leaseTTL, err := ParseDuration(v.GetString("scheduler.lease_ttl"))
	if err != nil {
		return Config{}, fmt.Errorf("scheduler lease ttl: %w", err)
	}
	if leaseTTL <= 0 {
		return Config{}, fmt.Errorf("scheduler lease ttl must be positive")
	}
	var workerTimeout time.Duration
	if timeoutValue := v.GetString("worker.timeout"); timeoutValue != "" {
		workerTimeout, err = ParseDuration(timeoutValue)
		if err != nil {
			return Config{}, err
		}
	}

	engine := v.GetString("engine.default")
	if engine == "" && v.GetString("engine") != "" {
		engine = v.GetString("engine")
	}
	if engine == "" {
		engine = "codex"
	}
	codexSandbox := v.GetString("engine.codex.sandbox")
	if codexSandbox == "" {
		codexSandbox = "workspace-write"
	}
	codexApproval := v.GetString("engine.codex.approval")
	if codexApproval == "" {
		codexApproval = "never"
	}

	cfg := Config{
		HomeDir:                       homeDir,
		Engine:                        engine,
		TerminalAdapter:               v.GetString("terminal.adapter"),
		TerminalMode:                  v.GetString("terminal.mode"),
		TerminalCloseOnFinish:         v.GetBool("terminal.close_on_finish"),
		TerminalCloseOnFailure:        v.GetBool("terminal.close_on_failure"),
		WorkerHeartbeatInterval:       heartbeat,
		WorkerTimeout:                 workerTimeout,
		CodexSandbox:                  codexSandbox,
		CodexApproval:                 codexApproval,
		CodexExecutable:               v.GetString("engine.codex.executable"),
		RunEngine:                     v.GetString("run.engine"),
		RunFolderRepo:                 valueOrDefault(v.GetString("run_folder.repo"), "."),
		RunFolderTemplate:             valueOrDefault(v.GetString("run_folder.template"), "codex"),
		RunFolderEngine:               v.GetString("run_folder.engine"),
		RunFolderResume:               v.GetBool("run_folder.resume"),
		RunFolderFresh:                v.GetBool("run_folder.fresh"),
		RunFolderRestart:              v.GetBool("run_folder.restart"),
		RunFolderNoResume:             v.GetBool("run_folder.no_resume"),
		RunFolderCheckpoint:           v.GetBool("run_folder.checkpoint"),
		RunFolderCommitEach:           v.GetBool("run_folder.commit_each"),
		RunFolderRequireCleanGit:      v.GetBool("run_folder.require_clean_git"),
		RunFolderIncludeSpecification: v.GetBool("run_folder.include_specification"),
		RunFolderDetach:               v.GetBool("run_folder.detach"),
		WorkerRuntime:                 v.GetString("runtime.default"),
		SchedulerProjectConcurrency:   v.GetInt("scheduler.project_concurrency"),
		SchedulerRuntimeConcurrency:   intMap(v.GetStringMap("scheduler.runtime_concurrency")),
		SchedulerLeaseTTL:             leaseTTL,
		RuntimeOptions:                runtimeOptions(v.GetStringMap("runtime")),
		Warnings:                      warnings,
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func runtimeOptions(values map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	for name, value := range values {
		if name == "default" {
			continue
		}
		if options, ok := value.(map[string]any); ok {
			result[name] = options
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func intMap(values map[string]any) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case int:
			result[key] = typed
		case int64:
			result[key] = int(typed)
		case float64:
			result[key] = int(typed)
		case string:
			if parsed, err := strconv.Atoi(typed); err == nil {
				result[key] = parsed
			}
		}
	}
	return result
}

func ResolveHomeDir(homeOverride string) (string, error) {
	homeDir := homeOverride
	if homeDir == "" {
		homeDir = os.Getenv("PROMPTGRINDER_HOME")
	}
	if homeDir != "" {
		return homeDir, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".promptgrinder"), nil
}

func DefaultTemplatePath(homeDir string) string {
	return filepath.Join(homeDir, "templates", "default.yaml")
}

func DefaultTemplateExample() string {
	return strings.TrimSpace(`# PromptGrinder default template.
# Edit this file or override values in ~/.promptgrinder/config.yaml,
# repository .ai/config.yaml, environment variables, or CLI flags.

engine:
  default: codex
  codex:
    sandbox: workspace-write
    approval: never

terminal:
  adapter: terminal
  mode: normal
  close_on_finish: true
  close_on_failure: false

worker:
  heartbeat_interval: 30s
  timeout: 45m

run:
  engine: codex

run_folder:
  repo: .
  template: codex
  engine: codex
  resume: false
  fresh: false
  restart: false
  no_resume: false
  checkpoint: true
  commit_each: true
  require_clean_git: false
  include_specification: false
  detach: true
`) + "\n"
}

func EnsureDefaultTemplate(homeDir string, stdin io.Reader, stdout io.Writer) (bool, error) {
	path := DefaultTemplatePath(homeDir)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	fmt.Fprintf(stdout, "No PromptGrinder default template found at %s.\n", path)
	fmt.Fprintln(stdout, "PromptGrinder can create this example so shorter commands can use your defaults:")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, DefaultTemplateExample())
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Create this default template now? [y/N] ")
	reader := bufio.NewReader(stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		fmt.Fprintln(stdout, "Skipped default template creation. Run `promptgrinder defaults` to see the example later.")
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(DefaultTemplateExample()), 0o644); err != nil {
		return false, err
	}
	fmt.Fprintf(stdout, "Created %s\n", path)
	return true, nil
}

func Defaults(repoRoot string, cfg Config) DefaultsReport {
	templatePath := DefaultTemplatePath(cfg.HomeDir)
	userConfigPath := filepath.Join(cfg.HomeDir, "config.yaml")
	repoConfigPath := ""
	repoConfigExists := false
	if repoRoot != "" {
		repoConfigPath = filepath.Join(repoRoot, ".ai", "config.yaml")
		repoConfigExists = fileExists(repoConfigPath)
	}
	return DefaultsReport{
		HomeDir:                cfg.HomeDir,
		TemplatePath:           templatePath,
		TemplateExists:         fileExists(templatePath),
		UserConfigPath:         userConfigPath,
		UserConfigExists:       fileExists(userConfigPath),
		RepositoryConfigPath:   repoConfigPath,
		RepositoryConfigExists: repoConfigExists,
		Config:                 cfg,
	}
}

func mergeConfigFile(v *viper.Viper, path string, warnings *[]string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read configuration %s: %w", path, err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("parse configuration %s: %w", path, err)
	}
	if err := validateConfigKeys(path, values, warnings); err != nil {
		return err
	}
	fileV := viper.New()
	fileV.SetConfigFile(path)
	fileV.SetConfigType("yaml")
	if err := fileV.ReadInConfig(); err == nil {
		return v.MergeConfigMap(fileV.AllSettings())
	} else {
		return fmt.Errorf("parse configuration %s: %w", path, err)
	}
}

var configSchema = map[string]any{
	"engine": map[string]any{
		"default": nil,
		"codex":   map[string]any{"sandbox": nil, "approval": nil, "executable": nil},
	},
	"terminal": map[string]any{"adapter": nil, "mode": nil, "close_on_finish": nil, "close_on_failure": nil},
	"worker":   map[string]any{"heartbeat_interval": nil, "timeout": nil},
	"runtime":  map[string]any{"default": nil},
	"scheduler": map[string]any{
		"project_concurrency": nil, "lease_ttl": nil, "runtime_concurrency": map[string]any{},
	},
	"run": map[string]any{"engine": nil},
	"run_folder": map[string]any{
		"repo": nil, "template": nil, "engine": nil, "resume": nil, "fresh": nil,
		"restart": nil, "no_resume": nil, "checkpoint": nil, "commit_each": nil,
		"require_clean_git": nil, "include_specification": nil, "detach": nil,
	},
}

func validateConfigKeys(source string, values map[string]any, warnings *[]string) error {
	if legacy, ok := values["engine"].(string); ok {
		*warnings = append(*warnings, fmt.Sprintf("%s: key engine is deprecated; migrate to engine.default", source))
		values["engine"] = map[string]any{"default": legacy}
	}
	valuesForSchema := values
	schemaForValidation := configSchema
	if runtimeValue, ok := values["runtime"]; ok {
		runtimes, ok := runtimeValue.(map[string]any)
		if !ok {
			return fmt.Errorf("configuration %s: key runtime must be a map", source)
		}
		runtimeSchemaValue := map[string]any{}
		if fallback, ok := runtimes["default"]; ok {
			name, ok := fallback.(string)
			if !ok {
				return fmt.Errorf("configuration %s: runtime.default must be a string", source)
			}
			if err := (workerdomain.RuntimeRef{Name: name}).Validate(); err != nil {
				return fmt.Errorf("configuration %s: runtime.default: %w", source, err)
			}
			runtimeSchemaValue["default"] = fallback
		}
		for name, options := range runtimes {
			if name == "default" {
				continue
			}
			if err := (workerdomain.RuntimeRef{Name: name}).Validate(); err != nil {
				return fmt.Errorf("configuration %s: runtime namespace: %w", source, err)
			}
			if _, ok := options.(map[string]any); !ok {
				return fmt.Errorf("configuration %s: runtime.%s must be a map", source, name)
			}
		}
		valuesForSchema = make(map[string]any, len(values))
		for key, value := range values {
			valuesForSchema[key] = value
		}
		valuesForSchema["runtime"] = runtimeSchemaValue
	}
	if schedulerValue, ok := values["scheduler"]; ok {
		schedulerValues, ok := schedulerValue.(map[string]any)
		if !ok {
			return fmt.Errorf("configuration %s: key scheduler must be a map", source)
		}
		dynamicSchema := map[string]any{
			"project_concurrency": nil, "lease_ttl": nil, "runtime_concurrency": map[string]any{},
		}
		if runtimeValue, ok := schedulerValues["runtime_concurrency"]; ok {
			runtimeLimits, ok := runtimeValue.(map[string]any)
			if !ok {
				return fmt.Errorf("configuration %s: scheduler.runtime_concurrency must be a map", source)
			}
			runtimeSchema := map[string]any{}
			for name := range runtimeLimits {
				if err := workerdomain.ValidateSlug("scheduler runtime name", name); err != nil {
					return fmt.Errorf("configuration %s: %w", source, err)
				}
				runtimeSchema[name] = nil
			}
			dynamicSchema["runtime_concurrency"] = runtimeSchema
		}
		schemaCopy := make(map[string]any, len(configSchema))
		for key, value := range configSchema {
			schemaCopy[key] = value
		}
		schemaCopy["scheduler"] = dynamicSchema
		schemaForValidation = schemaCopy
	}
	var walk func(map[string]any, map[string]any, string) error
	walk = func(input, schema map[string]any, prefix string) error {
		keys := make([]string, 0, len(input))
		for key := range input {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			expected, ok := schema[key]
			if !ok {
				return fmt.Errorf("configuration %s: unknown key %s", source, full)
			}
			childSchema, nested := expected.(map[string]any)
			if !nested {
				continue
			}
			child, ok := input[key].(map[string]any)
			if !ok {
				return fmt.Errorf("configuration %s: key %s must be a map", source, full)
			}
			if err := walk(child, childSchema, full); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(valuesForSchema, schemaForValidation, ""); err != nil {
		return err
	}
	return validateSourceValues(source, values)
}

func validateSourceValues(source string, values map[string]any) error {
	checkEnum := func(key string, allowed ...string) error {
		value, ok := nestedValue(values, strings.Split(key, ".")...)
		if !ok {
			return nil
		}
		text, ok := value.(string)
		if !ok || !oneOf(text, allowed...) {
			return fmt.Errorf("configuration %s: invalid value for %s", source, key)
		}
		return nil
	}
	for key, allowed := range map[string][]string{
		"engine.default":        {"codex"},
		"engine.codex.sandbox":  {"read-only", "workspace-write", "danger-full-access"},
		"engine.codex.approval": {"untrusted", "on-failure", "on-request", "never"},
		"terminal.adapter":      {"terminal", "iterm", "headless"},
		"terminal.mode":         {"normal", "dry-run"},
		"run.engine":            {"", "codex"},
		"run_folder.engine":     {"", "codex"},
	} {
		if err := checkEnum(key, allowed...); err != nil {
			return err
		}
	}
	for _, key := range []string{"worker.heartbeat_interval", "worker.timeout"} {
		value, ok := nestedValue(values, strings.Split(key, ".")...)
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("configuration %s: %s must be a duration string", source, key)
		}
		duration, err := time.ParseDuration(text)
		if err != nil || duration <= 0 {
			return fmt.Errorf("configuration %s: invalid value for %s", source, key)
		}
	}
	selected := 0
	for _, key := range []string{"resume", "fresh", "restart", "no_resume"} {
		if value, ok := nestedValue(values, "run_folder", key); ok {
			flag, valid := value.(bool)
			if !valid {
				return fmt.Errorf("configuration %s: run_folder.%s must be a boolean", source, key)
			}
			if flag {
				selected++
			}
		}
	}
	if selected > 1 {
		return fmt.Errorf("configuration %s: run_folder.resume, fresh, restart, and no_resume are mutually exclusive", source)
	}
	return nil
}

func nestedValue(values map[string]any, path ...string) (any, bool) {
	var current any = values
	for _, part := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func Validate(cfg Config) error {
	if cfg.SchedulerProjectConcurrency < 0 {
		return fmt.Errorf("scheduler project concurrency must not be negative")
	}
	for runtimeName, limit := range cfg.SchedulerRuntimeConcurrency {
		if err := workerdomain.ValidateSlug("scheduler runtime name", runtimeName); err != nil {
			return err
		}
		if limit < 0 {
			return fmt.Errorf("scheduler runtime concurrency for %q must not be negative", runtimeName)
		}
	}
	if !oneOf(cfg.Engine, "codex") {
		return fmt.Errorf("invalid engine.default %q: supported value is codex", cfg.Engine)
	}
	if !oneOf(cfg.TerminalAdapter, "terminal", "iterm", "headless") {
		return fmt.Errorf("invalid terminal.adapter %q: use terminal, iterm, or headless", cfg.TerminalAdapter)
	}
	if !oneOf(cfg.TerminalMode, "normal", "dry-run") {
		return fmt.Errorf("invalid terminal.mode %q: use normal or dry-run", cfg.TerminalMode)
	}
	if !oneOf(cfg.CodexSandbox, "read-only", "workspace-write", "danger-full-access") {
		return fmt.Errorf("invalid engine.codex.sandbox %q", cfg.CodexSandbox)
	}
	if !oneOf(cfg.CodexApproval, "untrusted", "on-failure", "on-request", "never") {
		return fmt.Errorf("invalid engine.codex.approval %q", cfg.CodexApproval)
	}
	if cfg.WorkerHeartbeatInterval < time.Second || cfg.WorkerHeartbeatInterval > 24*time.Hour {
		return fmt.Errorf("worker.heartbeat_interval must be between 1s and 24h")
	}
	if cfg.WorkerTimeout < 0 || cfg.WorkerTimeout > 30*24*time.Hour {
		return fmt.Errorf("worker.timeout must be no greater than 720h")
	}
	if cfg.RunEngine != "" && cfg.RunEngine != "codex" {
		return fmt.Errorf("invalid run.engine %q: supported value is codex", cfg.RunEngine)
	}
	if cfg.RunFolderEngine != "" && cfg.RunFolderEngine != "codex" {
		return fmt.Errorf("invalid run_folder.engine %q: supported value is codex", cfg.RunFolderEngine)
	}
	if strings.TrimSpace(cfg.RunFolderRepo) == "" || strings.ContainsAny(cfg.RunFolderRepo, "\r\n") {
		return fmt.Errorf("run_folder.repo must be a non-empty path without newlines")
	}
	if strings.TrimSpace(cfg.RunFolderTemplate) == "" || strings.ContainsAny(cfg.RunFolderTemplate, "\r\n") {
		return fmt.Errorf("run_folder.template must be a non-empty name without newlines")
	}
	selected := 0
	for _, value := range []bool{cfg.RunFolderResume, cfg.RunFolderFresh, cfg.RunFolderRestart, cfg.RunFolderNoResume} {
		if value {
			selected++
		}
	}
	if selected > 1 {
		return fmt.Errorf("run_folder.resume, fresh, restart, and no_resume are mutually exclusive")
	}
	if cfg.CodexExecutable != "" && strings.ContainsAny(cfg.CodexExecutable, "\r\n") {
		return fmt.Errorf("engine.codex.executable must not contain newlines")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateEnvironmentKeys() error {
	allowed := map[string]bool{
		"PROMPTGRINDER_HOME": true, "PROMPTGRINDER_PLAIN": true, "PROMPTGRINDER_HEADLESS": true,
	}
	var add func(map[string]any, string)
	add = func(schema map[string]any, prefix string) {
		for key, value := range schema {
			full := key
			if prefix != "" {
				full = prefix + "_" + key
			}
			if child, ok := value.(map[string]any); ok {
				add(child, full)
			} else {
				allowed["PROMPTGRINDER_"+strings.ToUpper(full)] = true
			}
		}
	}
	add(configSchema, "")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PROMPTGRINDER_") && !allowed[key] {
			return fmt.Errorf("environment configuration: unknown key %s", key)
		}
	}
	return nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func ParseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	return duration, nil
}
