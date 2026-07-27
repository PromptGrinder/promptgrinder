package markdown

import "testing"

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
