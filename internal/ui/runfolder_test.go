package ui

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"promptgrinder/internal/runfolder"
)

func inventory() []runfolder.ProgressPrompt {
	return []runfolder.ProgressPrompt{
		{Name: "00-spec.md", Type: runfolder.TypeSpecification, Status: "skipped"},
		{Name: "10-implement.md", Type: runfolder.TypeImplement, Status: "pending"},
		{Name: "20-test.md", Type: runfolder.TypeTest, Status: "pending"},
		{Name: "30-review.md", Type: runfolder.TypeReview, Status: "pending"},
		{Name: "40-verify.md", Type: runfolder.TypeVerify, Status: "pending"},
		{Name: "50-other.md", Type: runfolder.TypeUnknown, Status: "pending"},
	}
}

func TestRunFolderRendererPlainLifecycleFailureAndResume(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_1", Folder: "my prompts", Inventory: inventory(), Total: 6})
	r.Update(runfolder.ProgressEvent{Type: "prompt.started", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "running"})
	unsafe := false
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "failed", Duration: 65*time.Second + 600*time.Millisecond, WorkerID: "worker-1", LogPath: "/tmp/worker.log", Reason: "STATUS is BLOCKED, not PASS", CompletionStatus: "BLOCKED", NextPromptSafe: &unsafe})
	r.Finish(false)
	r.Close()
	r.Close()

	got := out.String()
	for _, want := range []string{"PromptGrinder", "Mode: foreground", "Sequence: seq_1", "Status: promptgrinder sequence seq_1", "00-spec.md [specification] - skipped", "30-review.md [review] - pending", "50-other.md [unknown] - pending", "10-implement.md [implement] - active", "failed in 1m 6s", "worker: worker-1", "log: /tmp/worker.log", "Reason: STATUS is BLOCKED, not PASS", "Completion: STATUS=BLOCKED NEXT_PROMPT_SAFE=no", "Resume: promptgrinder run-folder 'my prompts' --resume"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\r\x1b") {
		t.Fatalf("plain output contains terminal controls: %q", got)
	}
	if strings.Count(got, "Prompts:\n") != 1 {
		t.Fatalf("inventory emitted more than once: %q", got)
	}
}

func TestRunFolderRendererPrintsPreflightFailureReason(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_dirty", Folder: "tasks", Inventory: []runfolder.ProgressPrompt{{Name: "10-task.md", Type: runfolder.TypeImplement, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "10-task.md", PromptType: runfolder.TypeImplement, Status: "failed", Reason: "working tree is dirty"})
	r.Finish(false)
	if got := out.String(); !strings.Contains(got, "Reason: working tree is dirty") {
		t.Fatalf("failure reason missing from immediate output: %q", got)
	}
}

func TestRunFolderRendererResumeInventoryAndDurations(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	items := inventory()
	items[1].Status = "succeeded"
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_resume", Inventory: items})
	r.Update(runfolder.ProgressEvent{Type: "prompt.skipped", PromptName: "20-test.md", PromptType: runfolder.TypeTest, Status: "skipped", Duration: 0})
	r.Update(runfolder.ProgressEvent{Type: "prompt.succeeded", PromptName: "40-verify.md", PromptType: runfolder.TypeVerify, Status: "succeeded", Duration: 2*time.Hour + 4*time.Minute + 3*time.Second})
	r.Finish(true)
	got := out.String()
	for _, want := range []string{"10-implement.md [implement] - succeeded", "skipped in <1s", "succeeded in 2h 4m 3s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestRunFolderRendererNoColorForcesPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeLoud})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: inventory()})
	r.Close()
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatalf("NO_COLOR output contains terminal controls: %q", out.String())
	}
}

func TestRunFolderRendererUsesSharedStatusColors(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{
		{Name: "10-ok.md", Type: runfolder.TypeImplement, Status: "pending"},
		{Name: "20-bad.md", Type: runfolder.TypeTest, Status: "pending"},
	}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.succeeded", PromptName: "10-ok.md", PromptType: runfolder.TypeImplement, Status: "succeeded"})
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "20-bad.md", PromptType: runfolder.TypeTest, Status: "failed"})
	r.Close()
	got := out.String()
	if !strings.Contains(got, "\033[36m✓\033[0m") {
		t.Fatalf("success does not use minimal theme color: %q", got)
	}
	if !strings.Contains(got, "\033[31m✗\033[0m") {
		t.Fatalf("failure does not use shared red color: %q", got)
	}
}

type fakeTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *fakeTicker) Chan() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()                  { t.stopped.Store(true) }

func TestRunFolderRendererTickerLifecycleAndReplacement(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeMinimal})
	var nanos atomic.Int64
	nanos.Store(time.Unix(100, 0).UnixNano())
	r.now = func() time.Time { return time.Unix(0, nanos.Load()) }
	var mu sync.Mutex
	var tickers []*fakeTicker
	r.newTicker = func(time.Duration) rendererTicker {
		mu.Lock()
		defer mu.Unlock()
		f := &fakeTicker{ch: make(chan time.Time, 1)}
		tickers = append(tickers, f)
		return f
	}
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: inventory()})
	r.Update(runfolder.ProgressEvent{Type: "prompt.started", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement})
	nanos.Add(int64(3 * time.Second))
	tickers[0].ch <- time.Now()
	for i := 0; i < 100; i++ {
		r.mu.Lock()
		advanced := r.frame > 0
		r.mu.Unlock()
		if advanced {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r.Update(runfolder.ProgressEvent{Type: "prompt.started", PromptName: "20-test.md", PromptType: runfolder.TypeTest})
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "20-test.md", PromptType: runfolder.TypeTest, Status: "failed"})
	r.Close()
	for i, ticker := range tickers {
		if !ticker.stopped.Load() {
			t.Fatalf("ticker %d was not stopped", i)
		}
	}
	if !strings.Contains(out.String(), "3s") {
		t.Fatalf("elapsed time did not increase: %q", out.String())
	}
}

func TestRunFolderRendererConcurrentUpdatesAndClose(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: inventory()})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Update(runfolder.ProgressEvent{Type: "prompt.succeeded", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "succeeded"})
			r.Close()
		}()
	}
	wg.Wait()
}
