package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/state"
	"promptgrinder/internal/testsupport"
)

const (
	exitInvalidInput    = 2
	exitExecutionFailed = 5
)

var (
	compiledCLI string
	repoRoot    string
	buildDir    string
)

func TestMain(m *testing.M) {
	var err error
	_, source, _, _ := runtime.Caller(0)
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(source), ".."))
	buildDir, err = os.MkdirTemp("", "promptgrinder acceptance [build];")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	compiledCLI = filepath.Join(buildDir, "promptgrinder [compiled];")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", compiledCLI, "./cmd/promptgrinder")
	command.Dir = repoRoot
	output, buildErr := command.CombinedOutput()
	cancel()
	if buildErr != nil {
		fmt.Fprintf(os.Stderr, "build compiled acceptance binary: %v\n%s", buildErr, output)
		_ = os.RemoveAll(buildDir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(buildDir)
	os.Exit(code)
}

func TestCompiledCLIHelpVersionAndHumanStreams(t *testing.T) {
	help := runCLI(t, commandOptions{}, "--help")
	if help.exitCode != 0 || !strings.Contains(help.stdout, "promptgrinder") || help.stderr != "" {
		t.Fatalf("--help result = %#v", help)
	}

	version := runCLI(t, commandOptions{}, "--version")
	if version.exitCode != 0 || strings.TrimSpace(version.stdout) == "" ||
		!strings.Contains(version.stdout, "promptgrinder") || version.stderr != "" {
		t.Fatalf("--version result = %#v", version)
	}

	task := writeTask(t, "invalid [metadata];.md", `---
working_directory: 42
---
invalid
`)
	invalid := runCLI(t, commandOptions{codex: nonExecutingCodex(t)}, "validate", task)
	if invalid.exitCode != exitInvalidInput || !strings.Contains(invalid.stdout, "Valid: false") || invalid.stderr != "" {
		t.Fatalf("human invalid result = %#v", invalid)
	}
}

func TestCompiledCLIValidateAndDryRunDoNotExecuteCodexOrCreateState(t *testing.T) {
	stub, marker := markedFailingCodex(t)
	home := filepath.Join(t.TempDir(), "isolated home [dry-run];")
	validate := runCLI(t, commandOptions{home: home, codex: stub}, "validate", filepath.Join(repoRoot, "examples", "minimal.md"))
	if validate.exitCode != 0 || !strings.Contains(validate.stdout, "Valid: true") || validate.stderr != "" {
		t.Fatalf("validate result = %#v", validate)
	}
	dryRun := runCLI(t, commandOptions{home: home, codex: stub}, "run", "--dry-run", filepath.Join(repoRoot, "examples", "smoke", "read-only.md"))
	if dryRun.exitCode != 0 || !strings.Contains(dryRun.stdout, "Dry run valid: 1 prompt") || dryRun.stderr != "" {
		t.Fatalf("dry-run result = %#v", dryRun)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run invoked Codex stub, marker error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "workers")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created execution state, workers error = %v", err)
	}
}

func TestCompiledCLIJSONContractsAndUnknownEngineExitCode(t *testing.T) {
	stub := nonExecutingCodex(t)
	valid := runCLI(t, commandOptions{codex: stub}, "validate", filepath.Join(repoRoot, "examples", "minimal.md"), "--json")
	var plan struct {
		Valid         bool           `json:"valid"`
		Engine        string         `json:"engine"`
		ExecutionPlan map[string]any `json:"execution_plan"`
	}
	if valid.exitCode != 0 || json.Unmarshal([]byte(valid.stdout), &plan) != nil ||
		!plan.Valid || plan.Engine != "codex" || plan.ExecutionPlan == nil || valid.stderr != "" {
		t.Fatalf("valid JSON result = %#v, plan = %#v", valid, plan)
	}

	invalidTask := writeTask(t, "broken [frontmatter];.md", "---\nengine: [\n---\nbody\n")
	invalid := runCLI(t, commandOptions{codex: stub}, "validate", invalidTask, "--json")
	var invalidPlan map[string]any
	if invalid.exitCode != exitInvalidInput || json.Unmarshal([]byte(invalid.stdout), &invalidPlan) != nil || invalid.stderr != "" {
		t.Fatalf("invalid JSON result = %#v", invalid)
	}
	if validValue, _ := invalidPlan["valid"].(bool); validValue {
		t.Fatalf("invalid plan claims valid: %#v", invalidPlan)
	}

	unknown := runCLI(t, commandOptions{codex: stub}, "run", "--dry-run", "--engine", "missing", filepath.Join(repoRoot, "examples", "minimal.md"), "--json")
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if unknown.exitCode != exitInvalidInput || json.Unmarshal([]byte(unknown.stdout), &envelope) != nil ||
		envelope.Error.Code != "validation_error" || !strings.Contains(envelope.Error.Message, "unknown engine") ||
		unknown.stderr != "" {
		t.Fatalf("unknown engine result = %#v, envelope = %#v", unknown, envelope)
	}
}

func TestCompiledCLIRecordingCodexSuccess(t *testing.T) {
	fixture := recordingFixture(t, 0)
	result := runCLI(t, fixture.options, "run", fixture.task)
	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("run result = %#v", result)
	}
	assertRecording(t, fixture)
	worker := onlyWorker(t, fixture.options.home)
	if worker.Status != state.StatusSucceeded || worker.ExitCode == nil || *worker.ExitCode != 0 ||
		worker.EngineResult == nil || worker.EngineResult.SessionID != "acceptance-session" {
		t.Fatalf("persisted success worker = %#v", worker)
	}
	if strings.Contains(result.stdout, fixture.secret) || strings.Contains(result.stderr, fixture.secret) {
		t.Fatal("secret-looking fixture value leaked to user-facing output")
	}
}

func TestCompiledCLIRecordingCodexFailure(t *testing.T) {
	fixture := recordingFixture(t, 17)
	result := runCLI(t, fixture.options, "run", fixture.task)
	if result.exitCode != exitExecutionFailed || !strings.Contains(result.stderr, "headless execution failed") {
		t.Fatalf("failure result = %#v", result)
	}
	assertRecording(t, fixture)
	worker := onlyWorker(t, fixture.options.home)
	if worker.Status != state.StatusFailed || worker.ExitCode == nil || *worker.ExitCode != 17 {
		t.Fatalf("persisted failure worker = %#v", worker)
	}
	if strings.Contains(result.stdout, fixture.secret) || strings.Contains(result.stderr, fixture.secret) {
		t.Fatal("secret-looking fixture value leaked to user-facing output")
	}
}

type commandOptions struct {
	home  string
	codex string
	env   []string
}

type commandResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCLI(t *testing.T, options commandOptions, args ...string) commandResult {
	t.Helper()
	home := options.home
	if home == "" {
		home = filepath.Join(t.TempDir(), "isolated home [cli];")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, compiledCLI, args...)
	command.Dir = repoRoot
	pathValue := os.Getenv("PATH")
	if options.codex != "" {
		pathValue = filepath.Dir(options.codex) + string(os.PathListSeparator) + pathValue
	}
	command.Env = append(filteredEnvironment(os.Environ()),
		"PATH="+pathValue,
		"PROMPTGRINDER_HOME="+home,
		"PROMPTGRINDER_TERMINAL_ADAPTER=headless",
		"PROMPTGRINDER_PLAIN=1",
	)
	command.Env = append(command.Env, options.env...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("compiled CLI timed out: %v\nstdout:\n%s\nstderr:\n%s", ctx.Err(), stdout.String(), stderr.String())
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run compiled CLI: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func filteredEnvironment(input []string) []string {
	result := make([]string, 0, len(input))
	for _, entry := range input {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "PROMPTGRINDER_HOME", "PROMPTGRINDER_TERMINAL_ADAPTER", "PROMPTGRINDER_PLAIN",
			"PROMPTGRINDER_HEADLESS", "PROMPTGRINDER_ENGINE_CODEX_EXECUTABLE":
			continue
		default:
			result = append(result, entry)
		}
	}
	return result
}

func nonExecutingCodex(t *testing.T) string {
	t.Helper()
	return testsupport.FakeCodex(t)
}

func markedFailingCodex(t *testing.T) (string, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "unexpected invocation")
	script := "#!/bin/sh\nprintf invoked > " + shellQuote(marker) + "\nexit 99\n"
	return testsupport.FakeExecutable(t, "codex", script), marker
}

type recordingData struct {
	options     commandOptions
	task        string
	recordDir   string
	secret      string
	expectedDir string
}

func recordingFixture(t *testing.T, exitCode int) recordingData {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository acceptance;$data")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(t.TempDir(), "recordings codex;$data")
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "fixture-secret-looking-value"
	exitText := strconv.Itoa(exitCode)
	task := filepath.Join(root, "task spaces;$literal.md")
	recordYAML, _ := json.Marshal(recordDir)
	secretYAML, _ := json.Marshal(secret)
	exitYAML, _ := json.Marshal(exitText)
	content := fmt.Sprintf(`---
engine:
  name: codex
  sandbox: workspace-write
working_directory: .
env:
  PG_ACCEPT_RECORD: %s
  PG_ACCEPT_SECRET: %s
  PG_ACCEPT_EXIT_CODE: %s
---

# Acceptance boundary

Keep literal shell text unchanged: $HOME $(touch forbidden) ; [brackets] *.
`, recordYAML, secretYAML, exitYAML)
	if err := os.WriteFile(task, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
: "${PG_ACCEPT_RECORD:?}"
/bin/pwd > "$PG_ACCEPT_RECORD/cwd"
/usr/bin/env | /usr/bin/sort > "$PG_ACCEPT_RECORD/env"
printf '%s\n' "$@" > "$PG_ACCEPT_RECORD/args"
count=0
for argument in "$@"; do
  count=$((count + 1))
  last=$argument
done
printf '%s' "$last" > "$PG_ACCEPT_RECORD/prompt"
printf '%s' "$count" > "$PG_ACCEPT_RECORD/argc"
if [ "${PG_ACCEPT_EXIT_CODE:-0}" -ne 0 ]; then
  echo "controlled fake Codex failure" >&2
  exit "$PG_ACCEPT_EXIT_CODE"
fi
printf '%s\n' \
  '{"type":"thread.started","thread_id":"acceptance-session"}' \
  '{"type":"item.completed","item":{"type":"agent_message","text":"STATUS: PASS\nSUMMARY:\n- Fake Codex completed.\nNEXT_PROMPT_SAFE: yes"}}'
`
	fake := testsupport.FakeExecutable(t, "codex", script)
	home := filepath.Join(t.TempDir(), "isolated home run;$data")
	return recordingData{
		options: commandOptions{
			home: home, codex: fake,
			env: []string{"UNINTENDED_SECRET=must-not-cross-boundary"},
		},
		task: task, recordDir: recordDir, secret: secret, expectedDir: root,
	}
}

func assertRecording(t *testing.T, fixture recordingData) {
	t.Helper()
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(fixture.recordDir, name))
		if err != nil {
			t.Fatalf("read recording %s: %v", name, err)
		}
		return string(data)
	}
	gotDirectory := strings.TrimSpace(read("cwd"))
	gotInfo, gotErr := os.Stat(gotDirectory)
	wantInfo, wantErr := os.Stat(fixture.expectedDir)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("Codex cwd = %q, want %q (stat errors: %v, %v)", gotDirectory, fixture.expectedDir, gotErr, wantErr)
	}
	prompt := read("prompt")
	for _, literal := range []string{"# Acceptance boundary", "$HOME", "$(touch forbidden)", "; [brackets] *"} {
		if !strings.Contains(prompt, literal) {
			t.Fatalf("recorded prompt lost %q:\n%s", literal, prompt)
		}
	}
	argsText := read("args")
	for _, expected := range []string{"exec\n", "--cd\n" + fixture.expectedDir + "\n", "--sandbox\nworkspace-write\n", "--json\n"} {
		if !strings.Contains(argsText, expected) {
			t.Fatalf("recorded args missing %q:\n%s", expected, argsText)
		}
	}
	envLines := strings.Split(strings.TrimSpace(read("env")), "\n")
	sort.Strings(envLines)
	envText := strings.Join(envLines, "\n")
	for _, expected := range []string{"PG_ACCEPT_RECORD=", "PG_ACCEPT_SECRET=" + fixture.secret, "PG_ACCEPT_EXIT_CODE="} {
		if !strings.Contains(envText, expected) {
			t.Fatalf("recorded env missing %q:\n%s", expected, envText)
		}
	}
	for _, forbidden := range []string{"UNINTENDED_SECRET=", "PROMPTGRINDER_HOME=", "PATH="} {
		if strings.Contains(envText, forbidden) {
			t.Fatalf("recorded env contains unintended %q:\n%s", forbidden, envText)
		}
	}
}

func onlyWorker(t *testing.T, home string) state.Worker {
	t.Helper()
	workers, err := state.NewStore(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %#v", workers)
	}
	return workers[0]
}

func writeTask(t *testing.T, name, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository [fixtures];$data")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
