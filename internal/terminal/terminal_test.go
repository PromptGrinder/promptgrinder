package terminal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectAdapter(t *testing.T) {
	adapter, err := SelectAdapter("terminal", "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(TerminalAppAdapter); !ok {
		t.Fatalf("adapter = %T, want TerminalAppAdapter", adapter)
	}

	adapter, err = SelectAdapter("terminal", "dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(DryRunAdapter); !ok {
		t.Fatalf("adapter = %T, want DryRunAdapter", adapter)
	}

	adapter, err = SelectAdapter("headless", "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(HeadlessAdapter); !ok {
		t.Fatalf("adapter = %T, want HeadlessAdapter", adapter)
	}

	adapter, err = SelectAdapter("iterm", "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(ITermAdapter); !ok {
		t.Fatalf("adapter = %T, want ITermAdapter", adapter)
	}
}

func TestSelectAdapterRejectsFutureAdapters(t *testing.T) {
	if _, err := SelectAdapter("warp", "terminal"); err == nil {
		t.Fatal("expected unimplemented warp error")
	}
	if _, err := SelectAdapter("unknown", "terminal"); err == nil {
		t.Fatal("expected unsupported adapter error")
	}
}

func TestTitleFromScriptUsesWorkerID(t *testing.T) {
	title := titleFromScript(filepath.Join("/tmp", "workers", "wrk_123", "run.sh"))
	if title != "PromptGrinder: wrk_123" {
		t.Fatalf("title = %q", title)
	}
}

func TestAppleScriptGenerationQuotesPathsAndAssignsOwnedTitle(t *testing.T) {
	path := filepath.Join("/tmp", "space and ünicode", "wrk_abc123", `run "quoted".sh`)
	for name, script := range map[string]string{
		"Terminal.app": terminalLaunchAppleScript(path),
		"iTerm2":       iTermLaunchAppleScript(path),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(script, `PromptGrinder: wrk_abc123`) {
				t.Fatalf("script does not contain owned worker title:\n%s", script)
			}
			if !strings.Contains(script, `space and ünicode`) || !strings.Contains(script, `run \\\"quoted\\\".sh`) {
				t.Fatalf("script does not safely preserve path:\n%s", script)
			}
		})
	}
}

func TestCloseScriptsUseStableIdentityAndBundleIdentifier(t *testing.T) {
	titles := appleScriptStringList([]string{closeAllSentinel})
	terminalScript := terminalCloseAppleScript(titles)
	if !strings.Contains(terminalScript, ownedTitlePrefix) || !strings.Contains(terminalScript, closeAllSentinel) {
		t.Fatalf("Terminal close script lacks owned identity:\n%s", terminalScript)
	}
	iTermScript := iTermCloseAppleScript(titles)
	if !strings.Contains(iTermScript, `application id "com.googlecode.iterm2"`) ||
		!strings.Contains(iTermScript, `bundle identifier is "com.googlecode.iterm2"`) ||
		!strings.Contains(iTermScript, ownedTitlePrefix) {
		t.Fatalf("iTerm close script lacks stable identity:\n%s", iTermScript)
	}
}

func TestLaunchUsesInjectableAppleScriptBoundary(t *testing.T) {
	original := runAppleScript
	defer func() { runAppleScript = original }()
	var got string
	runAppleScript = func(_ context.Context, script string) ([]byte, error) {
		got = script
		return nil, nil
	}
	path := filepath.Join(t.TempDir(), "wrk_test", "run.sh")
	if err := (TerminalAppAdapter{}).Launch(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "wrk_test") {
		t.Fatalf("executed script = %q", got)
	}
}

func TestAppleScriptFailuresAreDistinguishable(t *testing.T) {
	tests := []struct {
		name   string
		output string
		kind   AppleScriptFailureKind
	}{
		{"permission", `Not authorized to send Apple events. (-1743)`, AppleScriptPermissionDenied},
		{"syntax", `syntax error: Expected end of line. (-2741)`, AppleScriptInvalid},
		{"application timeout", `AppleEvent timed out. (-1712)`, AppleScriptTimeout},
		{"application", `Application isn't running. (-600)`, AppleScriptApplication},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := runAppleScript
			defer func() { runAppleScript = original }()
			runAppleScript = func(context.Context, string) ([]byte, error) {
				return []byte(tt.output), errors.New("osascript failed")
			}
			err := executeAppleScript("test operation", "invalid")
			var scriptErr *AppleScriptError
			if !errors.As(err, &scriptErr) || scriptErr.Kind != tt.kind {
				t.Fatalf("error = %#v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestAppleScriptContextTimeoutIsDistinguishable(t *testing.T) {
	originalRunner, originalTimeout := runAppleScript, appleScriptTimeout
	defer func() {
		runAppleScript = originalRunner
		appleScriptTimeout = originalTimeout
	}()
	appleScriptTimeout = time.Millisecond
	runAppleScript = func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	err := executeAppleScript("test operation", "script")
	var scriptErr *AppleScriptError
	if !errors.As(err, &scriptErr) || scriptErr.Kind != AppleScriptTimeout {
		t.Fatalf("error = %#v, want timeout", err)
	}
}

func TestHeadlessAdapterExecutionSuccessWritesLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "worker.log")
	scriptPath := filepath.Join(dir, "run.sh")
	script := "#!/bin/zsh\nexec >> " + shellQuote(logPath) + " 2>&1\necho hello\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := (HeadlessAdapter{}).Launch(scriptPath); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "hello") {
		t.Fatalf("logs = %q", string(logs))
	}
}

func TestHeadlessAdapterExecutionFailure(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/zsh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := (HeadlessAdapter{}).Launch(scriptPath)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if !IsExecutionError(err) {
		t.Fatalf("err = %T, want ExecutionError", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestAppleScriptStringListUsesAppleScriptSyntax(t *testing.T) {
	got := appleScriptStringList([]string{`wrk_"quoted"`, "wrk_ünicode"})
	want := `{"wrk_\"quoted\"", "wrk_ünicode"}`
	if got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}
