package codex

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"promptgrinder/internal/engine"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/state"
)

var _ engine.Engine = Engine{}

func FuzzZshQuoteRoundTrip(f *testing.F) {
	for _, seed := range []string{"plain", "space and ünicode", "quote'$(touch nope)*?[x]", "line1\nline2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if strings.ContainsRune(value, 0) {
			t.Skip()
		}
		cmd := exec.Command("/bin/zsh", "-c", "printf %s "+zshQuote(value))
		var output bytes.Buffer
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			t.Fatalf("quoted value failed zsh parsing: %v", err)
		}
		if output.String() != value {
			t.Fatalf("round trip = %q, want %q", output.String(), value)
		}
	})
}

func TestBuildScriptUsesInternalCodexExecutor(t *testing.T) {
	script := Engine{}.BuildScript(state.Worker{
		ID:             "wrk_test",
		PromptPath:     "/tmp/path with spaces/prompt.md",
		TaskPath:       "/tmp/path with spaces/task.md",
		RepositoryPath: "/tmp/path with spaces",
		LogPath:        "/tmp/path with spaces/worker.log",
		RecordPath:     "/tmp/path with spaces/state/workers/wrk_test/worker.json",
	}, "/tmp/path with spaces/promptgrinder")

	if !strings.Contains(script, `__engine-codex "$RECORD_PATH" "$CODEX_BIN"`) {
		t.Fatalf("script does not use internal codex executor:\n%s", script)
	}
	if !strings.Contains(script, "PROMPTGRINDER_HOME='/tmp/path with spaces/state'") {
		t.Fatalf("script does not preserve custom PromptGrinder home:\n%s", script)
	}
	if !strings.Contains(script, `PROMPT_NAME="${TASK_PATH:t}"`) || !strings.Contains(script, `TITLE="PromptGrinder: $WORKER_ID"`) {
		t.Fatalf("script does not set collision-resistant worker terminal title:\n%s", script)
	}
	if !strings.Contains(script, "export PROMPTGRINDER_HOME") {
		t.Fatalf("script does not export PromptGrinder home:\n%s", script)
	}
	if !strings.Contains(script, `__worker-heartbeat "$WORKER_ID"`) {
		t.Fatalf("script does not include heartbeat calls:\n%s", script)
	}
	if !strings.Contains(script, "heartbeat_loop &") {
		t.Fatalf("script does not include heartbeat loop:\n%s", script)
	}
	if !strings.Contains(script, `if [ "${PROMPTGRINDER_HEADLESS:-}" != "1" ]; then`) {
		t.Fatalf("script does not suppress the heartbeat sleep loop in headless execution:\n%s", script)
	}
	if !strings.Contains(script, "kill \"$HEARTBEAT_PID\"") {
		t.Fatalf("script does not stop heartbeat loop:\n%s", script)
	}
	if !strings.Contains(script, "Warning: worker heartbeat failed.") {
		t.Fatalf("script does not warn on heartbeat failure:\n%s", script)
	}
	if strings.Contains(script, "$(< \"$PROMPT_PATH\")") {
		t.Fatalf("script still uses shell command substitution:\n%s", script)
	}
	if !strings.Contains(script, `exec "$KEEP_OPEN_SHELL" -l`) {
		t.Fatalf("script does not leave terminal open:\n%s", script)
	}
	if strings.Contains(script, `exec /bin/zsh "$`) || strings.Contains(script, `exec "$KEEP_OPEN_SHELL" "$`) {
		t.Fatalf("script appears to pass the worker script to the keep-open shell:\n%s", script)
	}
}

func TestBuildScriptPrintsCommandPreviewLiterally(t *testing.T) {
	script := Engine{}.BuildScript(state.Worker{
		ID:             "wrk_test",
		PromptPath:     "/tmp/prompt.md",
		TaskPath:       "/repo/tasks/task.md",
		RepositoryPath: "/repo",
		LogPath:        "/tmp/worker.log",
		RecordPath:     "/tmp/worker.json",
		Metadata: map[string]any{
			"engine": map[string]any{
				"model": "gpt-$(touch /tmp/owned)",
			},
			"images": []any{"$(touch /tmp/image-owned).png"},
		},
	}, "/tmp/promptgrinder")

	if strings.Contains(script, "echo \"Command:") {
		t.Fatalf("script prints command preview through double-quoted echo:\n%s", script)
	}
	if !strings.Contains(script, "COMMAND_PREVIEW='") {
		t.Fatalf("script does not assign quoted command preview:\n%s", script)
	}
	if !strings.Contains(script, "printf 'Command: %s\\n' \"$COMMAND_PREVIEW\"") {
		t.Fatalf("script does not print preview via printf data argument:\n%s", script)
	}
	if !strings.Contains(script, "print_launch_header") || !strings.Contains(script, "PromptGrinder dev") {
		t.Fatalf("script does not include launch header:\n%s", script)
	}
	if !strings.Contains(script, "NO_COLOR") || !strings.Contains(script, "PROMPTGRINDER_PLAIN") {
		t.Fatalf("script does not support plain launch header mode:\n%s", script)
	}
}

func TestWorkerEnvironmentUsesAllowlistAndExplicitMetadata(t *testing.T) {
	got := workerEnvironment(map[string]any{
		"env": map[string]any{"TASK_VALUE": "explicit", "LANG": "task-locale"},
	}, []string{
		"HOME=/Users/test",
		"PATH=/host/custom/bin",
		"OPENAI_API_KEY=must-not-copy",
		"LANG=parent-locale",
		"LC_ALL=en_US.UTF-8",
	})
	joined := strings.Join(got, "\n")
	for _, expected := range []string{"HOME=/Users/test", "LANG=task-locale", "LC_ALL=en_US.UTF-8", "TASK_VALUE=explicit"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment missing %q: %v", expected, got)
		}
	}
	for _, excluded := range []string{"PATH=", "OPENAI_API_KEY"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("environment copied %q: %v", excluded, got)
		}
	}
}

func TestBuildScriptClosesSuccessfulTerminalAndKeepsFailuresByDefault(t *testing.T) {
	script := Engine{}.BuildScript(state.Worker{
		ID:             "wrk_close",
		PromptPath:     "/tmp/prompt.md",
		TaskPath:       "/repo/task.md",
		RepositoryPath: "/repo",
		LogPath:        "/tmp/worker.log",
		RecordPath:     "/tmp/worker.json",
		CloseOnFinish:  true,
	}, "/tmp/promptgrinder")

	if !strings.Contains(script, "CLOSE_ON_FINISH='1'") || !strings.Contains(script, "CLOSE_ON_FAILURE='0'") {
		t.Fatalf("close policy missing from script:\n%s", script)
	}
	if !strings.Contains(script, `__terminal-close "$WORKER_ID"`) {
		t.Fatalf("terminal close command missing from script:\n%s", script)
	}
}

func TestBuildPropagatesClosePolicyIntoScript(t *testing.T) {
	request, err := (Engine{}).Build(execution.Context{
		RepositoryRoot: "/repo",
		TaskPath:       "/repo/task.md",
		WorkerID:       "wrk_close",
		Metadata:       map[string]any{},
		CloseOnFinish:  true,
		Directories: execution.Directories{
			Worker: "/tmp/worker",
			Prompt: "/tmp/worker/prompt.md",
			Log:    "/tmp/worker/worker.log",
		},
	}, []byte("task"), "/tmp/promptgrinder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Script, "CLOSE_ON_FINISH='1'") {
		t.Fatalf("close policy was not propagated:\n%s", request.Script)
	}
}

func TestBuildCommandDefaults(t *testing.T) {
	spec, err := BuildCommand(testCommandWorker(map[string]any{}), "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--cd", "/repo", "--sandbox", "workspace-write", "--json"}
	assertArgs(t, spec.Args, want)
}

func TestBuildCommandCapturesAndResumesSession(t *testing.T) {
	first, err := BuildCommand(testCommandWorker(map[string]any{"codex_capture_session": true}), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, first.Args, []string{"exec", "--cd", "/repo", "--sandbox", "workspace-write", "--json"})

	resumed, err := BuildCommand(testCommandWorker(map[string]any{"codex_session_id": "thread-123", "codex_capture_session": true}), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, resumed.Args, []string{"exec", "resume", "--config", `sandbox_mode="workspace-write"`, "--json", "thread-123"})
}

func TestBuildCommandPreservesDangerFullAccessWhenResumingSession(t *testing.T) {
	resumed, err := BuildCommand(testCommandWorker(map[string]any{
		"codex_session_id": "thread-123",
		"engine": map[string]any{
			"name":    "codex",
			"sandbox": "danger-full-access",
		},
	}), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, resumed.Args, []string{"exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "--json", "thread-123"})
}

func TestBuildCommandMapsDangerFullAccessToBypassForInitialSession(t *testing.T) {
	initial, err := BuildCommand(testCommandWorker(map[string]any{
		"engine": map[string]any{
			"name":    "codex",
			"sandbox": "danger-full-access",
		},
	}), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, initial.Args, []string{"exec", "--cd", "/repo", "--dangerously-bypass-approvals-and-sandbox", "--json"})
}

func TestBuildCommandMapsFrontmatter(t *testing.T) {
	worker := testCommandWorker(map[string]any{
		"engine": map[string]any{
			"name":    "codex",
			"profile": "backend",
			"model":   "gpt-5.5",
		},
		"sandbox":           "read-only",
		"approval":          "on-request",
		"working_directory": "backend",
		"web_search":        true,
		"images":            []any{"screenshot.png", "/tmp/absolute.png"},
	})
	spec, err := BuildCommand(worker, "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exec", "--cd", "/repo/backend", "--sandbox", "read-only", "--json",
		"--model", "gpt-5.5", "--profile", "backend", "--search",
		"--image", "/repo/tasks/screenshot.png", "--image", "/tmp/absolute.png",
	}
	assertArgs(t, spec.Args, want)
}

func TestBuildCommandMapsV2EngineMetadata(t *testing.T) {
	worker := testCommandWorker(map[string]any{
		"engine": map[string]any{
			"name":       "codex",
			"profile":    "backend",
			"model":      "gpt-5.5",
			"sandbox":    "read-only",
			"approval":   "on-request",
			"web_search": true,
			"images":     []any{"screenshot.png"},
		},
		"working_directory": "backend",
	})
	spec, err := BuildCommand(worker, "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exec", "--cd", "/repo/backend", "--sandbox", "read-only", "--json",
		"--model", "gpt-5.5", "--profile", "backend", "--search",
		"--image", "/repo/tasks/screenshot.png",
	}
	assertArgs(t, spec.Args, want)
}

func TestBuildCommandPrefersEngineMetadataOverLegacy(t *testing.T) {
	worker := testCommandWorker(map[string]any{
		"engine": map[string]any{
			"name":       "codex",
			"sandbox":    "read-only",
			"approval":   "on-request",
			"web_search": false,
			"images":     []any{"engine.png"},
		},
		"sandbox":    "danger-full-access",
		"approval":   "never",
		"web_search": true,
		"images":     []any{"legacy.png"},
	})
	spec, err := BuildCommand(worker, "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exec", "--cd", "/repo", "--sandbox", "read-only", "--json",
		"--image", "/repo/tasks/engine.png",
	}
	assertArgs(t, spec.Args, want)
}

func TestBuildCommandRejectsInvalidSandbox(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"sandbox": "bad"}), "codex")
	if err == nil || !strings.Contains(err.Error(), "unsupported sandbox") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsNonStringSandbox(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"sandbox": true}), "codex")
	if err == nil || !strings.Contains(err.Error(), "sandbox must be a string") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsInvalidV2Sandbox(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{
		"engine": map[string]any{"name": "codex", "sandbox": "bad"},
	}), "codex")
	if err == nil || !strings.Contains(err.Error(), "unsupported sandbox") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsInvalidApproval(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"approval": "bad"}), "codex")
	if err == nil || !strings.Contains(err.Error(), "unsupported approval") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsNonStringApproval(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"approval": true}), "codex")
	if err == nil || !strings.Contains(err.Error(), "approval must be a string") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsInvalidV2Approval(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{
		"engine": map[string]any{"name": "codex", "approval": "bad"},
	}), "codex")
	if err == nil || !strings.Contains(err.Error(), "unsupported approval") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsAbsoluteWorkingDirectory(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"working_directory": "/tmp/outside"}), "codex")
	if err == nil || !strings.Contains(err.Error(), "working_directory must be relative") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommandRejectsEscapingWorkingDirectory(t *testing.T) {
	_, err := BuildCommand(testCommandWorker(map[string]any{"working_directory": "../outside"}), "codex")
	if err == nil || !strings.Contains(err.Error(), "working_directory must stay within") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromptContentPreservation(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "prompt.out")
	fake := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nlast=''\nfor arg do last=\"$arg\"; done\nprintf '%s' \"$last\" > "+zshQuote(out)+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "# Title\n\n- item\n\n"
	worker := writeCodexWorker(t, dir, prompt, map[string]any{})

	exitCode, err := ExecuteWorker(worker.RecordPath, fake, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != prompt {
		t.Fatalf("prompt = %q, want %q", string(got), prompt)
	}
}

func TestExecuteWorkerReturnsCodexExitCode(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	worker := writeCodexWorker(t, dir, "# Task\n", map[string]any{})

	exitCode, err := ExecuteWorker(worker.RecordPath, fake, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 7 {
		t.Fatalf("exitCode = %d, want 7", exitCode)
	}
	store := state.NewStore(filepath.Join(dir, "state"))
	events, err := store.ReadEvents(worker.ID, state.EventFilter{Type: state.EventEngineFinished})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Data["exit_code"] == nil || events.Events[0].Engine != "codex" {
		t.Fatalf("finished event = %#v", events.Events)
	}
}

func TestParseResultDoesNotInventUsageOrCost(t *testing.T) {
	result := Engine{}.ParseResult(execution.Context{}, []byte("no usage reported"))
	if !result.Empty() {
		t.Fatalf("result = %#v, want empty", result)
	}
	if result.TokensInput != nil || result.TokensOutput != nil || result.TokensTotal != nil || result.Cost != nil {
		t.Fatalf("usage/cost should not be invented: %#v", result)
	}
}

func TestParseResultCapturesRuntimeReportedModel(t *testing.T) {
	log := []byte("OpenAI Codex v1\n--------\nmodel: gpt-5.6-sol\nprovider: openai\n--------\n")
	result := Engine{}.ParseResult(execution.Context{}, log)
	if result.Diagnostics["model"] != "gpt-5.6-sol" {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	if got := reportedModel([]byte("model: $(touch nope)\n")); got != "" {
		t.Fatalf("unsafe model accepted: %q", got)
	}
}

func TestParseResultExtractsCodexThreadID(t *testing.T) {
	log := []byte("PromptGrinder dev\n{\"type\":\"thread.started\",\"thread_id\":\"thread-123\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Implemented the feature.\"}}\n")
	result := Engine{}.ParseResult(execution.Context{}, log)
	if result.SessionID != "thread-123" {
		t.Fatalf("session id = %q", result.SessionID)
	}
	if result.Summary != "Implemented the feature." {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestParseResultExtractsBlockingCompletionReport(t *testing.T) {
	log := []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"STATUS: BLOCKED\\n\\nCHANGED_FILES:\\n- none\\n\\nNEXT_PROMPT_SAFE: no\"}}\n")
	result := Engine{}.ParseResult(execution.Context{}, log)

	if result.CompletionStatus != "BLOCKED" || result.NextPromptSafe == nil || *result.NextPromptSafe {
		t.Fatalf("result = %#v", result)
	}
	if !result.RejectsContinuation() {
		t.Fatal("blocked completion should reject continuation")
	}
}

func TestParseResultRejectsDuplicateCompletionFields(t *testing.T) {
	log := []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"STATUS: PASS\\nSTATUS: BLOCKED\\nNEXT_PROMPT_SAFE: yes\"}}\n")
	result := Engine{}.ParseResult(execution.Context{}, log)
	if result.CompletionReason != "duplicate completion fields" {
		t.Fatalf("result = %#v", result)
	}
	if err := result.OrderedCompletionError(); err == nil {
		t.Fatal("duplicate semantic fields must not be accepted")
	}
}

func TestParseResultFindsFinalCompletionAfterLargeCommandEvent(t *testing.T) {
	largeOutput := strings.Repeat("x", 256*1024)
	opening := `{"type":"item.completed","item":{"type":"agent_message","text":"Focused tests passed; running the final gate."}}` + "\n"
	command := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"` + largeOutput + `"}}` + "\n"
	completion := `{"type":"item.completed","item":{"type":"agent_message","text":"STATUS: PASS\nSUMMARY:\n- Final gate passed.\nNEXT_PROMPT_SAFE: yes"}}` + "\n"

	result := (Engine{}).ParseResult(execution.Context{}, []byte(opening+command+completion))

	if result.CompletionStatus != "PASS" || result.NextPromptSafe == nil || !*result.NextPromptSafe {
		t.Fatalf("completion = status %q safe %v summary %q", result.CompletionStatus, result.NextPromptSafe, result.Summary)
	}
	if !strings.Contains(result.Summary, "Final gate passed") {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestExecuteWorkerTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 2\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	worker := writeCodexWorker(t, dir, "# Task\n", map[string]any{"timeout": "50ms"})

	exitCode, err := ExecuteWorker(worker.RecordPath, fake, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 124 {
		t.Fatalf("exitCode = %d, want 124", exitCode)
	}
	store := state.NewStore(filepath.Join(dir, "state"))
	events, err := store.ReadEvents(worker.ID, state.EventFilter{Type: state.EventEngineTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Data["timeout"] != "50ms" {
		t.Fatalf("timeout event = %#v", events.Events)
	}
	if events.Events[0].Data["exit_code"] == nil {
		t.Fatalf("timeout event missing exit_code: %#v", events.Events[0].Data)
	}
}

func TestExecuteWorkerCapturesCompleteStdoutForResultParsing(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-codex")
	output := `{"type":"item.completed","item":{"type":"agent_message","text":"STATUS: PASS\nNEXT_PROMPT_SAFE: yes"}}`
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	worker := writeCodexWorker(t, dir, "# Task\n", map[string]any{})

	exitCode, err := ExecuteWorker(worker.RecordPath, fake, io.Discard, io.Discard)
	if err != nil || exitCode != 0 {
		t.Fatalf("exitCode = %d, err = %v", exitCode, err)
	}
	captured, err := os.ReadFile(CapturedOutputPath(worker.RecordPath))
	if err != nil {
		t.Fatal(err)
	}
	result := Engine{}.ParseResult(execution.Context{}, captured)
	if result.CompletionStatus != "PASS" || result.NextPromptSafe == nil || !*result.NextPromptSafe {
		t.Fatalf("result = %#v, captured = %q", result, captured)
	}
}

func testCommandWorker(metadata map[string]any) state.Worker {
	return state.Worker{
		ID:             "wrk_test",
		RepositoryPath: "/repo",
		TaskPath:       "/repo/tasks/task.md",
		PromptPath:     "/repo/state/prompt.md",
		Engine:         "codex",
		Metadata:       metadata,
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", got, want)
		}
	}
}

func writeCodexWorker(t *testing.T, dir, prompt string, metadata map[string]any) state.Worker {
	t.Helper()
	store := state.NewStore(filepath.Join(dir, "state"))
	worker := testCommandWorker(metadata)
	worker.ID = "wrk_exec"
	worker.RepositoryPath = dir
	worker.TaskPath = filepath.Join(dir, "task.md")
	worker.RecordPath = store.RecordPath(worker.ID)
	worker.PromptPath = filepath.Join(store.WorkerDir(worker.ID), "prompt.md")
	if err := os.MkdirAll(filepath.Dir(worker.PromptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker.PromptPath, []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	return worker
}
