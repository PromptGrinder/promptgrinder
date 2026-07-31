package markdown

import (
	"strings"
	"testing"
)

func TestParseMarkdownWithoutFrontmatter(t *testing.T) {
	task, err := Parse("# Task\n\nKeep formatting.\n")
	if err != nil {
		t.Fatal(err)
	}

	if len(task.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty", task.Metadata)
	}
	if task.Body != "# Task\n\nKeep formatting.\n" {
		t.Fatalf("body = %q", task.Body)
	}
}

func TestParseMarkdownFrontmatter(t *testing.T) {
	task, err := Parse(`---
engine:
  name: codex
  profile: backend
priority: high
dry_run: true
labels:
  - local
  - mvp
---
# Task
`)
	if err != nil {
		t.Fatal(err)
	}

	engine, ok := task.Metadata["engine"].(map[string]any)
	if !ok || engine["name"] != "codex" || engine["profile"] != "backend" {
		t.Fatalf("engine = %#v", task.Metadata["engine"])
	}
	if task.Metadata["dry_run"] != true {
		t.Fatalf("dry_run = %#v", task.Metadata["dry_run"])
	}
	labels, ok := task.Metadata["labels"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "local" || labels[1] != "mvp" {
		t.Fatalf("labels = %#v", task.Metadata["labels"])
	}
	if task.Body != "# Task\n" {
		t.Fatalf("body = %q", task.Body)
	}
}

func TestParseRejectsUnclosedFrontmatter(t *testing.T) {
	_, err := Parse("---\nengine: codex\n# Task\n")
	if err == nil {
		t.Fatal("expected malformed frontmatter error")
	}
}

func TestContractRendersSemanticsInStableOrderAndPreservesBody(t *testing.T) {
	body := "# Task\r\n\r\nKeep  spaces & $HOME; `echo nope`.\r\n"
	task, err := Parse("---\nvalidation:\n  - go test ./...\nacceptance_criteria: It works\nforbidden_paths: []\nallowed_paths:\n  - internal/**\n---\n" + body)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(task, "tasks/example.md"); err != nil {
		t.Fatal(err)
	}
	want := "# Task Semantics (v1)\n\n## Acceptance Criteria\n\n- It works\n\n## Allowed Paths\n\n- internal/**\n\n## Forbidden Paths\n\n\n## Validation\n\n- go test ./...\n\n" + body
	if got := string(Render(task)); got != want {
		t.Fatalf("rendered prompt:\n%q\nwant:\n%q", got, want)
	}
}

func TestContractRejectsUnsupportedAndMalformedValues(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"unknown", "mystery: true", `unknown top-level key "mystery"`},
		{"nested", "engine:\n  mystery: true", `unknown nested key "engine.mystery"`},
		{"depends", "depends_on: task-a", "depends_on is not supported"},
		{"criteria type", "acceptance_criteria: true", "must be a string or nonempty list"},
		{"empty validation", "validation: []", "must be a string or nonempty list"},
		{"null paths", "allowed_paths:", "must be a list of repository-relative patterns"},
		{"absolute", "allowed_paths: [/tmp/**]", "must be repository-relative"},
		{"escape", "allowed_paths: [../outside/**]", "must not escape"},
		{"conflict", "allowed_paths: [src/**]\nforbidden_paths: [src/**]", "conflicting path rule"},
		{"secret", "validation: API_TOKEN=super-secret-value", "secret-looking data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, err := Parse("---\n" + test.yaml + "\n---\nbody")
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(task, "tasks/bad.md")
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "tasks/bad.md") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestParseRejectsYAMLAliases(t *testing.T) {
	_, err := Parse("---\nacceptance_criteria: &criteria works\nvalidation: *criteria\n---\nbody")
	if err == nil || !strings.Contains(err.Error(), "aliases and anchors") {
		t.Fatalf("err = %v", err)
	}
}

func TestLargeFrontmatterWarningBoundaries(t *testing.T) {
	base := Task{Body: strings.Repeat("b", 256), RawFrontmatter: strings.Repeat("x", 2048)}
	if got := Warnings(base); len(got) != 1 {
		t.Fatalf("warnings at boundary = %#v", got)
	}
	base.RawFrontmatter = strings.Repeat("x", 2047)
	if got := Warnings(base); len(got) != 0 {
		t.Fatalf("warnings below raw boundary = %#v", got)
	}
	base.RawFrontmatter = strings.Repeat("x", 4096)
	base.Body = strings.Repeat("b", 257)
	if got := Warnings(base); len(got) != 0 {
		t.Fatalf("warnings above payload boundary = %#v", got)
	}
}
