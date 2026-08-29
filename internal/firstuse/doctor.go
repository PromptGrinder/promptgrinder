package firstuse

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"promptgrinder/internal/buildinfo"
	"promptgrinder/internal/config"
	"promptgrinder/internal/engine/codex"
	"promptgrinder/internal/terminal"

	"gopkg.in/yaml.v3"
)

type Status string

const (
	Pass    Status = "pass"
	Warning Status = "warning"
	Fail    Status = "fail"
	Skipped Status = "skipped"
	Unknown Status = "unknown"
)

type Check struct {
	ID          string         `json:"id"`
	Status      Status         `json:"status"`
	Required    bool           `json:"required"`
	Summary     string         `json:"summary"`
	Evidence    map[string]any `json:"evidence,omitempty"`
	Remediation string         `json:"remediation,omitempty"`
}

type DoctorReport struct {
	OK       bool    `json:"ok"`
	Checks   []Check `json:"checks"`
	Failures int     `json:"failures"`
	Warnings int     `json:"warnings"`
}

type DoctorOptions struct {
	Repo       string
	Terminal   string
	Active     bool
	HomeDir    string
	Executable string
	GOOS       string
	GOARCH     string
	Shell      string
	LookPath   func(string) (string, error)
	Run        func(context.Context, string, ...string) ([]byte, error)
}

func Doctor(ctx context.Context, options DoctorOptions) DoctorReport {
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.Run == nil {
		options.Run = runCommand
	}
	if options.HomeDir == "" {
		options.HomeDir, _ = config.ResolveHomeDir("")
	}
	checks := []Check{
		checkPlatform(options),
		{ID: "promptgrinder.version", Status: Pass, Required: true, Summary: "PromptGrinder version information is available.", Evidence: map[string]any{"version": buildinfo.String()}},
	}

	cfg, configChecks := checkConfig(options)
	checks = append(checks, configChecks...)
	if options.Terminal == "" && cfg.TerminalAdapter != "" {
		options.Terminal = cfg.TerminalAdapter
	}
	if options.Terminal == "" {
		options.Terminal = "terminal"
	}
	checks = append(checks, checkHome(options.HomeDir))

	pgPath := options.Executable
	if pgPath == "" {
		pgPath, _ = os.Executable()
	}
	checks = append(checks, executableCheck("tool.promptgrinder", "PromptGrinder", pgPath, true, "Reinstall PromptGrinder from a verified release artifact."))
	checks = append(checks, checkPromptGrinderPath(options, pgPath))

	codexPath := cfg.CodexExecutable
	codexSource := "PATH"
	if codexPath != "" {
		codexSource = "engine.codex.executable"
		codexPath = resolveConfiguredExecutable(codexPath, options.LookPath)
	} else {
		codexPath, _ = options.LookPath("codex")
	}
	codexCheck := executableCheck("tool.codex", "Codex CLI", codexPath, true, "Install Codex CLI separately, or set engine.codex.executable to its absolute path.")
	if codexPath != "" {
		codexCheck.Evidence["source"] = codexSource
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		version, err := options.Run(versionCtx, codexPath, "--version")
		cancel()
		if err == nil && strings.TrimSpace(string(version)) != "" {
			assessment := codex.AssessVersion(string(version))
			codexCheck.Evidence["version"] = RedactText(strings.TrimSpace(string(version)))
			codexCheck.Evidence["compatibility"] = assessment.Status
			if assessment.Status != codex.VersionQualified {
				codexCheck.Status = Warning
				codexCheck.Required = false
				codexCheck.Summary = "Codex CLI is installed but outside PromptGrinder's qualified compatibility band."
				codexCheck.Remediation = assessment.Reason + ". Update PromptGrinder when available, or explicitly opt in with " + codex.AllowUnqualifiedVersionEnvironment + "=1."
			}
		} else {
			codexCheck.Status = Warning
			codexCheck.Required = false
			codexCheck.Summary = "Codex CLI was found, but its version could not be parsed."
			codexCheck.Remediation = "Run `codex --version` and verify the installed CLI."
		}
	}
	checks = append(checks, codexCheck, checkCodexReadiness(ctx, codexPath, options))
	checks = append(checks, checkOptionalRuntime(ctx, "antigravity", "Antigravity CLI", "agy", cfg.RuntimeOptions["antigravity"], options))

	gitPath, _ := options.LookPath("git")
	checks = append(checks, executableCheck("tool.git", "Git", gitPath, options.Repo != "", "Install Git/Xcode command-line tools before using repository workflows."))
	checks = append(checks, checkRepository(ctx, options.Repo, gitPath, options))
	checks = append(checks, executableCheck("tool.zsh", "Zsh", "/bin/zsh", true, "Restore the macOS system /bin/zsh installation."))

	osascriptPath, _ := options.LookPath("osascript")
	needsOSA := options.Terminal == "terminal" || options.Terminal == "iterm"
	checks = append(checks, executableCheck("tool.osascript", "osascript", osascriptPath, needsOSA, "Use --terminal headless or restore the macOS osascript tool."))
	checks = append(checks, terminalInventory()...)
	checks = append(checks, checkTerminal(options))
	checks = append(checks, checkWorkerPath(options, pgPath, codexPath, gitPath))
	checks = append(checks, checkActive(ctx, options))

	report := DoctorReport{Checks: checks, OK: true}
	for _, check := range checks {
		if check.Status == Warning || check.Status == Unknown {
			report.Warnings++
		}
		if check.Status == Fail && check.Required {
			report.Failures++
			report.OK = false
		}
	}
	return report
}

func checkOptionalRuntime(ctx context.Context, id, name, command string, configured map[string]any, options DoctorOptions) Check {
	executable := ""
	source := "PATH"
	if value, ok := configured["executable"].(string); ok && strings.TrimSpace(value) != "" {
		executable = resolveConfiguredExecutable(value, options.LookPath)
		source = "runtime." + id + ".executable"
	} else {
		executable, _ = options.LookPath(command)
	}
	check := executableCheck("tool."+id, name, executable, false, "Install "+name+" separately or configure runtime."+id+".executable; it is optional unless a worker selects it.")
	if executable == "" || check.Status != Pass {
		return check
	}
	check.Evidence["source"] = source
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := options.Run(versionCtx, executable, "--version")
	if err == nil && strings.TrimSpace(string(version)) != "" {
		check.Evidence["version"] = RedactText(strings.TrimSpace(string(version)))
	}
	return check
}

func checkPromptGrinderPath(o DoctorOptions, executable string) Check {
	resolved, err := o.LookPath("promptgrinder")
	if err == nil && resolved != "" {
		return Check{ID: "shell.promptgrinder_path", Status: Pass, Required: false, Summary: "PromptGrinder is available on PATH.", Evidence: map[string]any{"path": resolved}}
	}
	dir := filepath.Dir(executable)
	if executable == "" || dir == "." {
		dir = "$HOME/.local/bin"
	}
	shell := o.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	profile, remediation := shellPathRemediation(shell, dir)
	evidence := map[string]any{"shell": shell, "directory": dir}
	if profile != "" {
		evidence["profile"] = profile
	}
	return Check{
		ID:          "shell.promptgrinder_path",
		Status:      Warning,
		Required:    false,
		Summary:     "PromptGrinder is running, but new shells cannot find it on PATH.",
		Evidence:    evidence,
		Remediation: remediation,
	}
}

func shellPathRemediation(shell, dir string) (string, string) {
	name := filepath.Base(shell)
	switch name {
	case "zsh":
		line := "export PATH=" + shellSingleQuote(dir) + ":\"$PATH\""
		return "~/.zshrc", "Run `printf '%s\\n' " + shellSingleQuote(line) + " >> ~/.zshrc && source ~/.zshrc`, then rerun `promptgrinder doctor`."
	case "bash":
		line := "export PATH=" + shellSingleQuote(dir) + ":\"$PATH\""
		return "~/.bash_profile", "Run `printf '%s\\n' " + shellSingleQuote(line) + " >> ~/.bash_profile && source ~/.bash_profile`, then rerun `promptgrinder doctor`."
	case "fish":
		return "~/.config/fish/config.fish", "Run `fish_add_path " + shellSingleQuote(dir) + "`, then rerun `promptgrinder doctor`."
	default:
		return "", "Add " + shellSingleQuote(dir) + " to PATH in your shell startup file, start a new shell, then rerun `promptgrinder doctor`."
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func checkPlatform(o DoctorOptions) Check {
	supportedArch := o.GOARCH == "arm64" || o.GOARCH == "amd64"
	if o.GOOS != "darwin" || !supportedArch {
		return Check{ID: "system.platform", Status: Fail, Required: true, Summary: fmt.Sprintf("Unsupported platform %s/%s.", o.GOOS, o.GOARCH), Evidence: map[string]any{"os": o.GOOS, "architecture": o.GOARCH}, Remediation: "Run PromptGrinder on a qualified macOS arm64 or amd64 system."}
	}
	return Check{ID: "system.platform", Status: Pass, Required: true, Summary: "Operating system and architecture are supported.", Evidence: map[string]any{"os": o.GOOS, "architecture": o.GOARCH}}
}

func checkConfig(o DoctorOptions) (config.Config, []Check) {
	sources := configSources(o.HomeDir, o.Repo)
	cfg, err := config.LoadWithHome(o.Repo, o.HomeDir)
	if err != nil {
		evidence := map[string]any{"sources": sources, "error": RedactText(err.Error())}
		if path, parseErr := firstInvalidYAML(o.HomeDir, o.Repo); path != "" {
			evidence["invalid_source"] = path
			evidence["parse_error"] = RedactText(parseErr.Error())
		}
		return config.Config{HomeDir: o.HomeDir}, []Check{{ID: "config.effective", Status: Fail, Required: true, Summary: "Effective configuration is invalid.", Evidence: evidence, Remediation: "Fix the named YAML/configuration source, then run `promptgrinder doctor` again."}}
	}
	status, summary, remediation := validateEffectiveConfig(cfg)
	if len(cfg.Warnings) > 0 && status == Pass {
		status = Warning
		summary = "Effective configuration is valid but uses deprecated settings."
		remediation = strings.Join(cfg.Warnings, "; ")
	}
	return cfg, []Check{{ID: "config.effective", Status: status, Required: true, Summary: summary, Evidence: map[string]any{"sources": sources, "terminal_adapter": cfg.TerminalAdapter, "engine": cfg.Engine}, Remediation: remediation}}
}

func validateEffectiveConfig(cfg config.Config) (Status, string, string) {
	switch cfg.TerminalAdapter {
	case "terminal", "iterm", "headless":
	default:
		return Fail, fmt.Sprintf("Unsupported terminal adapter %q.", cfg.TerminalAdapter), "Set terminal.adapter to terminal, iterm, or headless."
	}
	if cfg.Engine != "codex" {
		return Fail, fmt.Sprintf("Unsupported engine %q.", cfg.Engine), "Set engine.default to codex; PromptGrinder v1 supports no other engine."
	}
	switch cfg.CodexSandbox {
	case "read-only", "workspace-write", "danger-full-access":
	default:
		return Fail, fmt.Sprintf("Unsupported Codex sandbox %q.", cfg.CodexSandbox), "Set engine.codex.sandbox to read-only, workspace-write, or danger-full-access."
	}
	switch cfg.CodexApproval {
	case "untrusted", "on-failure", "on-request", "never":
	default:
		return Fail, fmt.Sprintf("Unsupported Codex approval policy %q.", cfg.CodexApproval), "Set engine.codex.approval to a supported Codex policy."
	}
	return Pass, "Effective configuration is valid.", ""
}

func firstInvalidYAML(home, repo string) (string, error) {
	paths := []string{config.DefaultTemplatePath(home), filepath.Join(home, "config.yaml")}
	if repo != "" {
		paths = append(paths, filepath.Join(repo, ".ai", "config.yaml"))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return path, err
		}
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return path, err
		}
	}
	return "", nil
}

func configSources(home, repo string) []map[string]any {
	paths := []struct {
		kind string
		path string
	}{{"default_template", config.DefaultTemplatePath(home)}, {"user", filepath.Join(home, "config.yaml")}}
	if repo != "" {
		paths = append(paths, struct {
			kind string
			path string
		}{"repository", filepath.Join(repo, ".ai", "config.yaml")})
	}
	out := make([]map[string]any, 0, len(paths)+1)
	for _, source := range paths {
		_, err := os.Stat(source.path)
		out = append(out, map[string]any{"kind": source.kind, "path": source.path, "exists": err == nil})
	}
	out = append(out, map[string]any{"kind": "environment", "prefix": "PROMPTGRINDER_", "keys": safeEnvironmentKeys()})
	return out
}

func checkHome(home string) Check {
	evidence := map[string]any{"path": home}
	info, err := os.Stat(home)
	probe := home
	if os.IsNotExist(err) {
		probe = filepath.Dir(home)
		evidence["exists"] = false
	} else if err != nil {
		return Check{ID: "home.state", Status: Fail, Required: true, Summary: "PromptGrinder home cannot be inspected.", Evidence: evidence, Remediation: "Correct PROMPTGRINDER_HOME ownership or permissions."}
	} else {
		evidence["exists"] = true
		evidence["mode"] = info.Mode().Perm().String()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			evidence["owner_uid"] = stat.Uid
			evidence["current_uid"] = os.Geteuid()
			if int(stat.Uid) != os.Geteuid() {
				return Check{ID: "home.state", Status: Fail, Required: true, Summary: "PromptGrinder home is owned by another user.", Evidence: evidence, Remediation: "Choose a user-owned PROMPTGRINDER_HOME or correct ownership before setup."}
			}
		}
		if info.Mode().Perm()&0o022 != 0 {
			return Check{ID: "home.state", Status: Warning, Required: false, Summary: "PromptGrinder home is writable by group or others.", Evidence: evidence, Remediation: fmt.Sprintf("Run `chmod go-w %q` if these permissions are unintended.", home)}
		}
	}
	if !directoryWritable(probe) {
		return Check{ID: "home.state", Status: Fail, Required: true, Summary: "PromptGrinder home cannot be created or written safely.", Evidence: evidence, Remediation: "Choose a writable PROMPTGRINDER_HOME or correct directory ownership and permissions."}
	}
	var stat syscall.Statfs_t
	if syscall.Statfs(probe, &stat) == nil {
		evidence["free_bytes"] = stat.Bavail * uint64(stat.Bsize)
		if stat.Bavail*uint64(stat.Bsize) < 64*1024*1024 {
			return Check{ID: "home.state", Status: Warning, Required: false, Summary: "PromptGrinder home has very little free space.", Evidence: evidence, Remediation: "Free disk space before creating workers and logs."}
		}
	}
	return Check{ID: "home.state", Status: Pass, Required: true, Summary: "PromptGrinder home is usable.", Evidence: evidence}
}

func directoryWritable(path string) bool {
	for path != "" {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return false
			}
			return syscall.Access(path, 0x2) == nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
	return false
}

func executableCheck(id, name, path string, required bool, remediation string) Check {
	if path == "" {
		status := Warning
		if required {
			status = Fail
		}
		return Check{ID: id, Status: status, Required: required, Summary: name + " was not found.", Remediation: remediation}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		status := Warning
		if required {
			status = Fail
		}
		return Check{ID: id, Status: status, Required: required, Summary: name + " is not executable.", Evidence: map[string]any{"path": path}, Remediation: remediation}
	}
	resolved, _ := filepath.EvalSymlinks(path)
	if resolved == "" {
		resolved = path
	}
	return Check{ID: id, Status: Pass, Required: required, Summary: name + " is executable.", Evidence: map[string]any{"path": path, "resolved_path": resolved}}
}

func checkCodexReadiness(ctx context.Context, path string, o DoctorOptions) Check {
	if path == "" {
		return Check{ID: "codex.readiness", Status: Skipped, Required: false, Summary: "Codex readiness was skipped because Codex was not found.", Remediation: "Resolve tool.codex first."}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := o.Run(probeCtx, path, "login", "status")
	text := RedactText(strings.TrimSpace(string(output)))
	if err == nil {
		return Check{ID: "codex.readiness", Status: Pass, Required: false, Summary: "Codex reports an authenticated login.", Evidence: map[string]any{"probe": "codex login status", "output": text}}
	}
	if text == "" {
		text = "probe returned no safe output"
	}
	return Check{ID: "codex.readiness", Status: Fail, Required: true, Summary: "Codex authentication/readiness could not be verified safely.", Evidence: map[string]any{"probe": "codex login status", "output": text}, Remediation: "Run `codex login status`; authenticate with `codex login` if needed, then rerun doctor."}
}

func checkRepository(ctx context.Context, repo, gitPath string, o DoctorOptions) Check {
	if repo == "" {
		return Check{ID: "repo.git", Status: Skipped, Required: false, Summary: "Repository check was not requested.", Remediation: "Pass --repo when checking a repository workflow."}
	}
	absolute, err := filepath.Abs(repo)
	if err != nil {
		absolute = repo
	}
	if gitPath == "" {
		return Check{ID: "repo.git", Status: Fail, Required: true, Summary: "Repository cannot be checked because Git is missing.", Evidence: map[string]any{"path": absolute}, Remediation: "Resolve tool.git first."}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := o.Run(probeCtx, gitPath, "-C", absolute, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return Check{ID: "repo.git", Status: Fail, Required: true, Summary: "The supplied path is not a usable Git repository.", Evidence: map[string]any{"path": absolute, "error": RedactText(strings.TrimSpace(string(out)))}, Remediation: "Pass the repository root with --repo, or initialize Git before repository workflows."}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	changes := 0
	if len(lines) > 1 {
		changes = len(lines) - 1
	}
	return Check{ID: "repo.git", Status: Pass, Required: true, Summary: "Git repository is usable.", Evidence: map[string]any{"path": absolute, "worktree_changes": changes}}
}

func checkTerminal(o DoctorOptions) Check {
	switch o.Terminal {
	case "headless":
		return Check{ID: "terminal.adapter", Status: Pass, Required: true, Summary: "Headless adapter requires no GUI application.", Evidence: map[string]any{"adapter": "headless"}}
	case "terminal":
		path := "/System/Applications/Utilities/Terminal.app"
		if _, err := os.Stat(path); err != nil {
			path = "/Applications/Utilities/Terminal.app"
		}
		return applicationCheck("Terminal.app", path, "terminal")
	case "iterm":
		path := "/Applications/iTerm.app"
		if _, err := os.Stat(path); err != nil {
			path = "/Applications/iTerm2.app"
		}
		check := applicationCheck("iTerm2", path, "iterm")
		if check.Evidence == nil {
			check.Evidence = map[string]any{}
		}
		check.Evidence["bundle_identifier"] = "com.googlecode.iterm2"
		if check.Status == Fail {
			check.Remediation = "Install iTerm2 (bundle identifier com.googlecode.iterm2), or select --terminal headless; setup never installs terminal applications."
		}
		return check
	default:
		return Check{ID: "terminal.adapter", Status: Fail, Required: true, Summary: fmt.Sprintf("Unsupported terminal adapter %q.", o.Terminal), Remediation: "Use --terminal terminal, --terminal iterm, or --terminal headless."}
	}
}

func terminalInventory() []Check {
	return []Check{
		{ID: "terminal.available.headless", Status: Pass, Required: false, Summary: "Headless execution is available.", Evidence: map[string]any{"adapter": "headless"}},
		applicationInventoryCheck("terminal.available.terminal", "Terminal.app", []string{"/System/Applications/Utilities/Terminal.app", "/Applications/Utilities/Terminal.app"}),
		applicationInventoryCheck("terminal.available.iterm", "iTerm2", []string{"/Applications/iTerm.app", "/Applications/iTerm2.app"}),
	}
}

func applicationInventoryCheck(id, name string, candidates []string) Check {
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return Check{ID: id, Status: Pass, Required: false, Summary: name + " is installed.", Evidence: map[string]any{"path": candidate}}
		}
	}
	return Check{ID: id, Status: Warning, Required: false, Summary: name + " was not found at a standard location.", Evidence: map[string]any{"searched_paths": candidates}, Remediation: "Install " + name + " only if you want to use its terminal adapter; headless execution remains available."}
}

func applicationCheck(name, path, adapter string) Check {
	if _, err := os.Stat(path); err != nil {
		return Check{ID: "terminal.adapter", Status: Fail, Required: true, Summary: name + " is not installed at its standard location.", Evidence: map[string]any{"adapter": adapter, "path": path}, Remediation: "Choose an installed adapter; setup never installs terminal applications."}
	}
	return Check{ID: "terminal.adapter", Status: Pass, Required: true, Summary: name + " is available.", Evidence: map[string]any{"adapter": adapter, "path": path}}
}

func checkWorkerPath(o DoctorOptions, promptgrinder, codex, git string) Check {
	required := map[string]string{"promptgrinder": promptgrinder, "codex": codex, "git": git, "zsh": "/bin/zsh"}
	if o.Terminal != "headless" {
		osa, _ := o.LookPath("osascript")
		required["osascript"] = osa
	}
	var missing []string
	evidence := map[string]any{}
	pathValue := os.Getenv("PATH")
	for name, path := range required {
		found := path != "" && (filepath.IsAbs(path) || pathOnPATH(path, pathValue))
		evidence[name] = map[string]any{"path": path, "discoverable": found}
		if !found {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		evidence["missing"] = missing
		return Check{ID: "worker.environment", Status: Fail, Required: true, Summary: "Generated workers cannot discover every required executable.", Evidence: evidence, Remediation: "Use absolute configured executable paths or launch PromptGrinder with a PATH containing the missing tools."}
	}
	return Check{ID: "worker.environment", Status: Pass, Required: true, Summary: "Generated worker environment can resolve required executables.", Evidence: evidence}
}

func checkActive(ctx context.Context, o DoctorOptions) Check {
	if !o.Active {
		return Check{ID: "terminal.active_launch", Status: Skipped, Required: false, Summary: "Visible no-AI terminal launch probe was not requested.", Remediation: "Rerun with --active to open a short-lived terminal probe."}
	}
	adapter, err := terminal.SelectAdapter(o.Terminal, "normal")
	if err != nil {
		return Check{ID: "terminal.active_launch", Status: Fail, Required: true, Summary: "Active terminal adapter could not be selected.", Remediation: err.Error()}
	}
	dir, err := os.MkdirTemp("", "promptgrinder-doctor-")
	if err != nil {
		return Check{ID: "terminal.active_launch", Status: Fail, Required: true, Summary: "Could not prepare active terminal probe.", Remediation: RedactText(err.Error())}
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "probe.zsh")
	if err := os.WriteFile(script, []byte("#!/bin/zsh\necho 'PromptGrinder active terminal check passed (no AI session opened).'\n"), 0o700); err != nil {
		return Check{ID: "terminal.active_launch", Status: Fail, Required: true, Summary: "Could not prepare active terminal probe.", Remediation: RedactText(err.Error())}
	}
	_ = ctx
	if err := adapter.Launch(script); err != nil {
		return Check{ID: "terminal.active_launch", Status: Fail, Required: true, Summary: "Visible no-AI terminal launch probe failed.", Evidence: map[string]any{"adapter": o.Terminal, "error": RedactText(err.Error())}, Remediation: "Review terminal Automation permissions and adapter prerequisites, then retry --active."}
	}
	return Check{ID: "terminal.active_launch", Status: Pass, Required: true, Summary: "Visible no-AI terminal launch probe started.", Evidence: map[string]any{"adapter": o.Terminal, "side_effect": "opened a terminal window or ran a headless zsh process"}}
}

func resolveConfiguredExecutable(value string, lookPath func(string) (string, error)) string {
	if filepath.IsAbs(value) {
		return value
	}
	path, _ := lookPath(value)
	return path
}

func pathOnPATH(path, pathValue string) bool {
	if filepath.IsAbs(path) {
		return true
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if filepath.Join(dir, path) == path {
			return true
		}
	}
	return false
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func safeEnvironmentKeys() []string {
	var keys []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "PROMPTGRINDER_") && !IsSecretKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func IsSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "PASS", "API_KEY", "PRIVATE", "CREDENTIAL", "AUTH", "COOKIE"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

func RedactText(value string) string {
	if value == "" {
		return value
	}
	var out bytes.Buffer
	for _, field := range strings.Fields(value) {
		upper := strings.ToUpper(field)
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "KEY=") ||
			strings.Contains(upper, "SECRET=") || strings.HasPrefix(field, "sk-") ||
			strings.Contains(upper, "AUTH.JSON") || strings.Contains(upper, "CREDENTIALS.JSON") {
			out.WriteString("[redacted]")
		} else {
			out.WriteString(field)
		}
		out.WriteByte(' ')
	}
	return strings.TrimSpace(out.String())
}
