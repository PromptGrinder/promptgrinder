package runfolder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/markdown"
	"promptgrinder/internal/state"
	"promptgrinder/internal/workerpathpolicy"
)

type fakeLauncher struct {
	calls             []launchCall
	failName          string
	running           bool
	logDir            string
	logText           string
	onLaunch          func(path string)
	reportedSessionID string
	result            *state.EngineResult
}

type launchCall struct {
	Path      string
	Content   string
	SessionID string
}

type recordingNotifier struct{ notifications []Notification }

func (n *recordingNotifier) Notify(notification Notification) error {
	n.notifications = append(n.notifications, notification)
	return nil
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
	nextSafe := true
	worker := state.Worker{ID: "wrk_" + strings.TrimSuffix(filepath.Base(path), ".md"), Status: state.StatusSucceeded, ExitCode: &zero, EngineResult: &state.EngineResult{Summary: "done\nSTATUS: PASS\nNEXT_PROMPT_SAFE: yes", CompletionStatus: "PASS", NextPromptSafe: &nextSafe}}
	if f.result != nil {
		copy := *f.result
		worker.EngineResult = &copy
	}
	if f.reportedSessionID != "" {
		worker.EngineResult.SessionID = f.reportedSessionID
	}
	if f.logDir != "" {
		worker.LogPath = filepath.Join(f.logDir, worker.ID+".log")
		if err := os.WriteFile(worker.LogPath, []byte(f.logText), 0o644); err != nil {
			return worker, err
		}
	}
	return worker, nil
}

func TestOrderedCompletionContractStopsUnsafeResults(t *testing.T) {
	cases := []struct {
		name   string
		result *state.EngineResult
		reason string
	}{
		{name: "blocked", result: completionResult("BLOCKED", false), reason: "STATUS is BLOCKED"},
		{name: "partial", result: completionResult("PARTIAL", false), reason: "STATUS is PARTIAL"},
		{name: "missing status", result: &state.EngineResult{Summary: "NEXT_PROMPT_SAFE: yes", NextPromptSafe: boolPtr(true)}, reason: "missing or malformed STATUS"},
		{name: "unsafe", result: completionResult("PASS", false), reason: "NEXT_PROMPT_SAFE is no"},
		{name: "empty final", result: &state.EngineResult{}, reason: "empty final answer"},
		{name: "clarification", result: &state.EngineResult{Summary: "Could you clarify the requirement?"}, reason: "missing or malformed STATUS"},
		{name: "duplicate", result: &state.EngineResult{Summary: "STATUS: PASS\nSTATUS: PASS\nNEXT_PROMPT_SAFE: yes", CompletionStatus: "PASS", NextPromptSafe: boolPtr(true), CompletionReason: "duplicate completion fields"}, reason: "duplicate completion fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			writePromptFile(t, dir, "10-implement-first.md", "first")
			writePromptFile(t, dir, "20-test-never.md", "never")
			launcher := &fakeLauncher{result: tc.result}
			summary, err := Run(dir, Options{HomeDir: home}, launcher)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v", err)
			}
			if len(launcher.calls) != 1 || summary.Sequence.Status != "failed" || summary.Sequence.Items[1].Status != "pending" {
				t.Fatalf("calls=%d sequence=%#v", len(launcher.calls), summary.Sequence)
			}
			item := summary.Sequence.Items[0]
			if item.WorkerID == "" || item.ExitCode == nil || *item.ExitCode != 0 || item.CompletionReason == "" {
				t.Fatalf("persisted item = %#v", item)
			}
		})
	}
}

func TestOrderedPromptInjectsContractExactlyOnce(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "00-specification.md", "shared")
	writePromptFile(t, dir, "10-implement-a.md", "task")
	launcher := &fakeLauncher{}
	if _, err := Run(dir, Options{HomeDir: t.TempDir()}, launcher); err != nil {
		t.Fatal(err)
	}
	content := launcher.calls[0].Content
	if strings.Count(content, "# Required Completion Report") != 1 || !strings.Contains(content, "STATUS: PASS") || !strings.Contains(content, "NEXT_PROMPT_SAFE: yes") {
		t.Fatalf("assembled prompt = %q", content)
	}
}

func TestCommitEachTellsWorkerNotToCommit(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "task")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	launcher := &fakeLauncher{}
	if _, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir(), CommitEach: true}, launcher); err != nil {
		t.Fatal(err)
	}
	content := launcher.calls[0].Content
	for _, want := range []string{"PromptGrinder is running with --commit-each", "Do not run git commit", "Leave approved changes in the worktree"} {
		if !strings.Contains(content, want) {
			t.Fatalf("assembled prompt missing %q: %s", want, content)
		}
	}
}

func TestPreflightChecksExpectedPathsAgainstAllowedPaths(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "---\nallowed_paths: [backend/api/**]\n---\ntask\n```yaml\nworker: {id: backend}\nexpected_paths: [backend/service.go]\n```\n")
	_, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "expected_paths are not permitted") || !strings.Contains(err.Error(), "backend/service.go") {
		t.Fatalf("err = %v", err)
	}
}

func completionResult(status string, safe bool) *state.EngineResult {
	safeValue := "no"
	if safe {
		safeValue = "yes"
	}
	return &state.EngineResult{Summary: "done\nSTATUS: " + status + "\nNEXT_PROMPT_SAFE: " + safeValue, CompletionStatus: status, NextPromptSafe: boolPtr(safe)}
}

func TestOrderedCompletionParsingIsAdapterIndependent(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writePromptFile(t, dir, "10-implement-first.md", "first")
	result := &state.EngineResult{
		Summary:          "STATUS: PASS\nSTATUS: PASS\nNEXT_PROMPT_SAFE: yes",
		CompletionStatus: "PASS",
		NextPromptSafe:   boolPtr(true),
	}
	summary, err := Run(dir, Options{HomeDir: home, EngineOverride: "replaceable-adapter"}, &fakeLauncher{result: result})
	if err == nil || !strings.Contains(err.Error(), "duplicate completion fields") {
		t.Fatalf("err = %v", err)
	}
	if summary.Sequence.Items[0].CompletionReason != "duplicate completion fields" {
		t.Fatalf("item = %#v", summary.Sequence.Items[0])
	}
}

func TestEngineFailurePersistsOrderedCompletionFields(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writePromptFile(t, dir, "10-implement-first.md", "first")
	unsafe := false
	engineExitCode := 23
	launcher := &fakeLauncher{result: &state.EngineResult{
		Summary:        "STATUS: BLOCKED\nNEXT_PROMPT_SAFE: no",
		EngineExitCode: &engineExitCode,
		NextPromptSafe: &unsafe,
	}}

	summary, err := Run(dir, Options{HomeDir: home, EngineOverride: "replaceable-adapter"}, launcher)
	if err == nil {
		t.Fatal("expected engine failure")
	}
	item := summary.Sequence.Items[0]
	if item.ExitCode == nil || *item.ExitCode != engineExitCode || item.CompletionStatus != "BLOCKED" || item.NextPromptSafe == nil || *item.NextPromptSafe || item.CompletionReason != "STATUS is BLOCKED, not PASS" {
		t.Fatalf("item = %#v", item)
	}
}

func boolPtr(value bool) *bool { return &value }

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
		"00A-specification.md":         TypeSpecification,
		"10-implement-v1-cleanup.md":   TypeImplement,
		"08A-implement-ranking.md":     TypeImplement,
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

func TestDiscoverIncludesOnlyRecognizedNumberedPrompts(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-custom-task.md", "task")
	writePromptFile(t, dir, "20-implement-task.md", "task")
	writePromptFile(t, dir, "README.md", "overview")
	writePromptFile(t, dir, "notes.md", "notes")

	inspection, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Prompts) != 1 || inspection.Prompts[0].Name != "20-implement-task.md" {
		t.Fatalf("prompts = %#v", inspection.Prompts)
	}
	if inspection.MarkdownTotal != 4 || strings.Join(inspection.Ignored, ",") != "README.md,notes.md" || strings.Join(inspection.Invalid, ",") != "10-custom-task.md" {
		t.Fatalf("inspection = %#v", inspection)
	}
	if _, err := Discover(dir); err == nil || !strings.Contains(err.Error(), "1 of 4 Markdown files included") || !strings.Contains(err.Error(), "10-custom-task.md") {
		t.Fatalf("Discover error = %v", err)
	}
}

func TestDiscoverUsesExplicitMetadataForGenericNumberedPrompts(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "01-snapshot-reliability.md", "---\nid: snapshot-reliability\ntype: implement\nrole: backend-feature\ndepends_on: []\n---\nfirst")
	writePromptFile(t, dir, "02-api-contract.md", "---\nid: api-contract\ntype: review\nrole: reviewer\ndepends_on: [snapshot-reliability]\n---\nsecond")

	prompts, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || prompts[0].ID != "snapshot-reliability" || prompts[0].Type != TypeImplement || prompts[0].Role != "backend-feature" || prompts[1].Type != TypeReview || strings.Join(prompts[1].DependsOn, ",") != "snapshot-reliability" {
		t.Fatalf("prompts = %#v", prompts)
	}
}

func TestDiscoverSupportsLetterSuffixedOrderingTokens(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "08C-score-contribution.md", "---\nid: score-contribution\ntype: implement\ndepends_on: [round-snapshots]\n---\nthird")
	writePromptFile(t, dir, "08A-ranking-history.md", "---\nid: ranking-history\ntype: implement\ndepends_on: []\n---\nfirst")
	writePromptFile(t, dir, "08B-round-snapshots.md", "---\nid: round-snapshots\ntype: implement\ndepends_on: [ranking-history]\n---\nsecond")

	prompts, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := names(prompts)
	want := []string{"08A-ranking-history.md", "08B-round-snapshots.md", "08C-score-contribution.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompts = %v, want %v", got, want)
	}
}

func TestDiscoverRejectsInvalidExplicitDependencyGraph(t *testing.T) {
	for _, test := range []struct {
		name, first, second, want string
	}{
		{name: "unknown", first: "id: first\ntype: implement", second: "id: second\ntype: test\ndepends_on: [missing]", want: `depends on unknown task id "missing"`},
		{name: "forward", first: "id: first\ntype: implement\ndepends_on: [second]", second: "id: second\ntype: test", want: "must appear earlier"},
		{name: "duplicate", first: "id: same\ntype: implement", second: "id: same\ntype: test", want: `duplicate task id "same"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writePromptFile(t, dir, "01-first.md", "---\n"+test.first+"\n---\nfirst")
			writePromptFile(t, dir, "02-second.md", "---\n"+test.second+"\n---\nsecond")
			if _, err := Discover(dir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDiscoverGenericPromptRequiresExplicitID(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "01-task.md", "---\ntype: implement\n---\ntask")
	if _, err := Discover(dir); err == nil || !strings.Contains(err.Error(), "must declare id") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPreflightValidatesEveryPromptBeforeCreatingState(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-valid.md", "valid")
	writePromptFile(t, dir, "20-test-invalid.md", "---\nunknown: true\n---\ninvalid")
	home := t.TempDir()
	_, err := Run(dir, Options{HomeDir: home}, &fakeLauncher{})
	if err == nil || !strings.Contains(err.Error(), "20-test-invalid.md") {
		t.Fatalf("Run error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".promptgrinder")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("folder state created before preflight completed: %v", statErr)
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
	readJSON(t, filepath.Join(folderStateRoot("", summary.Sequence.SequenceID), "prompts", "10-implement-a.md.json"), &promptState)
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

func TestSpecificationAndTaskSemanticsRenderExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "00-specification.md", "---\nacceptance_criteria: spec rule\n---\nspec body")
	writePromptFile(t, dir, "10-implement-a.md", "---\nvalidation: task check\n---\ntask body")
	launcher := &fakeLauncher{}
	if _, err := Run(dir, Options{}, launcher); err != nil {
		t.Fatal(err)
	}
	parsed, err := markdown.Parse(launcher.calls[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if err := markdown.Validate(parsed, launcher.calls[0].Path); err != nil {
		t.Fatal(err)
	}
	rendered := string(markdown.Render(parsed))
	for _, value := range []string{"spec rule", "task check"} {
		if strings.Count(rendered, value) != 1 {
			t.Fatalf("%q count in prompt = %d\n%s", value, strings.Count(rendered, value), rendered)
		}
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

	firstSummary, err := Run(dir, Options{}, first)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(folderStateRoot("", firstSummary.Sequence.SequenceID), "run.json")); err != nil {
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

func TestUnknownPromptIsNotRunnable(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-custom.md", "custom")
	_, err := Run(dir, Options{}, &fakeLauncher{})
	if err == nil || !strings.Contains(err.Error(), "unsupported numbered prompt name") {
		t.Fatalf("err = %v", err)
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

func TestOldPersistedSequenceWithoutNewTimestampsLoads(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "state", "sequences")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"sequence_id":"seq_old_format","folder":"/tmp/tasks","status":"completed","items":[]}`
	if err := os.WriteFile(filepath.Join(root, "seq_old_format.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	sequence, err := LoadSequence(home, "seq_old_format")
	if err != nil || sequence.SequenceID != "seq_old_format" || sequence.CreatedAt != nil || sequence.FinishedAt != nil {
		t.Fatalf("sequence=%#v err=%v", sequence, err)
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
	supervisor := &Supervisor{ID: "sup_live", PID: os.Getpid(), Status: "running", HeartbeatAt: &now}
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

func TestReconcileSequencesMarksDeadSupervisorDespiteFreshHeartbeat(t *testing.T) {
	home := t.TempDir()
	store := newSequenceStore(home)
	old := time.Now().UTC().Add(-48 * time.Hour)
	fresh := time.Now().UTC()
	supervisor := &Supervisor{ID: "sup_dead", PID: 99999999, Status: "running", HeartbeatAt: &fresh}
	sequence := SequenceState{SequenceID: "seq_dead", Status: "running", UpdatedAt: &old, Supervisor: supervisor, Items: []SequenceItem{{PromptName: "10-implement-a.md", Status: "running"}}}
	if err := store.save(sequence); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "state", "supervisors")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(supervisor)
	if err := os.WriteFile(filepath.Join(root, "sup_dead.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := ReconcileSequences(home, 24*time.Hour, true)
	if err != nil || len(stale) != 1 || stale[0].Status != "interrupted" {
		t.Fatalf("stale=%#v err=%v", stale, err)
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

func TestDetachedNotificationsReportSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, fail, want string
	}{{"success", "", "notification.success"}, {"failure", "10-implement-a.md", "notification.failure"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			writePromptFile(t, dir, "10-implement-a.md", "task")
			notifier := &recordingNotifier{}
			_, _ = Run(dir, Options{HomeDir: home, SupervisorID: "sup_notify", Notifier: notifier}, &fakeLauncher{failName: tc.fail})
			if len(notifier.notifications) != 1 || notifier.notifications[0].Type != tc.want {
				t.Fatalf("notifications = %#v", notifier.notifications)
			}
			events, err := os.ReadFile(filepath.Join(home, "events", "sequences", notifier.notifications[0].SequenceID+".jsonl"))
			if err != nil || !strings.Contains(string(events), `"type":"`+tc.want+`"`) {
				t.Fatalf("events=%q err=%v", events, err)
			}
		})
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
	data, err := os.ReadFile(filepath.Join(folderStateRoot(home, summary.Sequence.SequenceID), "summary.md"))
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
	summary, err := Run(dir, Options{RepoPath: dir, Checkpoint: true}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	var promptState PromptState
	readJSON(t, filepath.Join(folderStateRoot("", summary.Sequence.SequenceID), "prompts", "10-implement-a.md.json"), &promptState)
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

	summary, err := Run(promptDir, Options{RepoPath: repo, Checkpoint: true}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	var promptState PromptState
	readJSON(t, filepath.Join(folderStateRoot("", summary.Sequence.SequenceID), "prompts", "10-implement-a.md.json"), &promptState)
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
	summary, err := Run(dir, Options{RepoPath: dir, Checkpoint: true, CommitEach: true}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	var first PromptState
	readJSON(t, filepath.Join(folderStateRoot("", summary.Sequence.SequenceID), "prompts", "10-implement-a.md.json"), &first)
	var second PromptState
	readJSON(t, filepath.Join(folderStateRoot("", summary.Sequence.SequenceID), "prompts", "20-implement-b.md.json"), &second)
	if first.CommitSHA == "" {
		t.Fatalf("first prompt was not committed: %#v", first)
	}
	if second.CommitSHA != "" {
		t.Fatalf("second prompt should not commit without changes: %#v", second)
	}
}

func TestGitCommitFocusedReportsWorkerCommitOwnershipConflict(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "tracked.txt", "initial")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	baseline, err := workerpathpolicy.Capture(dir)
	if err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, dir, "approved.txt", "approved")
	git(t, dir, "add", "approved.txt")
	git(t, dir, "commit", "-m", "worker committed approved change")
	workerCommit := strings.TrimSpace(string(gitOutput(t, dir, "rev-parse", "HEAD")))

	_, err = gitCommitFocused(dir, "PromptGrinder: complete task", baseline, []string{"approved.txt"})
	if err == nil {
		t.Fatal("expected commit ownership conflict")
	}
	for _, want := range []string{"Commit ownership conflict:", "Worker commit: " + workerCommit, "Worktree: clean", "No changes were lost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
	clean, cleanErr := gitClean(dir)
	if cleanErr != nil || !clean {
		t.Fatalf("worker commit evidence was modified: clean=%t err=%v", clean, cleanErr)
	}
	if got := strings.TrimSpace(string(gitOutput(t, dir, "rev-parse", "HEAD"))); got != workerCommit {
		t.Fatalf("HEAD = %s, want unchanged worker commit %s", got, workerCommit)
	}
}

func TestStagedChangeMismatchRetainsGenericErrorWhenUnproven(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "tracked.txt", "initial")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	baseline, err := workerpathpolicy.Capture(dir)
	if err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, dir, "unexpected.txt", "unexpected")
	git(t, dir, "add", "unexpected.txt")

	err = stagedChangeMismatchError(dir, baseline, []string{"approved.txt"})
	if got, want := err.Error(), "staged change set does not exactly match approved worker changes"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
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

func TestPreflightReportsDirtyPathsWithoutCreatingRunState(t *testing.T) {
	dir := initGitRepo(t)
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "tracked.txt", "initial")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	writePromptFile(t, dir, "tracked.txt", "changed")
	writePromptFile(t, dir, "untracked.txt", "new")

	_, err := Preflight(dir, Options{HomeDir: home, RepoPath: dir, CommitEach: true})
	if err == nil {
		t.Fatal("expected dirty baseline error")
	}
	for _, want := range []string{"Cannot use --commit-each", "Modified:", "tracked.txt", "Untracked:", "untracked.txt", "Commit, stash, or isolate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%s", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(home, "state", "sequences")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preflight created sequence state: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".promptgrinder")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preflight created worktree state: %v", statErr)
	}
}

func TestCancelSequencePreservesCompletedCheckpoint(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	sequence := SequenceState{SequenceID: "seq_cancel", Folder: "/tmp/prompts", Status: "running", Items: []SequenceItem{
		{PromptName: "10-implement-a.md", Status: "succeeded"},
		{PromptName: "20-test-b.md", Status: "running"},
		{PromptName: "30-verify-c.md", Status: "pending"},
	}, StartedAt: &now, UpdatedAt: &now}
	if err := newSequenceStore(home).save(sequence); err != nil {
		t.Fatal(err)
	}
	cancelled, err := CancelSequence(home, sequence.SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || cancelled.Items[0].Status != "succeeded" || cancelled.Items[1].Status != "cancelled" || cancelled.Items[2].Status != "cancelled" {
		t.Fatalf("cancelled sequence = %#v", cancelled)
	}
}

func TestCommitEachRefusesIncidentBaselineWithoutLaunchingOrStaging(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "tracked.txt", "initial")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	if err := os.MkdirAll(filepath.Join(dir, "exports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "mobile-android", ".idea"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, filepath.Join(dir, "exports"), "report.txt", "unrelated")
	writePromptFile(t, filepath.Join(dir, "mobile-android", ".idea"), "workspace.xml", "unrelated")
	launcher := &fakeLauncher{}
	_, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir(), CommitEach: true}, launcher)
	if err == nil || !strings.Contains(err.Error(), "clean baseline") {
		t.Fatalf("err = %v", err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("worker launched: %#v", launcher.calls)
	}
	out, commandErr := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	if commandErr != nil || len(out) != 0 {
		t.Fatalf("index=%q err=%v", out, commandErr)
	}
}

func TestRunFolderPathPolicyRejectsWorkerCreatedUnallowedPath(t *testing.T) {
	dir := initGitRepo(t)
	writePromptFile(t, dir, "10-implement-a.md", "---\nallowed_paths: [src/**]\nforbidden_paths: []\nacceptance_criteria: safe\nvalidation: inspect\n---\na")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	launcher := &fakeLauncher{onLaunch: func(string) { writePromptFile(t, dir, "outside.txt", "evidence") }}
	var failureReason string
	_, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir(), Progress: func(event ProgressEvent) {
		if event.Type == "prompt.failed" {
			failureReason = event.Reason
		}
	}}, launcher)
	if err == nil || !strings.Contains(err.Error(), "path policy violation") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(failureReason, "outside.txt (outside allowed paths)") {
		t.Fatalf("failure reason = %q", failureReason)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "outside.txt")); statErr != nil {
		t.Fatalf("evidence missing: %v", statErr)
	}
	out, _ := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	if len(out) != 0 {
		t.Fatalf("violation staged: %q", out)
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

func gitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return out
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
