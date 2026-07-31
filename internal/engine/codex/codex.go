package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/execution"
	"promptgrinder/internal/state"
)

type Engine struct {
	Command string
}

func (e Engine) WithCommand(command string) engine.Engine {
	e.Command = command
	return e
}

func (e Engine) Name() string {
	return "codex"
}

func (e Engine) Describe() engine.Descriptor {
	return engine.Descriptor{
		Name:        e.Name(),
		Description: "OpenAI Codex CLI adapter using codex exec.",
		Capabilities: engine.Capabilities{
			SupportsModel:               true,
			SupportsProfile:             true,
			SupportsSandbox:             true,
			SupportsApproval:            true,
			SupportsWorkingDirectory:    true,
			SupportsWebSearch:           true,
			SupportsImages:              true,
			SupportsHeadless:            true,
			SupportsInteractiveTerminal: true,
			SupportsResume:              true,
		},
	}
}

func (e Engine) Validate(ctx execution.Context) error {
	_, err := e.buildCommand(ctx)
	return err
}

func (e Engine) ResolveMetadata(ctx execution.Context) (map[string]any, error) {
	opts, err := optionsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := cloneAnyMap(ctx.Metadata)
	engineMetadata := cloneAnyMap(opts.EngineMetadata)
	engineMetadata["name"] = "codex"
	if opts.Model != "" {
		engineMetadata["model"] = opts.Model
	}
	if opts.Profile != "" {
		engineMetadata["profile"] = opts.Profile
	}
	engineMetadata["sandbox"] = opts.Sandbox
	engineMetadata["approval"] = opts.Approval
	engineMetadata["web_search"] = opts.WebSearch
	if len(opts.Images) > 0 {
		images := make([]any, 0, len(opts.Images))
		for _, image := range opts.Images {
			images = append(images, image)
		}
		engineMetadata["images"] = images
	}
	out["engine"] = engineMetadata
	return out, nil
}

func (e Engine) Build(ctx execution.Context, prompt []byte, executablePath string) (execution.Request, error) {
	worker := contextWorker(ctx)
	script := e.buildScript(worker, executablePath, ctx.HeartbeatInterval)
	commandData := map[string]any{}
	if spec, err := e.buildCommand(ctx); err == nil {
		commandData = commandEventData(ctx, spec)
	}
	return execution.Request{Context: ctx, Worker: worker, Prompt: prompt, Script: script, CommandData: commandData}, nil
}

func (e Engine) ParseResult(ctx execution.Context, log []byte) state.EngineResult {
	result := state.EngineResult{}
	scanner := bufio.NewScanner(bytes.NewReader(log))
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			result.SessionID = event.ThreadID
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" && strings.TrimSpace(event.Item.Text) != "" {
			result.Summary = strings.TrimSpace(event.Item.Text)
		}
	}
	result.CompletionStatus, result.NextPromptSafe = parseCompletionReport(result.Summary)
	return result
}

func parseCompletionReport(summary string) (string, *bool) {
	completionStatus := ""
	var nextPromptSafe *bool
	for _, line := range strings.Split(summary, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "STATUS":
			value = strings.ToUpper(strings.TrimSpace(value))
			if value == "PASS" || value == "PARTIAL" || value == "BLOCKED" {
				completionStatus = value
			}
		case "NEXT_PROMPT_SAFE":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "yes":
				value := true
				nextPromptSafe = &value
			case "no":
				value := false
				nextPromptSafe = &value
			}
		}
	}
	return completionStatus, nextPromptSafe
}

func (e Engine) BuildScript(worker state.Worker, executablePath string) string {
	return e.buildScript(worker, executablePath, 30*time.Second)
}

func (e Engine) buildScript(worker state.Worker, executablePath string, heartbeatInterval time.Duration) string {
	command := e.command()
	spec, err := BuildCommand(worker, command)
	commandPreview := "codex command unavailable"
	if err == nil {
		commandPreview = spec.String()
	}
	homeDir := homeDirFromRecordPath(worker.RecordPath)
	heartbeatSeconds := int(heartbeatInterval.Seconds())
	if heartbeatSeconds <= 0 {
		heartbeatSeconds = 30
	}
	plainDefault := ""
	if os.Getenv("PROMPTGRINDER_PLAIN") != "" || os.Getenv("NO_COLOR") != "" {
		plainDefault = "1"
	}
	closeOnFinish := "0"
	if worker.CloseOnFinish {
		closeOnFinish = "1"
	}
	closeOnFailure := "0"
	if worker.CloseOnFailure {
		closeOnFailure = "1"
	}
	return fmt.Sprintf(`#!/bin/zsh
set -u
unsetopt BG_NICE 2>/dev/null || true

PROMPT_PATH=%s
TASK_PATH=%s
REPOSITORY_PATH=%s
LOG_PATH=%s
RECORD_PATH=%s
WORKER_ID=%s
CODEX_BIN=%s
PROMPTGRINDER_BIN=%s
PROMPTGRINDER_HOME=%s
HEARTBEAT_INTERVAL_SECONDS=%d
PROMPTGRINDER_PLAIN=%s
CLOSE_ON_FINISH=%s
CLOSE_ON_FAILURE=%s
export PROMPTGRINDER_HOME
export PROMPTGRINDER_PLAIN

exec > >(/usr/bin/tee -a "$LOG_PATH") 2>&1

COMMAND_PREVIEW=%s
STARTED_AT=$(/bin/date -u +"%%Y-%%m-%%dT%%H:%%M:%%SZ")
PROMPT_NAME="${TASK_PATH:t}"
TITLE="PromptGrinder: $WORKER_ID"

if [ -t 1 ] && [ -z "${PROMPTGRINDER_PLAIN:-}" ]; then
  printf '\033]0;%%s\007' "$TITLE"
fi

print_launch_header() {
  if [ -n "${NO_COLOR:-}${PROMPTGRINDER_PLAIN:-}" ] || [ ! -t 1 ]; then
    echo "PromptGrinder dev"
    echo "Prompt: $PROMPT_NAME"
    echo "Worker: $WORKER_ID"
    echo "Repository: $REPOSITORY_PATH"
    echo "Started: $STARTED_AT"
    echo "Log: $LOG_PATH"
    return
  fi
  local rows=(
    "PromptGrinder dev"
    "Prompt: $PROMPT_NAME"
    "Worker: $WORKER_ID"
    "Repository: $REPOSITORY_PATH"
    "Started: $STARTED_AT"
    "Log: $LOG_PATH"
  )
  local width=0
  local row
  for row in "${rows[@]}"; do
    if [ ${#row} -gt $width ]; then
      width=${#row}
    fi
  done
  local border=""
  local i
  for ((i=0; i<width+2; i++)); do
    border="${border}─"
  done
  echo "┌${border}┐"
  for row in "${rows[@]}"; do
    printf "│ %%-${width}s │\n" "$row"
  done
  echo "└${border}┘"
}

print_launch_header
printf 'Command: %%s\n' "$COMMAND_PREVIEW"
echo

heartbeat() {
  "$PROMPTGRINDER_BIN" __worker-heartbeat "$WORKER_ID" || echo "Warning: worker heartbeat failed."
}

heartbeat_loop() {
  while true; do
    /bin/sleep "$HEARTBEAT_INTERVAL_SECONDS"
    heartbeat
  done
}

heartbeat
HEARTBEAT_PID=""
if [ "${PROMPTGRINDER_HEADLESS:-}" != "1" ]; then
  heartbeat_loop &
  HEARTBEAT_PID=$!
fi

if [ ! -x "$CODEX_BIN" ]; then
  echo "Error: validated Codex executable is no longer executable: $CODEX_BIN"
  EXIT_CODE=127
else
  "$PROMPTGRINDER_BIN" __engine-codex "$RECORD_PATH" "$CODEX_BIN"
  EXIT_CODE=$?
fi

if [ -n "$HEARTBEAT_PID" ]; then
  kill "$HEARTBEAT_PID" >/dev/null 2>&1 || true
  wait "$HEARTBEAT_PID" >/dev/null 2>&1 || true
fi
heartbeat

"$PROMPTGRINDER_BIN" __worker-finish "$RECORD_PATH" "$EXIT_CODE"
FINISH_EXIT_CODE=$?
if [ "$EXIT_CODE" -eq 0 ] && [ "$FINISH_EXIT_CODE" -ne 0 ]; then
  EXIT_CODE=$FINISH_EXIT_CODE
fi
echo
echo "Finished with exit code $EXIT_CODE."
echo "Log: $LOG_PATH"
if [ "${PROMPTGRINDER_HEADLESS:-}" = "1" ]; then
  exit "$EXIT_CODE"
fi
if { [ "$EXIT_CODE" -eq 0 ] && [ "$CLOSE_ON_FINISH" = "1" ]; } || { [ "$EXIT_CODE" -ne 0 ] && [ "$CLOSE_ON_FAILURE" = "1" ]; }; then
  ("$PROMPTGRINDER_BIN" __terminal-close "$WORKER_ID" >/dev/null 2>&1) &!
  exit "$EXIT_CODE"
fi
echo "This terminal will remain open. Type exit to close it."
KEEP_OPEN_SHELL="${SHELL:-/bin/zsh}"
if [ -z "$KEEP_OPEN_SHELL" ] || [ ! -x "$KEEP_OPEN_SHELL" ]; then
  KEEP_OPEN_SHELL="/bin/zsh"
fi
exec "$KEEP_OPEN_SHELL" -l
`,
		zshQuote(worker.PromptPath),
		zshQuote(worker.TaskPath),
		zshQuote(worker.RepositoryPath),
		zshQuote(worker.LogPath),
		zshQuote(worker.RecordPath),
		zshQuote(worker.ID),
		zshQuote(command),
		zshQuote(executablePath),
		zshQuote(homeDir),
		heartbeatSeconds,
		zshQuote(plainDefault),
		zshQuote(closeOnFinish),
		zshQuote(closeOnFailure),
		zshQuote(commandPreview),
	)
}

func Execute(command, promptPath, repositoryPath string, stdout, stderr io.Writer) (int, error) {
	if command == "" {
		command = "codex"
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(command, "exec", "--cd", repositoryPath, string(prompt))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return 127, err
	}
	return 1, err
}

func ExecuteWorker(recordPath, command string, stdout, stderr io.Writer) (int, error) {
	store := state.Store{}
	worker, err := store.LoadPath(recordPath)
	if err != nil {
		return 1, err
	}
	spec, err := BuildCommand(worker, command)
	if err != nil {
		return 1, err
	}
	executionContext, err := execution.NewContext(worker, config.Config{}, nil)
	if err != nil {
		return 1, err
	}
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineStarted, state.SeverityInfo, "Engine started", map[string]any{"engine": worker.Engine, "command": spec.String()}))
	prompt, err := os.ReadFile(worker.PromptPath)
	if err != nil {
		return 1, err
	}
	timeout, err := execution.TimeoutFromMetadata(worker.Metadata)
	if err != nil {
		return 1, err
	}
	args := append([]string{}, spec.Args...)
	args = append(args, string(prompt))
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return signalProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.Env = workerEnvironment(worker.Metadata, os.Environ())
	cmd.Dir = executionContext.WorkingDirectory
	capturedOutput, err := os.Create(CapturedOutputPath(recordPath))
	if err != nil {
		return 1, fmt.Errorf("create Codex result capture: %w", err)
	}
	cmd.Stdout = io.MultiWriter(stdout, capturedOutput)
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		_ = capturedOutput.Close()
		if errors.Is(err, exec.ErrNotFound) {
			_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineFinished, state.SeverityError, "Engine command not found", map[string]any{"engine": worker.Engine, "exit_code": 127, "error": err.Error()}))
			return 127, err
		}
		return 1, err
	}
	pid := cmd.Process.Pid
	if err := store.SetProcess(recordPath, pid, pid); err != nil {
		_ = signalProcessGroup(pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = capturedOutput.Close()
		return 1, fmt.Errorf("record Codex process ownership: %w", err)
	}
	processDone := make(chan struct{})
	defer close(processDone)
	if timeout > 0 {
		go func() {
			select {
			case <-ctx.Done():
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
					_ = signalProcessGroup(pid, syscall.SIGKILL)
				case <-processDone:
				}
			case <-processDone:
			}
		}()
	}
	err = cmd.Wait()
	_ = store.ClearProcess(recordPath, pid)
	if closeErr := capturedOutput.Close(); closeErr != nil && err == nil {
		return 1, fmt.Errorf("close Codex result capture: %w", closeErr)
	}
	if err == nil {
		_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineFinished, state.SeverityInfo, "Engine finished", map[string]any{"engine": worker.Engine, "exit_code": 0}))
		return 0, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "PromptGrinder timeout after %s.\n", timeout)
		_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineTimeout, state.SeverityError, "Engine timed out", map[string]any{"engine": worker.Engine, "timeout": timeout.String(), "exit_code": 124}))
		return 124, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineFinished, state.SeverityError, "Engine finished", map[string]any{"engine": worker.Engine, "exit_code": exitErr.ExitCode()}))
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineFinished, state.SeverityError, "Engine command not found", map[string]any{"engine": worker.Engine, "exit_code": 127, "error": err.Error()}))
		return 127, err
	}
	_ = state.AppendEventForWorker(worker, state.NewEvent(worker.ID, state.EventEngineFinished, state.SeverityError, "Engine finished", map[string]any{"engine": worker.Engine, "exit_code": 1, "error": err.Error()}))
	return 1, err
}

func signalProcessGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	err := syscall.Kill(-pgid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func CapturedOutputPath(recordPath string) string {
	return filepath.Join(filepath.Dir(recordPath), "engine-output.jsonl")
}

func workerEnvironment(metadata map[string]any, parent []string) []string {
	allowed := map[string]bool{
		"HOME": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true,
		"TERM": true, "COLORTERM": true, "SSH_AUTH_SOCK": true,
	}
	values := map[string]string{}
	for _, entry := range parent {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (allowed[key] || strings.HasPrefix(key, "LC_")) {
			values[key] = value
		}
	}
	if raw, ok := metadata["env"].(map[string]any); ok {
		for key, value := range raw {
			if text, ok := value.(string); ok {
				values[key] = text
			}
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func commandEventData(ctx execution.Context, spec CommandSpec) map[string]any {
	options, err := optionsFromContext(ctx)
	if err != nil {
		return map[string]any{"command_preview": spec.String()}
	}
	data := map[string]any{
		"engine":            "codex",
		"command_preview":   spec.String(),
		"sandbox":           options.Sandbox,
		"approval":          options.Approval,
		"working_directory": options.WorkingDirectory,
	}
	if options.Profile != "" {
		data["profile"] = options.Profile
	}
	if options.Model != "" {
		data["model"] = options.Model
	}
	if options.WebSearch {
		data["web_search"] = true
	}
	if len(options.Images) > 0 {
		data["images"] = options.Images
	}
	return data
}

func (e Engine) buildCommand(ctx execution.Context) (CommandSpec, error) {
	return commandFromContext(ctx, e.command())
}

func (e Engine) command() string {
	if e.Command == "" {
		return "codex"
	}
	return e.Command
}

func contextWorker(ctx execution.Context) state.Worker {
	return state.Worker{
		ID:             ctx.WorkerID,
		RecordPath:     filepath.Join(ctx.Directories.Worker, "worker.json"),
		RepositoryPath: ctx.RepositoryRoot,
		TaskPath:       ctx.TaskPath,
		PromptPath:     ctx.Directories.Prompt,
		Engine:         "codex",
		Status:         state.StatusCreated,
		LogPath:        ctx.Directories.Log,
		Metadata:       ctx.Metadata,
		CloseOnFinish:  ctx.CloseOnFinish,
		CloseOnFailure: ctx.CloseOnFailure,
	}
}

func homeDirFromRecordPath(recordPath string) string {
	workerDir := filepath.Dir(recordPath)
	workersDir := filepath.Dir(workerDir)
	return filepath.Dir(workersDir)
}

func zshQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type CommandSpec struct {
	Executable string
	Args       []string
}

func (c CommandSpec) String() string {
	parts := []string{c.Executable}
	parts = append(parts, c.Args...)
	for i, part := range parts {
		parts[i] = zshQuote(part)
	}
	return strings.Join(parts, " ")
}

func BuildCommand(worker state.Worker, executable string) (CommandSpec, error) {
	ctx := execution.Context{
		RepositoryRoot:   worker.RepositoryPath,
		TaskPath:         worker.TaskPath,
		WorkerID:         worker.ID,
		WorkingDirectory: worker.RepositoryPath,
		Metadata:         worker.Metadata,
	}
	return commandFromContext(ctx, executable)
}

func commandFromContext(ctx execution.Context, executable string) (CommandSpec, error) {
	if executable == "" {
		executable = "codex"
	}
	options, err := optionsFromContext(ctx)
	if err != nil {
		return CommandSpec{}, err
	}
	args := []string{"exec"}
	if options.SessionID != "" {
		args = append(args, "resume")
		if options.Sandbox == "danger-full-access" {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--config", fmt.Sprintf("sandbox_mode=%q", options.Sandbox))
		}
		args = append(args, "--json")
	} else {
		args = append(args, "--cd", options.WorkingDirectory)
		if options.Sandbox == "danger-full-access" {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--sandbox", options.Sandbox)
		}
		args = append(args, "--json")
	}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.Profile != "" && options.SessionID == "" {
		args = append(args, "--profile", options.Profile)
	}
	if options.WebSearch && options.SessionID == "" {
		args = append(args, "--search")
	}
	for _, image := range options.Images {
		args = append(args, "--image", image)
	}
	if options.SessionID != "" {
		args = append(args, options.SessionID)
	}
	return CommandSpec{Executable: executable, Args: args}, nil
}

type options struct {
	Sandbox          string
	Approval         string
	WorkingDirectory string
	Model            string
	Profile          string
	WebSearch        bool
	Images           []string
	EngineMetadata   map[string]any
	SessionID        string
	CaptureSession   bool
}

func optionsFromContext(ctx execution.Context) (options, error) {
	opts := options{
		Sandbox:          codexSandboxDefault(ctx),
		Approval:         codexApprovalDefault(ctx),
		WorkingDirectory: ctx.RepositoryRoot,
	}
	metadata := ctx.Metadata
	if sessionID, ok := metadata["codex_session_id"].(string); ok {
		opts.SessionID = sessionID
	}
	if capture, ok := metadata["codex_capture_session"].(bool); ok {
		opts.CaptureSession = capture
	}
	if opts.WorkingDirectory == "" {
		opts.WorkingDirectory = "."
	}
	engineMetadata, err := mapValue(metadata["engine"])
	if err != nil {
		return options{}, err
	}
	opts.EngineMetadata = engineMetadata
	if name, ok := engineMetadata["name"].(string); ok && name != "" && name != "codex" {
		return options{}, fmt.Errorf("unsupported engine for this MVP: %s", name)
	}
	if profile, ok := engineMetadata["profile"].(string); ok {
		opts.Profile = profile
	}
	if model, ok := engineMetadata["model"].(string); ok {
		opts.Model = model
	}
	if err := validateStringField(metadata, "sandbox"); err != nil {
		return options{}, err
	}
	if err := validateStringField(engineMetadata, "sandbox"); err != nil {
		return options{}, err
	}
	if sandbox, ok := metadata["sandbox"].(string); ok && sandbox != "" {
		opts.Sandbox = sandbox
	}
	if sandbox, ok := engineMetadata["sandbox"].(string); ok && sandbox != "" {
		opts.Sandbox = sandbox
	}
	if !validSandbox(opts.Sandbox) {
		return options{}, fmt.Errorf("unsupported sandbox value %q: use read-only, workspace-write, or danger-full-access", opts.Sandbox)
	}
	if err := validateStringField(metadata, "approval"); err != nil {
		return options{}, err
	}
	if err := validateStringField(engineMetadata, "approval"); err != nil {
		return options{}, err
	}
	if approval, ok := metadata["approval"].(string); ok && approval != "" {
		opts.Approval = approval
	}
	if approval, ok := engineMetadata["approval"].(string); ok && approval != "" {
		opts.Approval = approval
	}
	if !validApproval(opts.Approval) {
		return options{}, fmt.Errorf("unsupported approval value %q: use never, on-failure, on-request, or untrusted", opts.Approval)
	}
	if ctx.WorkingDirectory != "" {
		opts.WorkingDirectory = ctx.WorkingDirectory
	}
	if workingDirectory, ok := metadata["working_directory"].(string); ok && workingDirectory != "" {
		resolved, err := resolveWorkingDirectory(ctx.RepositoryRoot, workingDirectory)
		if err != nil {
			return options{}, err
		}
		opts.WorkingDirectory = resolved
	}
	if err := validateBoolField(metadata, "web_search"); err != nil {
		return options{}, err
	}
	if err := validateBoolField(engineMetadata, "web_search"); err != nil {
		return options{}, err
	}
	if webSearch, ok := metadata["web_search"].(bool); ok {
		opts.WebSearch = webSearch
	}
	if webSearch, ok := engineMetadata["web_search"].(bool); ok {
		opts.WebSearch = webSearch
	}
	imagesValue := metadata["images"]
	if value, ok := engineMetadata["images"]; ok {
		imagesValue = value
	}
	images, err := stringSlice(imagesValue)
	if err != nil {
		return options{}, err
	}
	taskDir := filepath.Dir(ctx.TaskPath)
	for _, image := range images {
		opts.Images = append(opts.Images, resolveAgainst(taskDir, image))
	}
	return opts, nil
}

func validateStringField(metadata map[string]any, key string) error {
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	if _, ok := value.(string); !ok {
		return fmt.Errorf("%s must be a string", key)
	}
	return nil
}

func validateBoolField(metadata map[string]any, key string) error {
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%s must be a boolean", key)
	}
	return nil
}

func codexSandboxDefault(ctx execution.Context) string {
	if ctx.Config.CodexSandbox != "" {
		return ctx.Config.CodexSandbox
	}
	return "workspace-write"
}

func codexApprovalDefault(ctx execution.Context) string {
	if ctx.Config.CodexApproval != "" {
		return ctx.Config.CodexApproval
	}
	return "never"
}

func mapValue(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	switch typed := value.(type) {
	case string:
		if typed == "" || typed == "codex" {
			return map[string]any{"name": typed}, nil
		}
		return nil, fmt.Errorf("unsupported engine for this MVP: %s", typed)
	case map[string]any:
		return typed, nil
	default:
		return nil, fmt.Errorf("engine metadata must be a string or map")
	}
}

func stringSlice(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []any:
		out := []string{}
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("images must contain only strings")
			}
			out = append(out, text)
		}
		return out, nil
	case []string:
		return typed, nil
	default:
		return nil, fmt.Errorf("images must be a list of strings")
	}
}

func resolveAgainst(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func resolveWorkingDirectory(repositoryPath, value string) (string, error) {
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("working_directory must be relative to the repository root: %s", value)
	}
	base := filepath.Clean(repositoryPath)
	target := filepath.Clean(filepath.Join(base, value))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("working_directory must stay within the repository: %s", value)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working_directory must stay within the repository: %s", value)
	}
	return target, nil
}

func validSandbox(value string) bool {
	switch value {
	case "read-only", "workspace-write", "danger-full-access":
		return true
	default:
		return false
	}
}

func validApproval(value string) bool {
	switch value {
	case "never", "on-failure", "on-request", "untrusted":
		return true
	default:
		return false
	}
}

func cloneAnyMap(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		out[key] = value
	}
	return out
}
