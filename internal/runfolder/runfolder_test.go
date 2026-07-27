package runfolder

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/state"
)

type fakeLauncher struct {
	calls             []launchCall
	failName          string
	running           bool
	logDir            string
	logText           string
	onLaunch          func(path string)
	reportedSessionID string
}

type launchCall struct {
	Path      string
	Content   string
	SessionID string
}

func (f *fakeLauncher) LaunchPrompt(path, content, sessionID string) (state.Worker, error) {
	f.calls = append(f.calls, launchCall{Path: path, Content: content, SessionID: sessionID})
	if f.onLaunch != nil {
		f.onLaunch(path)
	}
	if filepath.Base(path) == f.failName {
		return state.Worker{ID: "wrk_fail", Status: state.StatusFailed}, fmt.Errorf("launch failed")
	}
	if f.running {
		return state.Worker{ID: "wrk_running", Status: state.StatusRunning}, nil
	}
	zero := 0
	worker := state.Worker{ID: "wrk_" + strings.TrimSuffix(filepath.Base(path), ".md"), Status: state.StatusSucceeded, ExitCode: &zero}
	if f.reportedSessionID != "" {
		worker.EngineResult = &state.EngineResult{SessionID: f.reportedSessionID}
	}
	if f.logDir != "" {
		worker.LogPath = filepath.Join(f.logDir, worker.ID+".log")
		if err := os.WriteFile(worker.LogPath, []byte(f.logText), 0o644); err != nil {
			return worker, err
		}
	}
	return worker, nil
}

func TestRunFolderReusesPersistedCodexSession(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "first")
	writePromptFile(t, dir, "20-implement-b.md", "second")
	launcher := &fakeLauncher{reportedSessionID: "thread-123"}

	summary, err := Run(dir, Options{HomeDir: home}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 || launcher.calls[0].SessionID != "" || launcher.calls[1].SessionID != "thread-123" {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if summary.Sequence.SessionID != "thread-123" {
		t.Fatalf("sequence session id = %q", summary.Sequence.SessionID)
	}
}

func TestDiscoverSortsMarkdownAlphabetically(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "20-implement-b.md", "b")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "notes.txt", "x")
	writePromptFile(t, dir, ".hidden.md", "hidden")

	prompts, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := names(prompts)
	want := []string{"10-implement-a.md", "20-implement-b.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompts = %v, want %v", got, want)
	}
}

func TestClassifyPromptTypes(t *testing.T) {
	cases := map[string]PromptType{
		"00-specification.md":          TypeSpecification,
		"10-implement-v1-cleanup.md":   TypeImplement,
		"30-test-benchmark.md":         TypeTest,
		"40-verify-geography.md":       TypeVerify,
		"50-review-v1-removal.md":      TypeReview,
		"90-final-verify.md":           TypeVerify,
		"99-something-unclassified.md": TypeUnknown,
	}
	for name, want := range cases {
		if got := Classify(name); got != want {
			t.Fatalf("Classify(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestDiscoverIncludesOnlyNumberedPrompts(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-custom-task.md", "task")
	writePromptFile(t, dir, "README.md", "overview")
	writePromptFile(t, dir, "notes.md", "notes")

	prompts, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].Name != "10-custom-task.md" {
		t.Fatalf("prompts = %#v", prompts)
	}
}

func TestSpecificationIsMetadataByDefaultAndContextPrepended(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "spec context")
	writePromptFile(t, dir, "10-implement-a.md", "active prompt")
	launcher := &fakeLauncher{}

	summary, err := Run(dir, Options{}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || filepath.Base(launcher.calls[0].Path) != "10-implement-a.md" {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if !strings.Contains(launcher.calls[0].Content, "# Shared Context\n\nspec context\n\n# Active Prompt\n\nactive prompt") {
		t.Fatalf("assembled prompt = %q", launcher.calls[0].Content)
	}
	if summary.Run.Status != "completed" || !contains(summary.Run.Completed, "00-specification.md") {
		t.Fatalf("summary = %#v", summary.Run)
	}
}

func TestRunEmitsProgressEvents(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "spec context")
	writePromptFile(t, dir, "10-implement-a.md", "active prompt")
	events := []ProgressEvent{}

	_, err := Run(dir, Options{Progress: func(event ProgressEvent) {
		events = append(events, event)
	}}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type+":"+event.PromptName)
	}
	got := strings.Join(types, ",")
	for _, want := range []string{"run.started:", "prompt.started:00-specification.md", "prompt.skipped:00-specification.md", "prompt.started:10-implement-a.md", "prompt.succeeded:10-implement-a.md", "run.completed:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress events = %s, missing %s", got, want)
		}
	}
}

func TestSpecificationV2IsSharedContextByDefault(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification-v2.md", "v2 spec context")
	writePromptFile(t, dir, "10-implement-a.md", "active prompt")
	launcher := &fakeLauncher{}

	summary, err := Run(dir, Options{}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || filepath.Base(launcher.calls[0].Path) != "10-implement-a.md" {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if !strings.Contains(launcher.calls[0].Content, "# Shared Context\n\nv2 spec context\n\n# Active Prompt\n\nactive prompt") {
		t.Fatalf("assembled prompt = %q", launcher.calls[0].Content)
	}
	if !contains(summary.Run.Completed, "00-specification-v2.md") {
		t.Fatalf("completed prompts = %#v", summary.Run.Completed)
	}
	if summary.Sequence.Progress().Succeeded != 2 {
		t.Fatalf("progress = %#v", summary.Sequence.Progress())
	}
}

func TestRunDoesNotMarkRunningWorkerCompleted(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "active prompt")

	summary, err := Run(dir, Options{}, &fakeLauncher{running: true})
	if err == nil || !strings.Contains(err.Error(), "has not finished yet") {
		t.Fatalf("err = %v", err)
	}
	if summary.Run.Status != "failed" || contains(summary.Run.Completed, "10-implement-a.md") {
		t.Fatalf("summary = %#v", summary.Run)
	}
	var promptState PromptState
	readJSON(t, filepath.Join(dir, ".promptgrinder", "prompts", "10-implement-a.md.json"), &promptState)
	if promptState.Status != "failed" {
		t.Fatalf("prompt state = %#v", promptState)
	}
}

func TestSharedContextPreservesActiveFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "spec context")
	writePromptFile(t, dir, "10-implement-a.md", "---\nsandbox: read-only\nworking_directory: backend\n---\nactive prompt")
	launcher := &fakeLauncher{}

	if _, err := Run(dir, Options{}, launcher); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	wantPrefix := "---\nsandbox: read-only\nworking_directory: backend\n---\n# Shared Context"
	if !strings.HasPrefix(launcher.calls[0].Content, wantPrefix) {
		t.Fatalf("assembled prompt does not preserve frontmatter prefix:\n%s", launcher.calls[0].Content)
	}
	if !strings.Contains(launcher.calls[0].Content, "# Active Prompt\n\nactive prompt") {
		t.Fatalf("assembled prompt missing active body:\n%s", launcher.calls[0].Content)
	}
}

func TestIncludeSpecificationExecutesSpecification(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "spec context")
	launcher := &fakeLauncher{}

	if _, err := Run(dir, Options{IncludeSpecification: true}, launcher); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || filepath.Base(launcher.calls[0].Path) != "00-specification.md" {
		t.Fatalf("calls = %#v", launcher.calls)
	}
}

func TestFreshCreatesStateAndDefaultResumes(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	first := &fakeLauncher{failName: "20-implement-b.md"}

	if _, err := Run(dir, Options{}, first); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(dir, ".promptgrinder", "run.json")); err != nil {
		t.Fatal(err)
	}
	second := &fakeLauncher{}
	summary, err := Run(dir, Options{}, second)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Resumed {
		t.Fatal("expected default resume")
	}
	if len(second.calls) != 1 || filepath.Base(second.calls[0].Path) != "20-implement-b.md" {
		t.Fatalf("resume calls = %#v", second.calls)
	}
}

func TestExactSequenceRerunSkipsSucceededPrompts(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	first := &fakeLauncher{}
	firstSummary, err := Run(dir, Options{HomeDir: home}, first)
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.Sequence == nil || firstSummary.Sequence.Progress().Succeeded != 2 {
		t.Fatalf("first sequence = %#v", firstSummary.Sequence)
	}

	second := &fakeLauncher{}
	secondSummary, err := Run(dir, Options{HomeDir: home}, second)
	if err != nil {
		t.Fatal(err)
	}
	if !secondSummary.Resumed || len(second.calls) != 0 {
		t.Fatalf("expected resume with no calls, summary=%#v calls=%#v", secondSummary, second.calls)
	}
	if secondSummary.Sequence.Progress().Succeeded != 2 || secondSummary.Sequence.Progress().Pending != 0 {
		t.Fatalf("progress = %#v", secondSummary.Sequence.Progress())
	}
}

func TestSequenceWithSucceededPrefixResumesAtFirstFailedPrompt(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	for i := 1; i <= 14; i++ {
		writePromptFile(t, dir, fmt.Sprintf("%02d-implement-step.md", i), fmt.Sprintf("prompt %d", i))
	}
	first := &fakeLauncher{failName: "11-implement-step.md"}
	if _, err := Run(dir, Options{HomeDir: home}, first); err == nil {
		t.Fatal("expected failure")
	}
	resume := &fakeLauncher{}
	summary, err := Run(dir, Options{HomeDir: home}, resume)
	if err != nil {
		t.Fatal(err)
	}
	got := namesFromCalls(resume.calls)
	if len(got) != 4 || got[0] != "11-implement-step.md" {
		t.Fatalf("resume calls = %v", got)
	}
	progress := summary.Sequence.Progress()
	if progress.Succeeded != 14 || progress.Pending != 0 || progress.Failed != 0 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestChangingPromptContentCreatesDifferentSequence(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	first := &fakeLauncher{}
	firstSummary, err := Run(dir, Options{HomeDir: home}, first)
	if err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, dir, "10-implement-a.md", "changed")
	second := &fakeLauncher{}
	secondSummary, err := Run(dir, Options{HomeDir: home}, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.Sequence.SequenceID == secondSummary.Sequence.SequenceID {
		t.Fatalf("sequence id did not change: %s", firstSummary.Sequence.SequenceID)
	}
	if len(second.calls) != 1 {
		t.Fatalf("changed sequence did not run prompt: %#v", second.calls)
	}
}

func TestEngineOverrideParticipatesInSequenceIdentity(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")

	firstSummary, err := Run(dir, Options{HomeDir: home, EngineOverride: "codex"}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	secondSummary, err := Run(dir, Options{HomeDir: home, EngineOverride: "other"}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.Sequence.SequenceID == secondSummary.Sequence.SequenceID {
		t.Fatalf("sequence id did not change: %s", firstSummary.Sequence.SequenceID)
	}
	if firstSummary.Sequence.Engine != "codex" || secondSummary.Sequence.Engine != "other" {
		t.Fatalf("sequence engines = %q %q", firstSummary.Sequence.Engine, secondSummary.Sequence.Engine)
	}
}

func TestUnsupportedDefaultEngineFailsBeforeSequenceCreation(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writeConfig(t, home, "engine:\n  default: codex\n")

	first := &fakeLauncher{}
	_, err := Run(dir, Options{HomeDir: home}, first)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, "engine:\n  default: other\n")
	second := &fakeLauncher{}
	if _, err := Run(dir, Options{HomeDir: home}, second); err == nil || !strings.Contains(err.Error(), "engine.default") {
		t.Fatalf("error = %v", err)
	}
	if len(second.calls) != 0 {
		t.Fatalf("invalid engine must fail before launch, calls=%#v", second.calls)
	}
}

func TestCodexEngineConfigParticipatesInSequenceIdentity(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writeConfig(t, home, "engine:\n  default: codex\n  codex:\n    sandbox: workspace-write\n    approval: never\n")

	firstSummary, err := Run(dir, Options{HomeDir: home}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, "engine:\n  default: codex\n  codex:\n    sandbox: read-only\n    approval: never\n")
	second := &fakeLauncher{}
	secondSummary, err := Run(dir, Options{HomeDir: home}, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.Sequence.SequenceID == secondSummary.Sequence.SequenceID {
		t.Fatalf("sequence id did not change: %s", firstSummary.Sequence.SequenceID)
	}
	if len(second.calls) != 1 {
		t.Fatalf("changed codex config should rerun prompt, calls=%#v", second.calls)
	}
}

func TestRestartRunsAllPromptsAgain(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	if _, err := Run(dir, Options{HomeDir: home}, &fakeLauncher{}); err != nil {
		t.Fatal(err)
	}
	restart := &fakeLauncher{}
	summary, err := Run(dir, Options{HomeDir: home, Restart: true}, restart)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Resumed || len(restart.calls) != 2 {
		t.Fatalf("summary=%#v calls=%#v", summary, restart.calls)
	}
}

func TestFreshIgnoresPreviousState(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	if _, err := Run(dir, Options{}, &fakeLauncher{failName: "20-implement-b.md"}); err == nil {
		t.Fatal("expected failure")
	}
	fresh := &fakeLauncher{}
	summary, err := Run(dir, Options{Fresh: true}, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Resumed || len(fresh.calls) != 2 {
		t.Fatalf("summary=%#v calls=%#v", summary, fresh.calls)
	}
}

func TestResumeFreshMutuallyExclusive(t *testing.T) {
	_, err := Run(t.TempDir(), Options{Resume: true, Fresh: true}, &fakeLauncher{})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopOnFailureAndRetryFailedOnResume(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	writePromptFile(t, dir, "30-test-c.md", "c")
	if _, err := Run(dir, Options{}, &fakeLauncher{failName: "20-implement-b.md"}); err == nil {
		t.Fatal("expected failure")
	}
	resume := &fakeLauncher{}
	if _, err := Run(dir, Options{Resume: true}, resume); err != nil {
		t.Fatal(err)
	}
	got := namesFromCalls(resume.calls)
	want := []string{"20-implement-b.md", "30-test-c.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resume calls = %v, want %v", got, want)
	}
}

func TestUnknownPromptRunsWithWarning(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-custom.md", "custom")
	summary, err := Run(dir, Options{}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0], "unknown prompt type") {
		t.Fatalf("warnings = %#v", summary.Warnings)
	}
}

func TestCurrentSequenceReturnsNewestEvenWhenOlderSequenceIsRunning(t *testing.T) {
	home := t.TempDir()
	store := newSequenceStore(home)
	oldTime := time.Now().UTC().Add(-24 * time.Hour)
	newTime := time.Now().UTC()
	old := SequenceState{
		SequenceID: "seq_old",
		Status:     "running",
		UpdatedAt:  &oldTime,
		Items:      []SequenceItem{{PromptName: "10-old.md", Status: "pending"}},
	}
	newest := SequenceState{
		SequenceID: "seq_new",
		Status:     "completed",
		UpdatedAt:  &newTime,
		Items:      []SequenceItem{{PromptName: "10-new.md", Status: "succeeded"}},
	}
	if err := store.save(old); err != nil {
		t.Fatal(err)
	}
	if err := store.save(newest); err != nil {
		t.Fatal(err)
	}

	current, err := CurrentSequence(home)
	if err != nil {
		t.Fatal(err)
	}
	if current.SequenceID != newest.SequenceID {
		t.Fatalf("current = %s, want %s", current.SequenceID, newest.SequenceID)
	}
}

func TestSequenceProgressDistinguishesRunningFromNext(t *testing.T) {
	sequence := SequenceState{Items: []SequenceItem{
		{PromptName: "10-done.md", Status: "succeeded"},
		{PromptName: "20-running.md", Status: "running"},
		{PromptName: "30-next.md", Status: "pending"},
	}}
	progress := sequence.Progress()
	if progress.Current != "20-running.md" || progress.Next != "30-next.md" {
		t.Fatalf("progress = %#v", progress)
	}

	sequence.Items[1].Status = "succeeded"
	progress = sequence.Progress()
	if progress.Current != "" || progress.Next != "30-next.md" {
		t.Fatalf("pending progress = %#v", progress)
	}
}

func TestReconcileSequencesMarksAbandonedPendingSequenceInterrupted(t *testing.T) {
	home := t.TempDir()
	store := newSequenceStore(home)
	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	sequence := SequenceState{
		SequenceID: "seq_stale",
		Status:     "running",
		UpdatedAt:  &oldTime,
		Items: []SequenceItem{
			{PromptName: "10-done.md", Status: "succeeded"},
			{PromptName: "README.md", Status: "pending"},
		},
	}
	if err := store.save(sequence); err != nil {
		t.Fatal(err)
	}

	stale, err := ReconcileSequences(home, 24*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].Status != "interrupted" {
		t.Fatalf("stale = %#v", stale)
	}
	if stale[0].Items[1].Status != "interrupted" {
		t.Fatalf("items = %#v", stale[0].Items)
	}
}

func TestReconcileSequencesPreservesHealthySupervisor(t *testing.T) {
	home := t.TempDir()
	store := newSequenceStore(home)
	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	now := time.Now().UTC()
	supervisor := &Supervisor{ID: "sup_live", PID: 123, Status: "running", HeartbeatAt: &now}
	sequence := SequenceState{
		SequenceID: "seq_live",
		Status:     "running",
		UpdatedAt:  &oldTime,
		Supervisor: supervisor,
		Items:      []SequenceItem{{PromptName: "10-running.md", Status: "running"}},
	}
	if err := store.save(sequence); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "state", "supervisors")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(supervisor)
	if err := os.WriteFile(filepath.Join(root, "sup_live.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := ReconcileSequences(home, 24*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %#v", stale)
	}
}

func TestRunPersistsSupervisorLifecycleAndSequenceEvents(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")

	summary, err := Run(dir, Options{
		HomeDir:           home,
		SupervisorID:      "sup_test",
		SupervisorLogPath: "/tmp/supervisor.log",
	}, &fakeLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sequence.Supervisor == nil || summary.Sequence.Supervisor.Status != "completed" {
		t.Fatalf("supervisor = %#v", summary.Sequence.Supervisor)
	}
	recordPath := filepath.Join(home, "state", "supervisors", "sup_test.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var supervisor Supervisor
	if err := json.Unmarshal(data, &supervisor); err != nil {
		t.Fatal(err)
	}
	if supervisor.Status != "completed" || supervisor.FinishedAt == nil {
		t.Fatalf("supervisor record = %#v", supervisor)
	}
	eventPath := filepath.Join(home, "events", "sequences", summary.Sequence.SequenceID+".jsonl")
	events, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"run.started"`, `"type":"prompt.started"`, `"type":"prompt.succeeded"`, `"type":"run.completed"`} {
		if !strings.Contains(string(events), want) {
			t.Fatalf("events = %s, missing %s", events, want)
		}
	}
}

func TestSequenceSummaryIncludesTokenUsageAndExecutiveSummary(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	logDir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "spec context")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")

	summary, err := Run(dir, Options{HomeDir: home}, &fakeLauncher{logDir: logDir, logText: "total tokens: 1,234\n"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sequence == nil {
		t.Fatal("missing sequence")
	}
	if !summary.Sequence.TokenUsage.Available || summary.Sequence.TokenUsage.Total != 2468 {
		t.Fatalf("token usage = %#v", summary.Sequence.TokenUsage)
	}
	if !strings.Contains(summary.Sequence.ExecutiveSummary, "10-implement-a.md succeeded") || !strings.Contains(summary.Sequence.ExecutiveSummary, "00-specification.md was used as shared context") {
		t.Fatalf("executive summary = %q", summary.Sequence.ExecutiveSummary)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".promptgrinder", "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Total tokens used: 2468") || !strings.Contains(string(data), "Executive Summary") {
		t.Fatalf("summary.md = %q", string(data))
	}
	globalData, err := os.ReadFile(filepath.Join(home, "summaries", summary.Sequence.SequenceID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(globalData), "Total tokens used: 2468") {
		t.Fatalf("global summary = %q", string(globalData))
	}
}

func TestParseTokenUsage(t *testing.T) {
	usage := parseTokenUsage("total_tokens: 100\nTokens used: 25\nused 5 tokens\n")
	if !usage.Available || usage.Total != 130 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestCheckpointRecordsGitMetadata(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "a")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	launcher := &fakeLauncher{onLaunch: func(path string) {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "changed.txt"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := Run(dir, Options{RepoPath: dir, Checkpoint: true}, launcher); err != nil {
		t.Fatal(err)
	}
	var promptState PromptState
	readJSON(t, filepath.Join(dir, ".promptgrinder", "prompts", "10-implement-a.md.json"), &promptState)
	if promptState.GitSHABefore == "" || promptState.GitSHAAfter == "" || !contains(promptState.FilesChanged, "changed.txt") {
		t.Fatalf("prompt state = %#v", promptState)
	}
}

func TestRepoPathControlsGitMetadataForExternalPromptFolder(t *testing.T) {
	promptDir := t.TempDir()
	repo := initGitRepo(t)
	writePromptFile(t, promptDir, "10-implement-a.md", "a")
	writePromptFile(t, repo, "tracked.txt", "initial")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	launcher := &fakeLauncher{onLaunch: func(path string) {
		if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}

	if _, err := Run(promptDir, Options{RepoPath: repo, Checkpoint: true}, launcher); err != nil {
		t.Fatal(err)
	}
	var promptState PromptState
	readJSON(t, filepath.Join(promptDir, ".promptgrinder", "prompts", "10-implement-a.md.json"), &promptState)
	if promptState.GitSHABefore == "" || promptState.GitSHAAfter == "" || !contains(promptState.FilesChanged, "changed.txt") {
		t.Fatalf("prompt state = %#v", promptState)
	}
}

func TestCommitEachCommitsOnlyWhenThereAreChanges(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	launcher := &fakeLauncher{onLaunch: func(path string) {
		if filepath.Base(path) == "10-implement-a.md" {
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "changed.txt"), []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, filepath.Dir(path), "add", "changed.txt")
		}
	}}
	if _, err := Run(dir, Options{RepoPath: dir, Checkpoint: true, CommitEach: true}, launcher); err != nil {
		t.Fatal(err)
	}
	var first PromptState
	readJSON(t, filepath.Join(dir, ".promptgrinder", "prompts", "10-implement-a.md.json"), &first)
	var second PromptState
	readJSON(t, filepath.Join(dir, ".promptgrinder", "prompts", "20-implement-b.md.json"), &second)
	if first.CommitSHA == "" {
		t.Fatalf("first prompt was not committed: %#v", first)
	}
	if second.CommitSHA != "" {
		t.Fatalf("second prompt should not commit without changes: %#v", second)
	}
}

func TestRequireCleanGitFailsWhenDirty(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "a")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	writePromptFile(t, dir, "dirty.txt", "dirty")

	_, err := Run(dir, Options{RepoPath: dir, RequireCleanGit: true}, &fakeLauncher{})
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("err = %v", err)
	}
}

func writePromptFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(prompts []Prompt) []string {
	out := []string{}
	for _, prompt := range prompts {
		out = append(out, prompt.Name)
	}
	return out
}

func namesFromCalls(calls []launchCall) []string {
	out := []string{}
	for _, call := range calls {
		out = append(out, filepath.Base(call.Path))
	}
	return out
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.name", "Test User")
	git(t, dir, "config", "user.email", "test@example.invalid")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
