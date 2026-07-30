package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesPromptGrinderHome(t *testing.T) {
	t.Setenv("PROMPTGRINDER_HOME", filepath.Join(t.TempDir(), "home"))

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HomeDir != os.Getenv("PROMPTGRINDER_HOME") {
		t.Fatalf("HomeDir = %q, want %q", cfg.HomeDir, os.Getenv("PROMPTGRINDER_HOME"))
	}
	if cfg.Engine != "codex" {
		t.Fatalf("Engine = %q, want codex", cfg.Engine)
	}
	if cfg.WorkerHeartbeatInterval != 30*time.Second {
		t.Fatalf("WorkerHeartbeatInterval = %s, want 30s", cfg.WorkerHeartbeatInterval)
	}
}

func TestLoadRepositoryOverride(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ai", "config.yaml"), []byte("terminal:\n  mode: dry-run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", filepath.Join(t.TempDir(), "home"))

	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TerminalMode != "dry-run" {
		t.Fatalf("TerminalMode = %q, want dry-run", cfg.TerminalMode)
	}
}

func TestLoadSchedulerConcurrencyLimits(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := "scheduler:\n  project_concurrency: 3\n  lease_ttl: 45s\n  runtime_concurrency:\n    codex: 2\n"
	if err := os.WriteFile(filepath.Join(repo, ".ai", "config.yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithHome(repo, filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchedulerProjectConcurrency != 3 || cfg.SchedulerRuntimeConcurrency["codex"] != 2 || cfg.SchedulerLeaseTTL != 45*time.Second {
		t.Fatalf("scheduler config = %#v", cfg)
	}
}

func TestLoadKeepsRuntimeConfigurationNamespacedAndOpaque(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `runtime:
  default: local
  claude:
    model: sonnet
    authentication:
      api_token: secret
`
	if err := os.WriteFile(filepath.Join(repo, ".ai", "config.yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithHome(repo, filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerRuntime != "local" {
		t.Fatalf("worker runtime = %q", cfg.WorkerRuntime)
	}
	claude := cfg.RuntimeOptions["claude"]
	if claude["model"] != "sonnet" {
		t.Fatalf("runtime options = %#v", cfg.RuntimeOptions)
	}
	authentication, ok := claude["authentication"].(map[string]any)
	if !ok || authentication["api_token"] != "secret" {
		t.Fatalf("nested runtime options = %#v", claude["authentication"])
	}
}

func TestLoadConfigurationPrecedence(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("terminal:\n  adapter: headless\n  mode: normal\nengine: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ai", "config.yaml"), []byte("terminal:\n  adapter: terminal\nengine: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != "codex" {
		t.Fatalf("Engine = %q, want codex", cfg.Engine)
	}
	if cfg.TerminalAdapter != "terminal" {
		t.Fatalf("TerminalAdapter = %q, want terminal", cfg.TerminalAdapter)
	}
	if !cfg.TerminalCloseOnFinish || cfg.TerminalCloseOnFailure {
		t.Fatalf("terminal close defaults = finish:%t failure:%t", cfg.TerminalCloseOnFinish, cfg.TerminalCloseOnFailure)
	}
	if !cfg.RunFolderDetach {
		t.Fatal("RunFolderDetach = false, want true")
	}
	if cfg.TerminalMode != "normal" {
		t.Fatalf("TerminalMode = %q, want user normal mode", cfg.TerminalMode)
	}
}

func TestEnvironmentOverridesRepositoryConfiguration(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ai", "config.yaml"), []byte("terminal:\n  adapter: terminal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_TERMINAL_ADAPTER", "headless")
	cfg, err := LoadWithHome(repo, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TerminalAdapter != "headless" {
		t.Fatalf("TerminalAdapter = %q, want environment override", cfg.TerminalAdapter)
	}
}

func TestLoadRejectsUnknownEnvironmentConfiguration(t *testing.T) {
	t.Setenv("PROMPTGRINDER_TERMINAL_ADAPTR", "headless")
	_, err := LoadWithHome("", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "PROMPTGRINDER_TERMINAL_ADAPTR") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadHeartbeatInterval(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("worker:\n  heartbeat_interval: 5s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerHeartbeatInterval != 5*time.Second {
		t.Fatalf("WorkerHeartbeatInterval = %s, want 5s", cfg.WorkerHeartbeatInterval)
	}
}

func TestLoadWorkerTimeout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("worker:\n  timeout: 2h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerTimeout != 2*time.Hour {
		t.Fatalf("WorkerTimeout = %s, want 2h", cfg.WorkerTimeout)
	}
}

func TestLoadRejectsInvalidHeartbeatInterval(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("worker:\n  heartbeat_interval: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	if _, err := Load(""); err == nil {
		t.Fatal("expected invalid heartbeat interval error")
	}
}

func TestLoadRejectsUnknownKeyWithSourceAndKey(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte("terminal:\n  adaptr: terminal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWithHome("", home)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "terminal.adaptr") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadValidatesModesDurationsAndExclusiveFolderModes(t *testing.T) {
	tests := []string{
		"terminal:\n  mode: surprise\n",
		"worker:\n  heartbeat_interval: 500ms\n",
		"run_folder:\n  resume: true\n  fresh: true\n",
	}
	for _, content := range tests {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWithHome("", home); err == nil {
			t.Fatalf("expected validation error for:\n%s", content)
		}
	}
}

func TestLoadLegacyEngineEmitsMigrationWarning(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("engine: codex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithHome("", home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != "codex" || len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "engine.default") {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadDefaultFallback(t *testing.T) {
	t.Setenv("PROMPTGRINDER_HOME", filepath.Join(t.TempDir(), "home"))

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TerminalAdapter != "terminal" {
		t.Fatalf("TerminalAdapter = %q, want terminal", cfg.TerminalAdapter)
	}
}

func TestLoadUsesDefaultTemplate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	template := []byte("run_folder:\n  checkpoint: true\n  commit_each: true\n  require_clean_git: true\nworker:\n  timeout: 45m\n")
	if err := os.WriteFile(DefaultTemplatePath(home), template, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RunFolderCheckpoint || !cfg.RunFolderCommitEach || !cfg.RunFolderRequireCleanGit {
		t.Fatalf("run folder defaults = %#v", cfg)
	}
	if cfg.WorkerTimeout != 45*time.Minute {
		t.Fatalf("WorkerTimeout = %s, want 45m", cfg.WorkerTimeout)
	}
}

func TestUserConfigOverridesDefaultTemplate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DefaultTemplatePath(home), []byte("run_folder:\n  checkpoint: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("run_folder:\n  checkpoint: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTGRINDER_HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunFolderCheckpoint {
		t.Fatalf("RunFolderCheckpoint = true, want user config override false")
	}
}

func TestEnsureDefaultTemplateCreatesExampleWhenAccepted(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var out bytes.Buffer

	created, err := EnsureDefaultTemplate(home, strings.NewReader("y\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected template to be created")
	}
	data, err := os.ReadFile(DefaultTemplatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "close_on_finish: true") || !strings.Contains(string(data), "detach: true") || !strings.Contains(out.String(), "Created") {
		t.Fatalf("template/output missing expected content:\n%s\n%s", string(data), out.String())
	}
}
