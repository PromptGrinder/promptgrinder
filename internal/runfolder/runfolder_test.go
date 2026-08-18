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
	calls              []launchCall
	failName           string
	failOnceName       string
	failedOnce         bool
	running            bool
	logDir             string
	logText            string
	onLaunch           func(path string)
	reportedSessionID  string
	result             *state.EngineResult
	resultOnce         *state.EngineResult
	returnedResultOnce bool
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
	if filepath.Base(path) == f.failOnceName && !f.failedOnce {
		f.failedOnce = true
		return state.Worker{ID: "wrk_fail", Status: state.StatusFailed}, fmt.Errorf("launch failed")
	}
	if f.running {
		return state.Worker{ID: "wrk_running", Status: state.StatusRunning}, nil
	}
	zero := 0
	nextSafe := true
	worker := state.Worker{ID: "wrk_" + strings.TrimSuffix(filepath.Base(path), ".md"), Status: state.StatusSucceeded, ExitCode: &zero, EngineResult: &state.EngineResult{Summary: "done\nSTATUS: PASS\nNEXT_PROMPT_SAFE: yes", CompletionStatus: "PASS", NextPromptSafe: &nextSafe}}
	if f.resultOnce != nil && !f.returnedResultOnce {
		copy := *f.resultOnce
		worker.EngineResult = &copy
		f.returnedResultOnce = true
	} else if f.result != nil {
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

func TestRunFolderRecoversOnlyTheFailedSlice(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writePromptFile(t, dir, "10-implement-first.md", "first")
	writePromptFile(t, dir, "20-implement-recover.md", "recover")
	writePromptFile(t, dir, "30-test-last.md", "last")
	events := []ProgressEvent{}
	launcher := &fakeLauncher{failOnceName: "20-implement-recover.md"}

	summary, err := Run(dir, Options{HomeDir: home, RecoveryAttempts: 1, Progress: func(event ProgressEvent) {
		events = append(events, event)
	}}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 4 {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if got := []string{filepath.Base(launcher.calls[0].Path), filepath.Base(launcher.calls[1].Path), filepath.Base(launcher.calls[2].Path), filepath.Base(launcher.calls[3].Path)}; strings.Join(got, ",") != "10-implement-first.md,20-implement-recover.md,20-implement-recover.md,30-test-last.md" {
		t.Fatalf("launch order = %v", got)
	}
	if !strings.Contains(launcher.calls[2].Content, "# Recovery Attempt") || !strings.Contains(launcher.calls[2].Content, "Previous failure: launch failed") {
		t.Fatalf("recovery prompt = %q", launcher.calls[2].Content)
	}
	if summary.Sequence.Status != "completed" || summary.Sequence.Items[1].RecoveryAttempts != 1 {
		t.Fatalf("summary = %#v", summary.Sequence)
	}
	foundRecovery := false
	for _, event := range events {
		if event.Type == "prompt.recovering" && event.PromptName == "20-implement-recover.md" && event.RecoveryAttempt == 1 {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatalf("progress events = %#v", events)
	}
}

func TestRunFolderRecoversBlockedCompletionInTheSameSlice(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writePromptFile(t, dir, "10-implement-first.md", "first")
	launcher := &fakeLauncher{resultOnce: completionResult("BLOCKED", false)}

	summary, err := Run(dir, Options{HomeDir: home, RecoveryAttempts: 1}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 || summary.Sequence.Items[0].RecoveryAttempts != 1 {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if !strings.Contains(launcher.calls[1].Content, "Previous completion reason: STATUS is BLOCKED, not PASS") {
		t.Fatalf("recovery prompt = %q", launcher.calls[1].Content)
	}
}

func TestRecoveryNeverBypassesSafetyFailures(t *testing.T) {
	for _, message := range []string{
		"path policy violation at completion: outside.txt (outside allowed paths)",
		"run-folder model preflight task: model is not selectable by this Codex runtime",
		"working tree is dirty; automatic commits require a clean baseline",
		"cancelled by user",
	} {
		if recoverableFailure(PromptState{}, errors.New(message)) {
			t.Fatalf("recoverable failure for %q", message)
		}
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

func TestRunFolderInjectsEffectiveRolePolicyWithoutRequiringRoleGates(t *testing.T) {
	dir := initGitRepo(t)
	writeRolePolicy(t, dir, "backend-feature", "Implement backend features.", []string{"backend/**"}, []string{"./mvnw verify"})
	writePromptFile(t, dir, "10-implement-a.md", "---\nrole: backend-feature\nvalidation: ./mvnw -pl backend test\n---\ntask")
	launcher := &fakeLauncher{}
	if _, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir()}, launcher); err != nil {
		t.Fatal(err)
	}
	content := launcher.calls[0].Content
	for _, want := range []string{"# Effective Role Policy", "Implement backend features.", "`backend/**`", "Run only validation declared by this slice", "./mvnw verify", "Do not run these gates unless"} {
		if !strings.Contains(content, want) {
			t.Fatalf("assembled prompt missing %q: %s", want, content)
		}
	}
}

func TestRolePolicyRejectsExpectedPathsOutsideRoleBoundary(t *testing.T) {
	dir := initGitRepo(t)
	writeRolePolicy(t, dir, "backend-feature", "Implement backend features.", []string{"backend/**"}, nil)
	writePromptFile(t, dir, "10-implement-a.md", "---\nrole: backend-feature\nallowed_paths: [\"**\"]\nexpected_paths: [mobile-android/app/Main.kt]\n---\ntask")
	_, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "outside role allowed paths") || !strings.Contains(err.Error(), "mobile-android/app/Main.kt") {
		t.Fatalf("err = %v", err)
	}
}

func TestRolePolicyRejectsChangesOutsideRoleBoundary(t *testing.T) {
	dir := initGitRepo(t)
	writeRolePolicy(t, dir, "backend-feature", "Implement backend features.", []string{"backend/**"}, nil)
	writePromptFile(t, dir, "10-implement-a.md", "---\nrole: backend-feature\n---\ntask")
	launcher := &fakeLauncher{onLaunch: func(string) {
		if err := os.MkdirAll(filepath.Join(dir, "mobile-android", "app"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mobile-android", "app", "Main.kt"), []byte("bad scope"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir()}, launcher)
	if err == nil || !strings.Contains(err.Error(), "outside role allowed paths") || !strings.Contains(err.Error(), "mobile-android/app/Main.kt") {
		t.Fatalf("err = %v", err)
	}
}

func TestRolePolicyNormalizesLegacyDirectoryScope(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRolePolicy(t, dir, "backend-feature", "Implement backend features.", []string{"backend"}, nil)
	writePromptFile(t, dir, "10-implement-a.md", "---\nrole: backend-feature\n---\ntask")
	launcher := &fakeLauncher{onLaunch: func(string) {
		if err := os.WriteFile(filepath.Join(dir, "backend", "Service.java"), []byte("in scope"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := Run(dir, Options{RepoPath: dir, HomeDir: t.TempDir()}, launcher); err != nil {
		t.Fatal(err)
	}
}

func TestRolePolicyChangesSequenceIdentity(t *testing.T) {
	dir := initGitRepo(t)
	writeRolePolicy(t, dir, "backend-feature", "Initial guidance.", []string{"backend/**"}, nil)
	writePromptFile(t, dir, "10-implement-a.md", "---\nrole: backend-feature\n---\ntask")
	first, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	writeRolePolicy(t, dir, "backend-feature", "Changed guidance.", []string{"backend/**"}, nil)
	second, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if first.SequenceID == second.SequenceID {
		t.Fatalf("sequence ID did not change: %s", first.SequenceID)
	}
}

func TestPreflightRejectsRoleMissingFromProjectRegistryBeforeWorkerLaunch(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".promptgrinder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".promptgrinder", "project.yaml"), []byte("name: example\nroles: [backend-feature]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRolePolicy(t, dir, "release-evidence", "Cross-stack verification.", []string{"backend/**"}, nil)
	writePromptFile(t, dir, "10-verify-release.md", "---\nrole: release-evidence\n---\nverify")
	_, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not registered") || !strings.Contains(err.Error(), "project.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestPreflightReportsRoleAndDirtyBaselineIssuesTogether(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".promptgrinder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".promptgrinder", "project.yaml"), []byte("name: example\nroles: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, dir, "10-verify-release.md", "---\nrole: release-evidence\n---\nverify")
	if err := os.WriteFile(filepath.Join(dir, "retained-report.md"), []byte("pending review"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Preflight(dir, Options{RepoPath: dir, HomeDir: t.TempDir(), RequireCleanGit: true})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	for _, want := range []string{"independent issues", "not registered", "Cannot use --commit-each or --require-clean-git", "retained-report.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%s", want, err)
		}
	}
	if got := result.Inspection.Prompts; len(got) != 1 || got[0].Name != "10-verify-release.md" {
		t.Fatalf("preflight inspection = %#v", got)
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

func TestRunFolderFreshContextModeStartsNewSessionBoundary(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "first")
	writePromptFile(t, dir, "20-implement-b.md", "---\ncontext_mode: fresh\n---\nsecond")
	writePromptFile(t, dir, "30-verify-c.md", "third")
	launcher := &fakeLauncher{reportedSessionID: "thread-123"}

	summary, err := Run(dir, Options{HomeDir: home}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 3 || launcher.calls[0].SessionID != "" || launcher.calls[1].SessionID != "" || launcher.calls[2].SessionID != "thread-123" {
		t.Fatalf("calls = %#v", launcher.calls)
	}
	if summary.Sequence.Items[1].ContextMode != ContextFresh {
		t.Fatalf("fresh context mode was not persisted: %#v", summary.Sequence.Items[1])
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

func TestInspectIncludesPGPromptFiles(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-api.pg", "implement")
	writePromptFile(t, dir, "20-test-api.pg", "test")
	writePromptFile(t, dir, "30-verify-api.md", "verify")

	inspection, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(inspection.Prompts); strings.Join(got, ",") != "10-implement-api.pg,20-test-api.pg,30-verify-api.md" {
		t.Fatalf("prompt files = %v", got)
	}
}

func TestInspectExplainsTypedFilenameAndFrontmatterMismatch(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "40-test-and-final-verify-ranking-rivals.md", "---\ntype: verify\n---\nverify")

	_, err := Inspect(dir)
	if err == nil {
		t.Fatal("expected type mismatch error")
	}
	for _, want := range []string{
		"starts with the \"test\" slice naming convention",
		"frontmatter says type: \"verify\"",
		"rename it to NN-verify-...{.md|.pg}",
		"change frontmatter type to \"test\"",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
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
	if _, err := Discover(dir); err == nil || !strings.Contains(err.Error(), "1 of 4 prompt files included") || !strings.Contains(err.Error(), "10-custom-task.md") {
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

func TestDiscoverDefaultsContextModeToSharedAndAcceptsFresh(t *testing.T) {
	dir := t.TempDir()
	writePromptFile(t, dir, "10-implement-default.md", "default")
	writePromptFile(t, dir, "20-implement-fresh.md", "---\ncontext_mode: fresh\n---\nfresh")

	prompts, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if prompts[0].ContextMode != ContextShared || prompts[1].ContextMode != ContextFresh {
		t.Fatalf("context modes = %#v", prompts)
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

func TestExplicitResumeAdoptsCompatiblePrefixAfterLaterPromptChanges(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	writePromptFile(t, dir, "30-implement-c.md", "c")
	first := &fakeLauncher{failName: "20-implement-b.md"}
	firstSummary, err := Run(dir, Options{HomeDir: home}, first)
	if err == nil {
		t.Fatal("expected failure")
	}
	writePromptFile(t, dir, "30-implement-c.md", "changed")
	resume := &fakeLauncher{}
	summary, err := Run(dir, Options{HomeDir: home, Resume: true}, resume)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Resumed || summary.Sequence.SequenceID != firstSummary.Sequence.SequenceID {
		t.Fatalf("summary = %#v", summary)
	}
	if got, want := strings.Join(namesFromCalls(resume.calls), ","), "20-implement-b.md,30-implement-c.md"; got != want {
		t.Fatalf("resume calls = %s, want %s", got, want)
	}
}

func TestDefaultRunAdoptsCompatiblePrefixAfterFailedSliceChanges(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	writePromptFile(t, dir, "30-implement-c.md", "c")
	first := &fakeLauncher{failName: "30-implement-c.md"}
	firstSummary, err := Run(dir, Options{HomeDir: home}, first)
	if err == nil {
		t.Fatal("expected failure")
	}
	writePromptFile(t, dir, "30-implement-c.md", "repaired prompt")

	resume := &fakeLauncher{}
	summary, err := Run(dir, Options{HomeDir: home}, resume)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Resumed || summary.Sequence.SequenceID != firstSummary.Sequence.SequenceID {
		t.Fatalf("summary = %#v", summary)
	}
	if got, want := strings.Join(namesFromCalls(resume.calls), ","), "30-implement-c.md"; got != want {
		t.Fatalf("resume calls = %s, want %s", got, want)
	}
}

func TestExplicitResumeRejectsChangedCompletedPrefix(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	if _, err := Run(dir, Options{HomeDir: home}, &fakeLauncher{failName: "20-implement-b.md"}); err == nil {
		t.Fatal("expected failure")
	}
	writePromptFile(t, dir, "10-implement-a.md", "changed")
	_, err := Run(dir, Options{HomeDir: home, Resume: true}, &fakeLauncher{})
	if err == nil || !strings.Contains(err.Error(), "no run state found") {
		t.Fatalf("err = %v", err)
	}
}

func TestResumeSequenceRetainsCompletedPrefixAcrossRolePolicyChange(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	writeRolePolicy(t, repo, "backend-feature", "Initial completed-slice policy.", []string{"backend/**"}, nil)
	writePromptFile(t, repo, "10-implement-a.md", "---\nrole: backend-feature\n---\na")
	writePromptFile(t, repo, "20-test-b.md", "b")

	firstSummary, err := Run(repo, Options{HomeDir: home, RepoPath: repo}, &fakeLauncher{failName: "20-test-b.md"})
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	sequenceID := firstSummary.Sequence.SequenceID
	previousPolicyHash := firstSummary.Sequence.Items[0].PolicyHash
	writeRolePolicy(t, repo, "backend-feature", "Changed policy for already completed work.", []string{"backend/**"}, []string{"go test ./..."})

	resume := &fakeLauncher{}
	summary, err := Run(repo, Options{HomeDir: home, RepoPath: repo, ResumeSequence: sequenceID}, resume)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(namesFromCalls(resume.calls), ","), "20-test-b.md"; got != want {
		t.Fatalf("resume calls = %s, want %s", got, want)
	}
	if summary.Adoption == nil || !summary.Adoption.Explicit || summary.Adoption.RestartAt != "20-test-b.md" || strings.Join(summary.Adoption.RetainedPrompts, ",") != "10-implement-a.md" {
		t.Fatalf("adoption = %#v", summary.Adoption)
	}
	if len(summary.Adoption.PolicyHashChanges) != 1 || summary.Adoption.PolicyHashChanges[0].PromptName != "10-implement-a.md" || summary.Adoption.PolicyHashChanges[0].PreviousHash != previousPolicyHash || summary.Adoption.PolicyHashChanges[0].CurrentHash == previousPolicyHash {
		t.Fatalf("policy changes = %#v", summary.Adoption.PolicyHashChanges)
	}

	events, err := os.ReadFile(filepath.Join(home, "events", "sequences", sequenceID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"sequence.adopted"`, `"explicit":true`, `"policy_hash_changes"`, `"restart_at":"20-test-b.md"`} {
		if !strings.Contains(string(events), want) {
			t.Fatalf("events = %s, missing %s", events, want)
		}
	}
	sequenceSummary, err := os.ReadFile(filepath.Join(home, "summaries", sequenceID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Sequence Adoption", "explicitly adopted", "role-policy fingerprint changed", "completed work was retained"} {
		if !strings.Contains(string(sequenceSummary), want) {
			t.Fatalf("summary = %s, missing %s", sequenceSummary, want)
		}
	}
}

func TestResumeSequenceRejectsUnsafeIdentityAndTerminalState(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		dir := initGitRepo(t)
		writePromptFile(t, dir, "10-implement-a.md", "a")
		_, err := Preflight(dir, Options{HomeDir: t.TempDir(), RepoPath: dir, ResumeSequence: "seq_0000000000000000"})
		if err == nil || !strings.Contains(err.Error(), "was not found") || !strings.Contains(err.Error(), "no run state was created or changed") {
			t.Fatalf("err = %v", err)
		}
		_, err = Preflight(dir, Options{HomeDir: t.TempDir(), RepoPath: dir, ResumeSequence: "../../sequence"})
		if err == nil || !strings.Contains(err.Error(), "invalid sequence id") {
			t.Fatalf("invalid id err = %v", err)
		}
	})

	t.Run("completed sequence", func(t *testing.T) {
		dir := initGitRepo(t)
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = Preflight(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: summary.Sequence.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "is completed and cannot be adopted") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("changed completed content", func(t *testing.T) {
		dir := initGitRepo(t)
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		writePromptFile(t, dir, "20-test-b.md", "b")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
		if err == nil {
			t.Fatal("expected first run to fail")
		}
		writePromptFile(t, dir, "10-implement-a.md", "changed")
		_, err = Preflight(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: summary.Sequence.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "completed prompt content changed") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("repository mismatch", func(t *testing.T) {
		dir := initGitRepo(t)
		otherRepo := initGitRepo(t)
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		writePromptFile(t, dir, "20-test-b.md", "b")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
		if err == nil {
			t.Fatal("expected first run to fail")
		}
		_, err = Preflight(dir, Options{HomeDir: home, RepoPath: otherRepo, ResumeSequence: summary.Sequence.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "belongs to repository") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("folder mismatch", func(t *testing.T) {
		dir := initGitRepo(t)
		otherFolder := t.TempDir()
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		writePromptFile(t, dir, "20-test-b.md", "b")
		writePromptFile(t, otherFolder, "10-implement-a.md", "a")
		writePromptFile(t, otherFolder, "20-test-b.md", "b")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
		if err == nil {
			t.Fatal("expected first run to fail")
		}
		_, err = Preflight(otherFolder, Options{HomeDir: home, RepoPath: dir, ResumeSequence: summary.Sequence.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "belongs to folder") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reordered prompts", func(t *testing.T) {
		dir := initGitRepo(t)
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		writePromptFile(t, dir, "20-test-b.md", "b")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
		if err == nil {
			t.Fatal("expected first run to fail")
		}
		if err := os.Rename(filepath.Join(dir, "10-implement-a.md"), filepath.Join(dir, "15-implement-a.md")); err != nil {
			t.Fatal(err)
		}
		_, err = Preflight(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: summary.Sequence.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "prompt order mismatch") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled sequence", func(t *testing.T) {
		dir := initGitRepo(t)
		home := t.TempDir()
		writePromptFile(t, dir, "10-implement-a.md", "a")
		writePromptFile(t, dir, "20-test-b.md", "b")
		summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
		if err == nil {
			t.Fatal("expected first run to fail")
		}
		cancelled := *summary.Sequence
		cancelled.Status = "cancelled"
		if err := newSequenceStore(home).save(cancelled); err != nil {
			t.Fatal(err)
		}
		_, err = Preflight(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: cancelled.SequenceID})
		if err == nil || !strings.Contains(err.Error(), "is cancelled and cannot be adopted") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestResumeSequenceRejectsDependencyChanges(t *testing.T) {
	dir := initGitRepo(t)
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "---\nid: a\n---\na")
	writePromptFile(t, dir, "20-test-b.md", "---\nid: b\ndepends_on: [a]\n---\nb")
	summary, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-test-b.md"})
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	writePromptFile(t, dir, "20-test-b.md", "---\nid: b\n---\nrepaired b")
	_, err = Preflight(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: summary.Sequence.SequenceID})
	if err == nil || !strings.Contains(err.Error(), "dependency mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestResumeSequenceRefreshesFingerprintsForNewlyCompletedSlices(t *testing.T) {
	dir := initGitRepo(t)
	home := t.TempDir()
	writePromptFile(t, dir, "10-implement-a.md", "a")
	writePromptFile(t, dir, "20-implement-b.md", "b")
	writePromptFile(t, dir, "30-test-c.md", "c")
	first, err := Run(dir, Options{HomeDir: home, RepoPath: dir}, &fakeLauncher{failName: "20-implement-b.md"})
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	writePromptFile(t, dir, "20-implement-b.md", "repaired b")
	second, err := Run(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: first.Sequence.SequenceID}, &fakeLauncher{failName: "30-test-c.md"})
	if err == nil {
		t.Fatal("expected adopted run to fail at the next slice")
	}
	if second.Sequence.Items[1].Status != "succeeded" {
		t.Fatalf("second sequence = %#v", second.Sequence)
	}

	finalLauncher := &fakeLauncher{}
	final, err := Run(dir, Options{HomeDir: home, RepoPath: dir, ResumeSequence: first.Sequence.SequenceID}, finalLauncher)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(namesFromCalls(finalLauncher.calls), ","); got != "30-test-c.md" {
		t.Fatalf("final resume calls = %s", got)
	}
	if final.Sequence.Status != "completed" || len(final.Sequence.Adoptions) != 2 {
		t.Fatalf("final sequence = %#v", final.Sequence)
	}
}

func TestResumeSequenceLegacyPolicyChangeUsesCheckpointEvidence(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	writeRolePolicy(t, repo, "backend-feature", "Initial policy.", []string{"backend/**"}, nil)
	writePromptFile(t, repo, "10-implement-a.md", "---\nrole: backend-feature\n---\na")
	writePromptFile(t, repo, "20-test-b.md", "b")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add prompts")

	summary, err := Run(repo, Options{HomeDir: home, RepoPath: repo, Checkpoint: true}, &fakeLauncher{failName: "20-test-b.md"})
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	legacy := *summary.Sequence
	legacy.StateVersion = 0
	legacy.RepositoryPath = ""
	legacy.Folder = ""
	for index := range legacy.Items {
		legacy.Items[index].PromptID = ""
		legacy.Items[index].DependsOn = nil
		legacy.Items[index].PromptHash = ""
		legacy.Items[index].PolicyHash = ""
	}
	if err := newSequenceStore(home).save(legacy); err != nil {
		t.Fatal(err)
	}
	writeRolePolicy(t, repo, "backend-feature", "Changed policy.", []string{"backend/**"}, nil)

	resume := &fakeLauncher{}
	adopted, err := Run(repo, Options{HomeDir: home, RepoPath: repo, Checkpoint: true, ResumeSequence: legacy.SequenceID}, resume)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(namesFromCalls(resume.calls), ","); got != "20-test-b.md" {
		t.Fatalf("resume calls = %s", got)
	}
	if adopted.Sequence.StateVersion != sequenceStateVersion || adopted.Adoption == nil || !adopted.Adoption.MigratedLegacyState {
		t.Fatalf("adopted legacy sequence = %#v", adopted.Sequence)
	}
	summaryData, err := os.ReadFile(filepath.Join(home, "summaries", legacy.SequenceID+".md"))
	if err != nil || !strings.Contains(string(summaryData), "Legacy state fingerprints were migrated") {
		t.Fatalf("summary=%q err=%v", summaryData, err)
	}
}

func TestResumeSequenceLegacyPolicyChangeWithoutEvidenceFailsMigration(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	writeRolePolicy(t, repo, "backend-feature", "Initial policy.", []string{"backend/**"}, nil)
	writePromptFile(t, repo, "10-implement-a.md", "---\nrole: backend-feature\n---\na")
	writePromptFile(t, repo, "20-test-b.md", "b")
	summary, err := Run(repo, Options{HomeDir: home, RepoPath: repo}, &fakeLauncher{failName: "20-test-b.md"})
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	legacy := *summary.Sequence
	legacy.StateVersion = 0
	for index := range legacy.Items {
		legacy.Items[index].PromptID = ""
		legacy.Items[index].DependsOn = nil
		legacy.Items[index].PromptHash = ""
		legacy.Items[index].PolicyHash = ""
	}
	if err := newSequenceStore(home).save(legacy); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(newSequenceStore(home).Root, legacy.SequenceID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	writeRolePolicy(t, repo, "backend-feature", "Changed policy.", []string{"backend/**"}, nil)

	_, err = Preflight(repo, Options{HomeDir: home, RepoPath: repo, ResumeSequence: legacy.SequenceID})
	for _, want := range []string{"cannot safely adopt legacy sequence", "checkpoint/commit", "did not modify or clone"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, missing %q", err, want)
		}
	}
	after, readErr := os.ReadFile(statePath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("rejected adoption modified sequence state: err=%v", readErr)
	}
	entries, readErr := os.ReadDir(newSequenceStore(home).Root)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("rejected adoption cloned sequence state: entries=%v err=%v", entries, readErr)
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

func TestResumeSequenceMutuallyExclusiveWithOtherResumeModes(t *testing.T) {
	for _, options := range []Options{
		{ResumeSequence: "seq_named", Resume: true},
		{ResumeSequence: "seq_named", Fresh: true},
		{ResumeSequence: "seq_named", Restart: true},
		{ResumeSequence: "seq_named", NoResume: true},
	} {
		_, err := Preflight(t.TempDir(), options)
		if err == nil || !strings.Contains(err.Error(), "--resume-sequence is mutually exclusive") {
			t.Fatalf("options = %#v, err = %v", options, err)
		}
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

func writeRolePolicy(t *testing.T, dir, id, description string, allowedPaths, qualityGates []string) {
	t.Helper()
	roleDir := filepath.Join(dir, ".promptgrinder", "roles")
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "id: %s\ndescription: %s\n", id, description)
	if len(allowedPaths) > 0 {
		body.WriteString("allowed_paths:\n")
		for _, pattern := range allowedPaths {
			fmt.Fprintf(&body, "  - %s\n", pattern)
		}
	}
	if len(qualityGates) > 0 {
		body.WriteString("quality_gates:\n")
		for _, gate := range qualityGates {
			fmt.Fprintf(&body, "  - %s\n", gate)
		}
	}
	if err := os.WriteFile(filepath.Join(roleDir, id+".yaml"), []byte(body.String()), 0o644); err != nil {
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
