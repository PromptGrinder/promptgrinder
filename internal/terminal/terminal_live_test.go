//go:build darwin && integration

package terminal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests cross the real AppleScript/application boundary. They require the
// integration build tag and an explicit environment opt-in, and use a unique
// wrk_ title so cleanup cannot select unrelated tabs.
func requireLiveTerminal(t *testing.T) {
	t.Helper()
	if os.Getenv("PROMPTGRINDER_LIVE_TERMINAL") != "1" {
		t.Skip("live macOS terminal smoke test; set PROMPTGRINDER_LIVE_TERMINAL=1")
	}
}

func TestLiveTerminalAppLaunchAndTargetedClose(t *testing.T) {
	requireLiveTerminal(t)
	liveLaunchAndClose(t, TerminalAppAdapter{})
}

func TestLiveITermLaunchAndTargetedClose(t *testing.T) {
	requireLiveTerminal(t)
	if !pathExists("/Applications/iTerm.app") && !pathExists("/Applications/iTerm2.app") {
		t.Skip("iTerm2 is not installed")
	}
	liveLaunchAndClose(t, ITermAdapter{})
}

func liveLaunchAndClose(t *testing.T, adapter TerminalAdapter) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "space ünicode", "wrk_live_"+strings.ToLower(adapter.Name()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "completed")
	script := filepath.Join(dir, `run "quoted".sh`)
	body := "#!/bin/zsh\nprint -r -- ok > " + shellQuote(marker) + "\n/bin/sleep 2\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	title := titleFromScript(script)
	t.Cleanup(func() {
		_ = CloseTerminalTabs([]string{title})
	})
	if err := adapter.Launch(script); err != nil {
		t.Fatalf("%s live launch: %v", adapter.Name(), err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not execute disposable script", adapter.Name())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := CloseTerminalTabs([]string{title}); err != nil {
		t.Fatalf("%s targeted close: %v", adapter.Name(), err)
	}
	t.Logf("live result: macOS=%s architecture=%s adapter=%s title=%s", runtime.GOOS, runtime.GOARCH, adapter.Name(), title)
}
