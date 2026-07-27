package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	pgruntime "promptgrinder/internal/runtime"
)

func TestSharedProgressRendererPlainLifecycle(t *testing.T) {
	out := &bytes.Buffer{}
	renderer := NewSharedProgressRenderer(out, false, Options{Plain: true})
	progress := pgruntime.SharedRunProgress{Index: 2, Total: 3, TaskPath: "/prompts/02B-task.md"}

	progress.Status = pgruntime.SharedRunStarted
	renderer.Update(progress)
	progress.Status = pgruntime.SharedRunSucceeded
	progress.Duration = 4*time.Minute + 39*time.Second
	renderer.Update(progress)
	renderer.Close()

	got := out.String()
	for _, want := range []string{
		"[2/3] 02B-task.md - In progress",
		"[2/3] 02B-task.md - Completed successfully in 4m 39s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, missing %q", got, want)
		}
	}
}

func TestSharedProgressRendererPlainFailure(t *testing.T) {
	out := &bytes.Buffer{}
	renderer := NewSharedProgressRenderer(out, false, Options{Plain: true})
	renderer.Update(pgruntime.SharedRunProgress{
		Index: 1, Total: 2, TaskPath: "01-task.md", Status: pgruntime.SharedRunFailed,
		Duration: 12*time.Second + 600*time.Millisecond,
	})

	if got := out.String(); !strings.Contains(got, "[1/2] 01-task.md - Failed in 13s") {
		t.Fatalf("output = %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, test := range []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Millisecond, "<1s"},
		{59 * time.Second, "59s"},
		{4*time.Minute + 39*time.Second, "4m 39s"},
		{2*time.Hour + 3*time.Minute + 4*time.Second, "2h 3m 4s"},
	} {
		if got := formatDuration(test.in); got != test.want {
			t.Fatalf("formatDuration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}
