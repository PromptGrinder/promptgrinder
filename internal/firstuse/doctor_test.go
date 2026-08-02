package firstuse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDoctorCheckSeveritiesAndReadOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	exe := fakeExecutable(t, "promptgrinder")
	tool := fakeExecutable(t, "tool")
	lookPath := func(name string) (string, error) {
		if name == "codex" {
			return "", errors.New("missing")
		}
		return tool, nil
	}
	report := Doctor(context.Background(), DoctorOptions{
		HomeDir: home, Terminal: "headless", GOOS: "darwin", GOARCH: "arm64",
		Executable: exe, LookPath: lookPath,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		},
	})
	if report.OK {
		t.Fatal("missing Codex should fail a required check")
	}
	assertCheck(t, report, "tool.codex", Fail)
	assertCheck(t, report, "codex.readiness", Skipped)
	assertCheck(t, report, "repo.git", Skipped)
	assertCheck(t, report, "terminal.active_launch", Skipped)
	assertCheck(t, report, "terminal.available.headless", Pass)
	assertCheck(t, report, "tool.antigravity", Pass)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("doctor created home: %v", err)
	}
}

func TestDoctorTreatsMissingOptionalRuntimeAsInventoryWarning(t *testing.T) {
	home := t.TempDir()
	exe := fakeExecutable(t, "promptgrinder")
	tool := fakeExecutable(t, "tool")
	report := Doctor(context.Background(), DoctorOptions{
		HomeDir: home, Terminal: "headless", GOOS: "darwin", GOARCH: "arm64", Executable: exe,
		LookPath: func(name string) (string, error) {
			if name == "agy" {
				return "", errors.New("missing")
			}
			return tool, nil
		},
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"--version"}) {
				return []byte("codex-cli 1.2.3"), nil
			}
			return []byte("logged in"), nil
		},
	})
	check := findCheck(t, report, "tool.antigravity")
	if check.Status != Warning || check.Required || !report.OK {
		t.Fatalf("optional runtime check = %#v, report = %#v", check, report)
	}
}

func TestDoctorUnknownReadinessFailsPreflight(t *testing.T) {
	home := t.TempDir()
	exe := fakeExecutable(t, "promptgrinder")
	tool := fakeExecutable(t, "tool")
	report := Doctor(context.Background(), DoctorOptions{
		HomeDir: home, Terminal: "headless", GOOS: "darwin", GOARCH: "amd64",
		Executable: exe,
		LookPath:   func(string) (string, error) { return tool, nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"--version"}) {
				return []byte("codex-cli 1.2.3"), nil
			}
			if reflect.DeepEqual(args, []string{"login", "status"}) {
				return []byte("not logged in"), errors.New("exit 1")
			}
			return []byte("ok"), nil
		},
	})
	if report.OK {
		t.Fatalf("unknown readiness must fail doctor: %#v", report)
	}
	assertCheck(t, report, "codex.readiness", Fail)
}

func TestDoctorMalformedConfigIdentifiesSources(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("terminal: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := fakeExecutable(t, "promptgrinder")
	report := Doctor(context.Background(), DoctorOptions{
		HomeDir: home, Terminal: "headless", GOOS: "darwin", GOARCH: "arm64",
		Executable: exe, LookPath: func(string) (string, error) { return "", errors.New("missing") },
	})
	check := findCheck(t, report, "config.effective")
	if check.Status != Fail || check.Evidence["sources"] == nil {
		t.Fatalf("config check = %#v", check)
	}
}

func TestDoctorRejectsHomeBelowNonDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := checkHome(filepath.Join(parent, "home"))
	if check.Status != Fail || !check.Required {
		t.Fatalf("home check = %#v, want required failure", check)
	}
}

func TestDoctorPrintsShellSpecificPathRemediation(t *testing.T) {
	for _, test := range []struct {
		name, shell, profile, command string
	}{
		{name: "zsh", shell: "/bin/zsh", profile: "~/.zshrc", command: "source ~/.zshrc"},
		{name: "bash", shell: "/bin/bash", profile: "~/.bash_profile", command: "source ~/.bash_profile"},
		{name: "fish", shell: "/opt/homebrew/bin/fish", profile: "~/.config/fish/config.fish", command: "fish_add_path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			exe := fakeExecutable(t, "promptgrinder")
			check := checkPromptGrinderPath(DoctorOptions{Shell: test.shell, LookPath: func(string) (string, error) {
				return "", errors.New("missing")
			}}, exe)
			if check.Status != Warning || check.Evidence["profile"] != test.profile || !strings.Contains(check.Remediation, test.command) || !strings.Contains(check.Remediation, filepath.Dir(exe)) {
				t.Fatalf("check = %#v", check)
			}
			if strings.Contains(check.Remediation, "promptgrinder doctor >>") {
				t.Fatalf("unsafe remediation: %q", check.Remediation)
			}
		})
	}
}

func TestDoctorPathCheckPassesWhenCommandIsDiscoverable(t *testing.T) {
	check := checkPromptGrinderPath(DoctorOptions{LookPath: func(string) (string, error) {
		return "/usr/local/bin/promptgrinder", nil
	}}, "/some/other/promptgrinder")
	if check.Status != Pass || check.Remediation != "" {
		t.Fatalf("check = %#v", check)
	}
}

func TestRedaction(t *testing.T) {
	input := "OPENAI_API_KEY=supersecret /Users/me/.codex/auth.json ordinary"
	got := RedactText(input)
	if strings.Contains(got, "supersecret") || strings.Contains(got, "auth.json") || !strings.Contains(got, "ordinary") {
		t.Fatalf("redaction = %q", got)
	}
	for _, key := range []string{"MY_TOKEN", "password", "auth_cookie", "credential_path"} {
		if !IsSecretKey(key) {
			t.Fatalf("%q should be secret-looking", key)
		}
	}
}

func fakeExecutable(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertCheck(t *testing.T, report DoctorReport, id string, status Status) {
	t.Helper()
	check := findCheck(t, report, id)
	if check.Status != status {
		t.Fatalf("%s status = %s, want %s (%#v)", id, check.Status, status, check)
	}
}

func findCheck(t *testing.T, report DoctorReport, id string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("missing check %s", id)
	return Check{}
}
