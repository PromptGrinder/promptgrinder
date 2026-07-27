package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TerminalAdapter interface {
	Name() string
	Command(scriptPath string) string
	Launch(scriptPath string) error
}

type TerminalAppAdapter struct{}

const (
	ownedTitlePrefix = "PromptGrinder: wrk_"
	closeAllSentinel = "__PROMPTGRINDER_ALL__"
)

type AppleScriptFailureKind string

const (
	AppleScriptPermissionDenied AppleScriptFailureKind = "permission_denied"
	AppleScriptInvalid          AppleScriptFailureKind = "invalid_applescript"
	AppleScriptTimeout          AppleScriptFailureKind = "application_timeout"
	AppleScriptApplication      AppleScriptFailureKind = "application_error"
)

type AppleScriptError struct {
	Operation string
	Kind      AppleScriptFailureKind
	Output    string
	Err       error
}

func (e *AppleScriptError) Error() string {
	detail := strings.TrimSpace(e.Output)
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	return fmt.Sprintf("%s failed (%s): %s", e.Operation, e.Kind, detail)
}

func (e *AppleScriptError) Unwrap() error { return e.Err }

var appleScriptTimeout = 15 * time.Second
var runAppleScript = func(ctx context.Context, script string) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).CombinedOutput()
}

func executeAppleScript(operation, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), appleScriptTimeout)
	defer cancel()
	out, err := runAppleScript(ctx, script)
	if err == nil {
		return nil
	}
	return &AppleScriptError{
		Operation: operation,
		Kind:      classifyAppleScriptFailure(ctx.Err(), string(out)),
		Output:    string(out),
		Err:       err,
	}
}

func classifyAppleScriptFailure(ctxErr error, output string) AppleScriptFailureKind {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return AppleScriptTimeout
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "not authorized"),
		strings.Contains(lower, "not permitted"),
		strings.Contains(lower, "automation"),
		strings.Contains(lower, "-1743"):
		return AppleScriptPermissionDenied
	case strings.Contains(lower, "syntax error"), strings.Contains(lower, "-2741"):
		return AppleScriptInvalid
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "-1712"):
		return AppleScriptTimeout
	default:
		return AppleScriptApplication
	}
}

func (a TerminalAppAdapter) Name() string {
	return "terminal"
}

func (a TerminalAppAdapter) Command(scriptPath string) string {
	return fmt.Sprintf("/bin/zsh %q", scriptPath)
}

func (a TerminalAppAdapter) Launch(scriptPath string) error {
	return executeAppleScript("Terminal.app launch", terminalLaunchAppleScript(scriptPath))
}

func terminalLaunchAppleScript(scriptPath string) string {
	command, _ := json.Marshal((TerminalAppAdapter{}).Command(scriptPath))
	title, _ := json.Marshal(titleFromScript(scriptPath))
	return fmt.Sprintf(`tell application "Terminal"
  activate
  set workerTab to do script %s
  set custom title of workerTab to %s
end tell`, string(command), string(title))
}

func titleFromScript(scriptPath string) string {
	workerID := filepath.Base(filepath.Dir(scriptPath))
	if strings.HasPrefix(workerID, "wrk_") {
		return "PromptGrinder: " + workerID
	}
	return "PromptGrinder"
}

var CloseTerminalTabs = closeTerminalTabs

func closeTerminalTabs(titles []string) error {
	if len(titles) == 0 {
		return nil
	}
	titleValues := appleScriptStringList(titles)
	if err := closeTerminalAppTabs(titleValues); err != nil {
		return err
	}
	if err := closeITermTabs(string(titleValues)); err != nil {
		return err
	}
	return nil
}

func appleScriptStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		quoted = append(quoted, string(encoded))
	}
	return "{" + strings.Join(quoted, ", ") + "}"
}

func closeTerminalAppTabs(titleValues string) error {
	return executeAppleScript("Terminal.app close", terminalCloseAppleScript(titleValues))
}

func terminalCloseAppleScript(titleValues string) string {
	return fmt.Sprintf(`set promptGrinderTitles to %s
tell application "Terminal"
  set closeAllPromptGrinder to false
  repeat with targetTitle in promptGrinderTitles
    if targetTitle is %q then set closeAllPromptGrinder to true
  end repeat
  repeat with targetTitle in promptGrinderTitles
    repeat with terminalWindow in windows
      repeat with terminalTab in tabs of terminalWindow
        set tabTitle to custom title of terminalTab
        if (closeAllPromptGrinder and tabTitle starts with %q) or tabTitle is targetTitle then
          close terminalTab
          exit repeat
        end if
      end repeat
    end repeat
  end repeat
end tell`, titleValues, closeAllSentinel, ownedTitlePrefix)
}

func closeITermTabs(titleValues string) error {
	return executeAppleScript("iTerm2 close", iTermCloseAppleScript(titleValues))
}

func iTermCloseAppleScript(titleValues string) string {
	return fmt.Sprintf(`set promptGrinderTitles to %s
tell application "System Events"
  set iTermIsRunning to exists (application process whose bundle identifier is "com.googlecode.iterm2")
end tell
if iTermIsRunning then
  tell application id "com.googlecode.iterm2"
    set closeAllPromptGrinder to false
    repeat with targetTitle in promptGrinderTitles
      if targetTitle is %q then
        set closeAllPromptGrinder to true
      end if
    end repeat
    repeat with terminalWindow in windows
      repeat with terminalTab in tabs of terminalWindow
        set shouldClose to false
        repeat with terminalSession in sessions of terminalTab
          set sessionName to name of terminalSession
          if closeAllPromptGrinder and sessionName starts with %q then
            set shouldClose to true
          end if
          repeat with targetTitle in promptGrinderTitles
            if sessionName is targetTitle then
              set shouldClose to true
            end if
          end repeat
        end repeat
        if shouldClose then
          close terminalTab
        end if
      end repeat
    end repeat
  end tell
end if`, titleValues, closeAllSentinel, ownedTitlePrefix)
}

type DryRunAdapter struct{}

type ITermAdapter struct{}

func (a ITermAdapter) Name() string {
	return "iterm"
}

func (a ITermAdapter) Command(scriptPath string) string {
	return fmt.Sprintf("/bin/zsh %q", scriptPath)
}

func (a ITermAdapter) Launch(scriptPath string) error {
	return executeAppleScript("iTerm2 launch", iTermLaunchAppleScript(scriptPath))
}

func iTermLaunchAppleScript(scriptPath string) string {
	command, _ := json.Marshal((ITermAdapter{}).Command(scriptPath))
	title, _ := json.Marshal(titleFromScript(scriptPath))
	return fmt.Sprintf(`tell application id "com.googlecode.iterm2"
  activate
  set workerWindow to create window with default profile command %s
  set name of current session of workerWindow to %s
end tell`, string(command), string(title))
}

func (a DryRunAdapter) Name() string {
	return "dry-run"
}

func (a DryRunAdapter) Command(scriptPath string) string {
	return fmt.Sprintf("/bin/zsh %q", scriptPath)
}

func (a DryRunAdapter) Launch(scriptPath string) error {
	return nil
}

type HeadlessAdapter struct{}

func (a HeadlessAdapter) Name() string {
	return "headless"
}

type ExecutionError struct {
	Command string
	Err     error
}

func (e ExecutionError) Error() string {
	return fmt.Sprintf("headless execution failed for %s: %v", e.Command, e.Err)
}

func (e ExecutionError) Unwrap() error {
	return e.Err
}

func IsExecutionError(err error) bool {
	var executionErr ExecutionError
	return errors.As(err, &executionErr)
}

func (a HeadlessAdapter) Command(scriptPath string) string {
	return fmt.Sprintf("/bin/zsh %q", scriptPath)
}

func (a HeadlessAdapter) Launch(scriptPath string) error {
	cmd := exec.Command("/bin/zsh", scriptPath)
	cmd.Env = append(os.Environ(), "PROMPTGRINDER_HEADLESS=1")
	if err := cmd.Run(); err != nil {
		return ExecutionError{Command: a.Command(scriptPath), Err: err}
	}
	return nil
}

type StaticFailure struct {
	Err error
}

func (f StaticFailure) Name() string {
	return "static-failure"
}

func (f StaticFailure) Command(scriptPath string) string {
	return fmt.Sprintf("/bin/zsh %q", scriptPath)
}

func (f StaticFailure) Launch(scriptPath string) error {
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("terminal launch failed")
}

func SelectAdapter(adapter, mode string) (TerminalAdapter, error) {
	if mode == "dry-run" {
		return DryRunAdapter{}, nil
	}
	switch adapter {
	case "", "terminal":
		return TerminalAppAdapter{}, nil
	case "warp":
		return nil, fmt.Errorf("terminal adapter %q is not implemented yet", adapter)
	case "iterm":
		return ITermAdapter{}, nil
	case "headless":
		return HeadlessAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported terminal adapter %q", adapter)
	}
}

func ValidateAvailability(adapter, mode string) error {
	if mode == "dry-run" {
		return nil
	}
	if err := executableFile("/bin/zsh"); err != nil {
		return fmt.Errorf("terminal preflight failed: %w", err)
	}
	switch adapter {
	case "", "terminal":
		if err := executableFile("/usr/bin/osascript"); err != nil {
			return fmt.Errorf("Terminal.app preflight failed: %w", err)
		}
		if !pathExists("/System/Applications/Utilities/Terminal.app") && !pathExists("/Applications/Utilities/Terminal.app") {
			return fmt.Errorf("Terminal.app preflight failed: application is unavailable")
		}
	case "iterm":
		if err := executableFile("/usr/bin/osascript"); err != nil {
			return fmt.Errorf("iTerm2 preflight failed: %w", err)
		}
		if !pathExists("/Applications/iTerm.app") && !pathExists("/Applications/iTerm2.app") {
			return fmt.Errorf("iTerm2 preflight failed: application with bundle identifier com.googlecode.iterm2 is unavailable; install iTerm2 or select terminal.adapter: headless")
		}
	case "headless":
		return nil
	default:
		return fmt.Errorf("unsupported terminal adapter %q", adapter)
	}
	return nil
}

func executableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is unavailable", path)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
