package workeradapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"promptgrinder/internal/testsupport"
	"promptgrinder/internal/workerlaunch"
)

func TestAntigravityAdapterBuildsDocumentedHeadlessCommand(t *testing.T) {
	fake := testsupport.FakeExecutable(t, "agy", "#!/bin/sh\nexit 0\n")
	var gotExecutable, gotDir string
	var gotArgs []string
	adapter := Antigravity{
		Executable: fake,
		Run: func(_ context.Context, executable string, args []string, dir string) ([]byte, int, error) {
			gotExecutable, gotArgs, gotDir = executable, append([]string(nil), args...), dir
			return []byte(`{"response":"done","error":null}`), 123, nil
		},
	}
	request := antigravityRequest(t)
	request.Runtime.Options = map[string]any{
		"model": "gemini-test", "effort": "low", "mode": "plan",
		"sandbox": true, "print_timeout": "2m",
	}
	if err := adapter.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Launch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if gotExecutable != fake || gotDir != request.Worktree {
		t.Fatalf("executable/dir = %q/%q", gotExecutable, gotDir)
	}
	want := []string{
		"--print", "--output-format", "json", "--disable-slash-commands",
		"--model", "gemini-test", "--effort", "low", "--mode", "plan",
		"--print-timeout", "2m", "--sandbox=true", request.Context,
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args\n got: %#v\nwant: %#v", gotArgs, want)
	}
	if result.RuntimeName != "antigravity" || !strings.HasPrefix(result.RunID, "agy_") ||
		result.Process.PID != 123 || result.Process.FinishedAt == nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestAntigravityAdapterRejectsUnknownOptionsAndMalformedOutput(t *testing.T) {
	fake := testsupport.FakeExecutable(t, "agy", "#!/bin/sh\nexit 0\n")
	request := antigravityRequest(t)
	request.Runtime.Options = map[string]any{"invented": true}
	if err := (Antigravity{Executable: fake}).Preflight(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
	request.Runtime.Options = nil
	adapter := Antigravity{Executable: fake, Run: func(context.Context, string, []string, string) ([]byte, int, error) {
		return []byte("not-json"), 1, nil
	}}
	if _, err := adapter.Launch(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "parse Antigravity JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeAdaptersShareContract(t *testing.T) {
	for name, launcher := range map[string]workerlaunch.Launcher{
		"codex": Codex{}, "antigravity": Antigravity{},
	} {
		t.Run(name, func(t *testing.T) {
			provider, ok := launcher.(workerlaunch.CapabilityProvider)
			if !ok {
				t.Fatal("adapter does not advertise capabilities")
			}
			caps := provider.Capabilities()
			if !caps.Headless || !caps.StructuredOutput || !caps.WorkingDirectory {
				t.Fatalf("capabilities = %#v", caps)
			}
			if err := workerlaunch.Negotiate(launcher, workerlaunch.Capabilities{
				Headless: true, StructuredOutput: true, WorkingDirectory: true,
			}); err != nil {
				t.Fatalf("default named-worker contract: %v", err)
			}
			preflight, ok := launcher.(workerlaunch.Preflighter)
			if !ok {
				t.Fatal("adapter does not implement process-free preflight")
			}
			request := workerlaunch.LaunchRequest{Runtime: workerlaunch.RuntimeConfig{Name: "wrong-runtime"}}
			if err := preflight.Preflight(context.Background(), request); err == nil {
				t.Fatal("adapter accepted a request for another runtime")
			}
		})
	}
	if (Antigravity{}).Capabilities().SessionResume {
		t.Fatal("Antigravity must not advertise undocumented headless session ID capture")
	}
}

func antigravityRequest(t *testing.T) workerlaunch.LaunchRequest {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return workerlaunch.LaunchRequest{
		Repository: repo, Worktree: repo,
		Runtime: workerlaunch.RuntimeConfig{Name: "antigravity"},
		Context: "# PromptGrinder Worker Context\n\nDo the assigned task.\n",
	}
}
