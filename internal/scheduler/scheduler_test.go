package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverMarkdownFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	write := func(path string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("# Task\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b-task.md")
	write("a-task.md")
	write("notes.txt")
	write(".hidden.md")
	write(".hidden-dir/c-task.md")
	write("nested/d-task.md")

	files, err := DiscoverMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "a-task.md"),
		filepath.Join(root, "b-task.md"),
		filepath.Join(root, "nested", "d-task.md"),
	}
	if len(files) != len(want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files = %#v, want %#v", files, want)
		}
	}
}
