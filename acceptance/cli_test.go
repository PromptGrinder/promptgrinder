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
	"regexp"
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

func TestCompiledCLIRolesEnhanceOfflineReviewAndApprovalModes(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"review", nil},
		{"reject-all", []string{"--reject-all"}},
		{"apply-selected", []string{"--apply-selected", "description"}},
		{"apply-all", []string{"--apply-all"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spring boot project")
			if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			secret := "sk_fixture_value_that_must_not_escape"
			if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project><parent><artifactId>spring-boot-starter-parent</artifactId></parent></project>\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Backend owns Spring Boot APIs.\nAPI_KEY="+secret+"\nRun ./mvnw test.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "advisor request")
			response := `{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"gate","role_id":"backend-feature","operation":"append","field":"quality_gates","value":"./mvnw test","confidence":"high","explanation":"documented test command","evidence":[{"path":"README.md"}]},{"id":"description","role_id":"backend-feature","operation":"set","field":"description","value":"Own Spring Boot APIs","confidence":"high","explanation":"documented ownership","evidence":[{"path":"README.md"}]}]}`
			script := `#!/bin/sh
set -eu
output=
previous=
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then output=$argument; fi
  previous=$argument
done
/bin/cat > "$PG_ROLE_RECORD"
printf '%s' "$PG_ROLE_RESPONSE" > "$output"
`
			fake := testsupport.FakeExecutable(t, "codex", script)
			options := commandOptions{cwd: root, codex: fake, env: []string{"PG_ROLE_RECORD=" + record, "PG_ROLE_RESPONSE=" + response}}
			discovered := runCLI(t, options, "discover")
			if discovered.exitCode != 0 {
				t.Fatalf("discover = %#v", discovered)
			}
			rolePath := filepath.Join(root, ".promptgrinder", "roles", "backend-feature.yaml")
			role, err := os.ReadFile(rolePath)
			if err != nil {
				t.Fatal(err)
			}
			role = append(role, []byte("custom: keep-me\n")...)
			if err := os.WriteFile(rolePath, role, 0o644); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), role...)
			args := append([]string{"roles", "enhance"}, test.args...)
			result := runCLI(t, options, args...)
			if result.exitCode != 0 || result.stderr != "" {
				t.Fatalf("enhance = %#v", result)
			}
			request, err := os.ReadFile(record)
			if err != nil || strings.Contains(string(request), secret) {
				t.Fatalf("unsafe recorded request: %v %s", err, request)
			}
			after, err := os.ReadFile(rolePath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.stdout, "Old:") || !strings.Contains(result.stdout, "Confidence: high") || !strings.Contains(result.stdout, "README.md") || strings.Contains(result.stdout, secret) {
				t.Fatalf("incomplete or unsafe review:\n%s", result.stdout)
			}
			if strings.Contains(string(after), secret) {
				t.Fatalf("secret fixture reached generated role:\n%s", after)
			}
			switch test.name {
			case "review", "reject-all":
				if !bytes.Equal(before, after) {
					t.Fatal("non-apply mode wrote role")
				}
				if test.name == "review" {
					repeated := runCLI(t, options, "roles", "enhance")
					if repeated.exitCode != 0 || repeated.stdout != result.stdout {
						t.Fatalf("review output is unstable: first=%#v second=%#v", result, repeated)
					}
				}
			case "apply-selected":
				if !strings.Contains(string(after), "description: Own Spring Boot APIs") || strings.Contains(string(after), "./mvnw test") {
					t.Fatalf("selected application changed wrong fields:\n%s", after)
				}
			case "apply-all":
				if !strings.Contains(string(after), "description: Own Spring Boot APIs") || !strings.Contains(string(after), "./mvnw test") || !strings.Contains(string(after), "custom: keep-me") {
					t.Fatalf("apply-all lost changes:\n%s", after)
				}
			}
		})
	}
}

func TestCompiledCLIPersistedRoleReviewFollowupsDoNotInvokeAdvisor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "persisted review repository")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project><parent><artifactId>spring-boot-starter-parent</artifactId></parent></project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Run ./mvnw test before merging.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "promptgrinder home")
	count := filepath.Join(t.TempDir(), "advisor count")
	response := `{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"gate","role_id":"backend-feature","operation":"append","field":"quality_gates","value":"./mvnw test","confidence":"high","explanation":"documented test command","evidence":[{"path":"README.md"}]}]}`
	script := `#!/bin/sh
set -eu
printf x >> "$PG_ADVISOR_COUNT"
output=
previous=
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then output=$argument; fi
  previous=$argument
done
/bin/cat >/dev/null
printf '%s' "$PG_ROLE_RESPONSE" > "$output"
`
	fake := testsupport.FakeExecutable(t, "codex", script)
	options := commandOptions{home: home, cwd: root, codex: fake, env: []string{"PG_ADVISOR_COUNT=" + count, "PG_ROLE_RESPONSE=" + response}}
	if result := runCLI(t, options, "discover"); result.exitCode != 0 {
		t.Fatalf("discover = %#v", result)
	}
	enhanced := runCLI(t, options, "roles", "enhance")
	if enhanced.exitCode != 0 {
		t.Fatalf("enhance = %#v", enhanced)
	}
	match := regexp.MustCompile(`Review: (rev_[a-f0-9]+)`).FindStringSubmatch(enhanced.stdout)
	if len(match) != 2 {
		t.Fatalf("missing review ID:\n%s", enhanced.stdout)
	}
	if calls, err := os.ReadFile(count); err != nil || string(calls) != "x" {
		t.Fatalf("advisor calls after enhance = %q, %v", calls, err)
	}
	failing, marker := markedFailingCodex(t)
	options.codex = failing
	if result := runCLI(t, options, "roles", "refine", match[1]); result.exitCode != 0 {
		t.Fatalf("refine with failing advisor = %#v", result)
	}
	if result := runCLI(t, options, "roles", "apply", match[1], "--safe"); result.exitCode != 0 {
		t.Fatalf("apply with failing advisor = %#v", result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stored review follow-up invoked advisor: %v", err)
	}
	role, err := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend-feature.yaml"))
	if err != nil || !bytes.Contains(role, []byte("./mvnw test")) {
		t.Fatalf("safe stored application missing: %v\n%s", err, role)
	}
}

type commandOptions struct {
	home  string
	codex string
	cwd   string
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
	command.Dir = options.cwd
	if command.Dir == "" {
		command.Dir = repoRoot
	}
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
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.150.1'; exit 0; fi\nprintf invoked > " + shellQuote(marker) + "\nexit 99\n"
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
if [ "${1:-}" = "--version" ]; then
  echo "codex-cli 0.150.1"
  exit 0
fi
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
