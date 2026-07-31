package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
)

func TestSubmitAndAcceptCompleteEvidence(t *testing.T) {
	fixture := newFixture(t, true)
	result, err := fixture.service.Submit(fixture.registry, fixture.task.ID, "implementer", completeEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Lifecycle != workerdomain.LifecycleAwaitingReview || result.Task.Status != workerdomain.TaskStatusReview {
		t.Fatalf("submit state/task = %s/%s", result.State.Lifecycle, result.Task.Status)
	}
	if len(result.Handoff.TaskAttempts) != 1 || len(result.Handoff.RuntimeEvidence) != 1 ||
		len(result.Handoff.Validations) != 1 || result.Handoff.ImplementerID != "implementer" {
		t.Fatalf("incomplete handoff: %+v", result.Handoff)
	}

	accepted, err := fixture.service.Accept(fixture.registry, fixture.task.ID, "reviewer", "tests and changes look correct")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Task.Status != workerdomain.TaskStatusAccepted || accepted.State.Lifecycle != workerdomain.LifecycleIdle ||
		accepted.State.LastCompletedTaskID != fixture.task.ID || accepted.State.ActiveTaskID != "" {
		t.Fatalf("accepted result = %+v", accepted)
	}
	if accepted.Handoff.Status != workerdomain.ReviewStatusAccepted || accepted.Handoff.ReviewerID != "reviewer" {
		t.Fatalf("accepted handoff = %+v", accepted.Handoff)
	}
}

func TestSeparateReviewerCannotSelfApprove(t *testing.T) {
	fixture := newFixture(t, true)
	if _, err := fixture.service.Submit(fixture.registry, fixture.task.ID, "implementer", completeEvidence()); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.Accept(fixture.registry, fixture.task.ID, "implementer", "self approval")
	if err == nil || !strings.Contains(err.Error(), "requires a reviewer separate") {
		t.Fatalf("self approval error = %v", err)
	}
	inspected, inspectErr := fixture.service.Inspect(fixture.registry, fixture.task.ID)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if inspected.Handoff.Status != workerdomain.ReviewStatusPending {
		t.Fatalf("failed authorization changed handoff: %+v", inspected.Handoff)
	}
}

func TestRejectPreservesEvidenceAndRequeuesOriginalWorker(t *testing.T) {
	fixture := newFixture(t, true)
	if _, err := fixture.service.Submit(fixture.registry, fixture.task.ID, "implementer", completeEvidence()); err != nil {
		t.Fatal(err)
	}
	rejected, err := fixture.service.Reject(fixture.registry, fixture.task.ID, "reviewer", "validation needs another case")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.RejectionPolicy != RejectionPolicy || rejected.Task.Status != workerdomain.TaskStatusPending ||
		rejected.State.Lifecycle != workerdomain.LifecycleIdle || rejected.State.ActiveTaskID != "" {
		t.Fatalf("rejected result = %+v", rejected)
	}
	if len(rejected.Task.ReviewHandoffs) != 1 || rejected.Handoff.Status != workerdomain.ReviewStatusRejected ||
		len(rejected.Handoff.TaskAttempts) != 1 {
		t.Fatalf("rejected evidence was not preserved: %+v", rejected.Handoff)
	}
	queue, err := taskqueue.New(fixture.home).List("project", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Entries) != 1 || queue.Entries[0].TaskID != fixture.task.ID {
		t.Fatalf("queue = %+v", queue)
	}
}

func TestSubmitRejectsIncompleteEvidenceAndInvalidLifecycle(t *testing.T) {
	fixture := newFixture(t, true)
	_, err := fixture.service.Submit(fixture.registry, fixture.task.ID, "implementer", Evidence{Summary: "done"})
	if err == nil || !strings.Contains(err.Error(), "validation evidence") {
		t.Fatalf("incomplete evidence error = %v", err)
	}
	state, err := workerstate.New(fixture.home).Load("project", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	state, err = workerstate.New(fixture.home).Transition(state, workerdomain.LifecycleBlocked, "manual")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Submit(fixture.registry, fixture.task.ID, "implementer", completeEvidence())
	if err == nil || !strings.Contains(err.Error(), "not executing together") {
		t.Fatalf("invalid lifecycle error = %v", err)
	}
}

type fixture struct {
	home     string
	registry *workerregistry.Registry
	service  Service
	task     workerdomain.Task
}

func newFixture(t *testing.T, separate bool) fixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("# Task\n\nImplement it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	implementer := definition("implementer")
	reviewer := definition("reviewer")
	registry := &workerregistry.Registry{
		Root: root, Version: 1,
		Project: workerdomain.Project{ID: "project", Name: "Project", RequireSeparateReviewer: separate},
		Workers: map[string]workerdomain.WorkerDefinition{"implementer": implementer, "reviewer": reviewer},
	}
	task, err := taskstore.New(home).Assign(root, implementer, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	task, err = taskstore.New(home).UpdateControl("project", task.ID, func(current *workerdomain.Task) error {
		current.AttemptCount = 1
		current.Attempts = append(current.Attempts, workerdomain.TaskAttempt{
			Number: 1, RunID: "run-1", Runtime: "codex", SessionID: "session-1",
			StartedAt: time.Unix(100, 0).UTC(), Disposition: "completed",
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	states := workerstate.New(home)
	state, err := states.Load("project", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	state, err = states.Transition(state, workerdomain.LifecycleStarting, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = states.Transition(state, workerdomain.LifecycleExecuting, ""); err != nil {
		t.Fatal(err)
	}
	return fixture{home: home, registry: registry, service: Service{Home: home, Now: func() time.Time { return time.Unix(200, 0).UTC() }}, task: task}
}

func definition(id string) workerdomain.WorkerDefinition {
	return workerdomain.WorkerDefinition{
		ID: id, DisplayName: id, Role: id + " role", ProjectID: "project",
		Runtime: workerdomain.RuntimeRef{Name: "codex"},
		Policy:  workerdomain.WorkerPolicy{BranchPrefix: "worker/" + id, DefaultWorktree: "."},
	}
}

func completeEvidence() Evidence {
	return Evidence{
		Summary:      "implemented review workflow",
		ChangedPaths: []string{"internal/review/review.go"},
		Commits:      []string{},
		Validations:  []workerdomain.Validation{{Command: "go test ./...", Result: "pass"}},
	}
}
