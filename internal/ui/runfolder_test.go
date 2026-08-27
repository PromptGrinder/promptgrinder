package ui

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"promptgrinder/internal/runfolder"
	"promptgrinder/internal/state"
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
	for _, want := range []string{"PromptGrinder", "Mode: foreground", "Sequence: seq_1", "Status: promptgrinder sequence seq_1", "Cancel: promptgrinder sequence cancel seq_1", "00-spec.md [specification] - skipped", "30-review.md [review] - pending", "50-other.md [unknown] - pending", "10-implement.md [implement] - active", "✗ [2/6] 10-implement.md|unscoped|unknown-engine/default|1m 6s", "Reason: STATUS is BLOCKED, not PASS", "Completion: STATUS=BLOCKED NEXT_PROMPT_SAFE=no", "Resume: promptgrinder run-folder 'my prompts' --resume"} {
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

func TestRunFolderRendererShowsParallelWorktreeLanes(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true})
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_parallel", Folder: "tasks", ParallelWorktrees: true, FeatureBranch: "feature/google-play", Inventory: []runfolder.ProgressPrompt{
		{Name: "01-location.pg", Type: runfolder.TypeImplement, Status: "pending", Lane: "location-policy", Priority: 1},
		{Name: "02-storage.pg", Type: runfolder.TypeImplement, Status: "pending", Lane: "deletion-storage", Priority: 2},
	}, Total: 2})
	r.Update(runfolder.ProgressEvent{Type: "prompt.started", PromptName: "01-location.pg", PromptType: runfolder.TypeImplement, Lane: "location-policy", Priority: 1, Worktree: "/tmp/lanes/location", Status: "working"})
	r.Update(runfolder.ProgressEvent{Type: "prompt.waiting-to-merge", PromptName: "02-storage.pg", PromptType: runfolder.TypeImplement, Lane: "deletion-storage", Priority: 2, Worktree: "/tmp/lanes/storage", Status: "waiting-to-merge"})
	r.Update(runfolder.ProgressEvent{Type: "prompt.succeeded", PromptName: "01-location.pg", PromptType: runfolder.TypeImplement, Lane: "location-policy", Priority: 1, Worktree: "/tmp/lanes/location", IntegrationState: "integrated", Status: "integrated"})
	r.Update(runfolder.ProgressEvent{Type: "run.completed", PRHint: "Integrated feature branch is ready for review; create a PR against your intended base: gh pr create --head feature/google-play"})
	r.Close()
	for _, want := range []string{"Mode: parallel worktrees · foreground", "Feature branch: feature/google-play", "├─ P1 01-location.pg [location-policy] - pending", "└─ P2 02-storage.pg [deletion-storage] - pending", "01-location.pg [implement] - working", "02-storage.pg [implement] - waiting-to-merge", "✓ ├─ P1 01-location.pg [location-policy] - succeeded|unscoped|unknown-engine/default|<1s · /tmp/lanes/location", "Next action: Integrated feature branch is ready for review; create a PR against your intended base: gh pr create --head feature/google-play"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunFolderRendererDistinguishesCompletedCapabilityGate(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true})
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_gate", Folder: "tasks", Inventory: []runfolder.ProgressPrompt{{Name: "10-implement-gate.md", Type: runfolder.TypeImplement, Status: "pending"}, {Name: "20-implement-dependent.md", Type: runfolder.TypeImplement, Status: "pending"}}, Total: 2})
	unsafe := false
	r.Update(runfolder.ProgressEvent{Type: "prompt.gate-blocked", PromptName: "10-implement-gate.md", PromptType: runfolder.TypeImplement, Status: "gate-blocked", WorkerID: "wrk_gate", Scope: "data-audit", Engine: "codex", Model: "gpt-5.6-terra", CompletionStatus: "BLOCKED", NextPromptSafe: &unsafe, Reason: "declared capability gate completed: product outcome BLOCKED", Total: 2, Completed: 1})
	r.Finish(true)
	got := out.String()
	for _, want := range []string{"■ [1/2] 10-implement-gate.md|data-audit|codex/gpt-5.6-terra|<1s", "Completion: STATUS=BLOCKED NEXT_PROMPT_SAFE=no", "Result: product-blocked"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Result: failed") {
		t.Fatalf("gate was rendered as an execution failure:\n%s", got)
	}
}

func TestRunFolderRendererRendersStructuredBlockedReport(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true})
	r.Update(runfolder.ProgressEvent{Type: "run.started", SequenceID: "seq_report", Folder: "tasks", Inventory: []runfolder.ProgressPrompt{{Name: "10-final-verify.pg", Type: runfolder.TypeVerify, Status: "pending"}}, Total: 1})
	unsafe := false
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "10-final-verify.pg", PromptType: runfolder.TypeVerify, Status: "failed", CompletionStatus: "BLOCKED", NextPromptSafe: &unsafe, LogPath: "/tmp/worker.log", FailureReport: &state.FailureReport{
		Category:        "environment-capability",
		Summary:         "final verification incomplete",
		FeatureEvidence: []string{"Slices 01–09: passed", "Targeted discovery behaviour: passed"},
		BlockingChecks:  []state.FailureCheck{{Summary: "Backend full gate: failed", Details: []string{"35 fixture errors"}}, {Summary: "Local catalogue import: not run", Details: []string{"admin token unavailable"}}},
		EvidenceReport:  "docs/features/example/handoffs/10-completion-report.md",
		NextAction:      "repair fixtures and configure the local token",
	}})
	r.Finish(false)
	got := out.String()
	for _, want := range []string{"Result: BLOCKED — final verification incomplete", "Failure type: environment/capability block", "Feature evidence:", "Slices 01–09: passed", "Blocking checks:", "Backend full gate: failed", "Local catalogue import: not run", "Evidence report: docs/features/example/handoffs/10-completion-report.md", "Next action: repair fixtures and configure the local token", "Resume: promptgrinder run-folder tasks --resume"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRunFolderRendererShowsAutomaticRecovery(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{{Name: "10-implement.md", Type: runfolder.TypeImplement, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.recovering", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement, Status: "recovering", RecoveryAttempt: 1, Reason: "automatic recovery attempt 1 of 1 after: launch failed"})
	r.Close()
	if got := out.String(); !strings.Contains(got, "10-implement.md [implement] - active") || !strings.Contains(got, "Reason: automatic recovery attempt 1 of 1 after: launch failed") {
		t.Fatalf("recovery output = %q", got)
	}
}

func TestRunFolderRendererRetainsKnownRecoveryIdentityAndArtifact(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{{Name: "10-test.md", Type: runfolder.TypeTest, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.failed", PromptName: "10-test.md", PromptType: runfolder.TypeTest, Status: "failed", WorkerID: "wrk_known", Scope: "android-test", Engine: "codex", Model: "gpt-5.6-terra", LogPath: "/tmp/known.log", RecoveryAttempt: 1, RecoveryArtifact: "/tmp/artifact", Reason: "resume recovery blocked"})
	r.Close()
	for _, want := range []string{"10-test.md|android-test|codex/gpt-5.6-terra|<1s", "Retained artifact: /tmp/artifact", "Reason: resume recovery blocked"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestRunFolderRendererReportsIncludedAndIgnoredMarkdown(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: inventory(), MarkdownTotal: 7, Ignored: []string{"README.md"}})
	r.Close()
	for _, want := range []string{"Preflight: 6 of 7 Markdown files included", "Ignored: README.md"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
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
	for _, want := range []string{"10-implement.md [implement] - succeeded", "skipped in <1s", "✓ [5/6] 40-verify.md|unscoped|unknown-engine/default|2h 4m 3s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestRunFolderRendererShowsAutomaticCompatibleResumePlan(t *testing.T) {
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, false, Options{Plain: true, Theme: ThemeMinimal})
	r.Update(runfolder.ProgressEvent{
		Type:       "run.started",
		SequenceID: "seq_prior",
		ResumePlan: "Compatible sequence seq_prior adopted automatically: retaining 4 successful slice(s); restarting at 30-implement.md. Use --fresh to rerun all slices.",
		Inventory:  inventory(),
	})
	r.Close()
	if got := out.String(); !strings.Contains(got, "Resume plan: Compatible sequence seq_prior adopted automatically") {
		t.Fatalf("resume plan missing from output: %q", got)
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

func TestRunFolderRendererKeepsSuccessRowCompact(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeDefault})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{{Name: "10-ok.md", Type: runfolder.TypeImplement, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{
		Type: "prompt.succeeded", PromptName: "10-ok.md", PromptType: runfolder.TypeImplement,
		Status: "succeeded", LogPath: "/tmp/worker logs/wrk_1/worker.log",
	})
	r.Close()
	got := out.String()
	if !strings.Contains(got, "10-ok.md|unscoped|unknown-engine/default|<1s") || strings.Contains(got, "/tmp/worker logs/") {
		t.Fatalf("success row is not compact: %q", got)
	}
}

func TestRunFolderRendererRedrawSurvivesWrappedRows(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeMinimal})
	longName := "10-implement-" + strings.Repeat("a", 180) + ".md"
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{{Name: longName, Type: runfolder.TypeImplement, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.succeeded", PromptName: longName, PromptType: runfolder.TypeImplement, Status: "succeeded", WorkerID: "wrk_" + strings.Repeat("b", 80), LogPath: "/tmp/worker logs/wrk_1/worker.log"})
	r.Close()
	got := out.String()
	if strings.Count(got, "\033[s") != 1 || !strings.Contains(got, "\033[u\033[J") {
		t.Fatalf("dashboard did not use save/restore redraw: %q", got)
	}
	if !strings.Contains(got, longName+"|unscoped|unknown-engine/default|<1s") {
		t.Fatalf("wrapped dashboard corrupted compact row: %q", got)
	}
}

func TestTerminalFileLinkRejectsControlCharacters(t *testing.T) {
	got := terminalFileLink("/tmp/worker\x1b]8;;https://example.invalid/worker.log")
	if got != "worker log" || strings.Contains(got, "\x1b") {
		t.Fatalf("unsafe terminal link = %q", got)
	}
}

func TestTerminalFileLinkShowsCopyableAbsolutePath(t *testing.T) {
	const relative = "worker logs/wrk_1/worker.log"
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	got := terminalFileLink(relative)
	if !strings.Contains(got, absolute) || !strings.Contains(got, "file://") {
		t.Fatalf("terminal link does not expose absolute path and file target: %q", got)
	}
}

func TestRunFolderRendererUsesSharedSpinnerFrames(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	r := NewRunFolderRenderer(&out, true, Options{Theme: ThemeDefault})
	r.Update(runfolder.ProgressEvent{Type: "run.started", Inventory: []runfolder.ProgressPrompt{{Name: "10-implement.md", Type: runfolder.TypeImplement, Status: "pending"}}})
	r.Update(runfolder.ProgressEvent{Type: "prompt.started", PromptName: "10-implement.md", PromptType: runfolder.TypeImplement})
	r.Close()
	if got := out.String(); !strings.Contains(got, "|\033[0m [1/1] 10-implement.md") || strings.Contains(got, "⠋") {
		t.Fatalf("run-folder did not use shared spinner frame: %q", got)
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
