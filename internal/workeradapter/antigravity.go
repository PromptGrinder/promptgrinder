package workeradapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"promptgrinder/internal/workerlaunch"
)

// Antigravity launches Google Antigravity CLI in documented headless mode.
// The CLI can resume a known conversation, but its headless result does not
// currently document a conversation identifier, so SessionResume is not
// advertised.
type Antigravity struct {
	Executable string
	Run        func(context.Context, string, []string, string) ([]byte, int, error)
}

func (Antigravity) Capabilities() workerlaunch.Capabilities {
	return workerlaunch.Capabilities{
		Headless: true, StructuredOutput: true, WorkingDirectory: true,
		Sandbox: true, Approval: true, SessionResume: false,
	}
}

func (a Antigravity) Preflight(_ context.Context, request workerlaunch.LaunchRequest) error {
	if request.Runtime.Name != "antigravity" {
		return fmt.Errorf("Antigravity adapter cannot launch runtime %q", request.Runtime.Name)
	}
	_, err := a.command(request)
	return err
}

func (a Antigravity) Launch(ctx context.Context, request workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	executable, args, err := a.commandParts(request)
	if err != nil {
		return workerlaunch.LaunchResult{}, err
	}
	runID, err := antigravityRunID()
	if err != nil {
		return workerlaunch.LaunchResult{}, err
	}
	started := time.Now().UTC()
	runner := a.Run
	if runner == nil {
		runner = runAntigravity
	}
	output, pid, runErr := runner(ctx, executable, args, request.Worktree)
	finished := time.Now().UTC()
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	result := workerlaunch.LaunchResult{
		RuntimeName: "antigravity", RunID: runID,
		Process: workerlaunch.RuntimeProcessResult{
			PID: pid, ExitCode: &exitCode, StartedAt: &started, FinishedAt: &finished,
		},
	}
	if runErr != nil {
		return result, fmt.Errorf("Antigravity CLI: %w", runErr)
	}
	if err := validateAntigravityResult(output); err != nil {
		return result, err
	}
	return result, nil
}

func (a Antigravity) command(request workerlaunch.LaunchRequest) (string, error) {
	executable, args, err := a.commandParts(request)
	if err != nil {
		return "", err
	}
	return strings.Join(append([]string{executable}, args...), " "), nil
}

func (a Antigravity) commandParts(request workerlaunch.LaunchRequest) (string, []string, error) {
	executable := a.Executable
	if value, ok := request.Runtime.Options["executable"].(string); ok && value != "" {
		executable = value
	}
	if executable == "" {
		executable = "agy"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil && executable == "agy" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			candidate := filepath.Join(home, ".local", "bin", "agy")
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				resolved, err = candidate, nil
			}
		}
	}
	if err != nil {
		return "", nil, fmt.Errorf("Antigravity executable %q was not found: %w", executable, err)
	}
	args := []string{"--print", "--output-format", "json", "--disable-slash-commands"}
	known := map[string]bool{
		"executable": true, "model": true, "agent": true, "effort": true,
		"mode": true, "sandbox": true, "print_timeout": true,
		"dangerously_skip_permissions": true,
	}
	for key := range request.Runtime.Options {
		if !known[key] {
			return "", nil, fmt.Errorf("unsupported Antigravity runtime option %q", key)
		}
	}
	for _, option := range []struct{ key, flag string }{
		{"model", "--model"}, {"agent", "--agent"}, {"effort", "--effort"},
		{"mode", "--mode"}, {"print_timeout", "--print-timeout"},
	} {
		if value, ok := request.Runtime.Options[option.key].(string); ok && value != "" {
			args = append(args, option.flag, value)
		}
	}
	for _, option := range []struct{ key, flag string }{
		{"sandbox", "--sandbox"}, {"dangerously_skip_permissions", "--dangerously-skip-permissions"},
	} {
		if value, ok := request.Runtime.Options[option.key]; ok {
			enabled, ok := value.(bool)
			if !ok {
				return "", nil, fmt.Errorf("Antigravity runtime option %q must be boolean", option.key)
			}
			args = append(args, option.flag+"="+strconv.FormatBool(enabled))
		}
	}
	args = append(args, request.Context)
	return resolved, args, nil
}

func runAntigravity(ctx context.Context, executable string, args []string, worktree string) ([]byte, int, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = worktree
	output, err := command.CombinedOutput()
	pid := 0
	if command.Process != nil {
		pid = command.Process.Pid
	}
	if err != nil {
		return output, pid, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, pid, nil
}

func validateAntigravityResult(output []byte) error {
	if len(strings.TrimSpace(string(output))) == 0 {
		return fmt.Errorf("Antigravity CLI returned empty output")
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("parse Antigravity JSON output: %w", err)
	}
	if raw, ok := result["error"]; ok && raw != nil {
		return fmt.Errorf("Antigravity CLI reported an error: %v", raw)
	}
	return nil
}

func antigravityRunID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "agy_" + time.Now().UTC().Format("20060102T150405") + "_" + hex.EncodeToString(random), nil
}
