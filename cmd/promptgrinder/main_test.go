package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCompiledCLIValidateRenderMatchesFakeCodexPrompt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "promptgrinder")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(os.TempDir(), "promptgrinder-go-cache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", repo)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	task := filepath.Join(repo, "task with spaces.md")
	content := "---\nacceptance_criteria: preserve spaces\nallowed_paths: [src/**]\nforbidden_paths: []\nvalidation: printf '%s\\n' '$HOME; *'\n---\nBody  with spaces & metacharacters: $HOME; `nope`\n"
	if err := os.WriteFile(task, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	capture := filepath.Join(dir, "captured prompt")
	fake := filepath.Join(dir, "fake codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.150.1'; exit 0; fi\nlast=''\nfor arg in \"$@\"; do last=$arg; done\nprintf %s \"$last\" > \"" + capture + "\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"fake-session\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"STATUS: PASS\\nNEXT_PROMPT_SAFE: yes\"}}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "engine:\n  default: codex\n  codex:\n    executable: " + fake + "\nterminal:\n  adapter: headless\n  mode: normal\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PROMPTGRINDER_HOME="+home, "PROMPTGRINDER_PLAIN=1")

	render := exec.Command(bin, "validate", "--render", task)
	render.Env = env
	rendered, err := render.Output()
	if err != nil {
		t.Fatalf("validate --render: %v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("validation invoked Codex: %v", err)
	}
	dry := exec.Command(bin, "run", task, "--dry-run", "--plain")
	dry.Env = env
	if output, err := dry.CombinedOutput(); err != nil {
		t.Fatalf("dry run: %v\n%s", err, output)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("dry-run invoked Codex: %v", err)
	}
	run := exec.Command(bin, "run", task, "--plain")
	run.Env = env
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("real fake-Codex run: %v\n%s", err, output)
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != string(rendered) {
		t.Fatalf("captured prompt = %q\nrendered prompt = %q", captured, rendered)
	}
}
