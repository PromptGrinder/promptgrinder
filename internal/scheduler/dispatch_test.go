package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstate"
)

func schedulerDefinition(project, id, runtimeName string) workerdomain.WorkerDefinition {
	return workerdomain.WorkerDefinition{
		ID: id, DisplayName: id, Role: "test", ProjectID: project,
		Runtime: workerdomain.RuntimeRef{Name: runtimeName},
		Policy: workerdomain.WorkerPolicy{
			BranchPrefix: "worker/" + id, DefaultWorktree: ".",
			AllowedPaths: []string{"**"}, ForbiddenPaths: []string{"secret/**"},
		},
	}
}

func schedulerFixture(t *testing.T, workerIDs ...string) (string, string, *workerregistry.Registry) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	workers := map[string]workerdomain.WorkerDefinition{}
	for _, id := range workerIDs {
		definition := schedulerDefinition("project", id, "codex")
		workers[id] = definition
		if _, err := workerstate.New(home).Ensure(definition); err != nil {
			t.Fatal(err)
		}
	}
	return root, home, &workerregistry.Registry{
		Root: root, Version: 1, Project: workerdomain.Project{ID: "project", Name: "Project"}, Workers: workers,
	}
}

func enqueueSchedulerTask(t *testing.T, root, home string, definition workerdomain.WorkerDefinition, id string) {
	t.Helper()
	path := filepath.Join(root, id+".md")
	if err := os.WriteFile(path, []byte("# "+id+"\n\ninstructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := taskstore.New(home).Enqueue(root, definition, filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceDispatchesFIFOAndPersistsDecision(t *testing.T) {
	root, home, registry := schedulerFixture(t, "worker")
	definition := registry.Workers["worker"]
	enqueueSchedulerTask(t, root, home, definition, "one")
	enqueueSchedulerTask(t, root, home, definition, "two")
	service := Service{Home: home, Config: config.Config{SchedulerLeaseTTL: time.Minute}, Owner: "test"}
	dispatch, err := service.RunOnce(context.Background(), registry)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch == nil || dispatch.Task.ID != "one" {
		t.Fatalf("dispatch = %#v, want task one", dispatch)
	}
	queue, err := taskqueue.New(home).List("project", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Entries) != 1 || queue.Entries[0].TaskID != "two" {
		t.Fatalf("remaining queue = %#v", queue.Entries)
	}
	if data, err := os.ReadFile(service.EventsPath("project")); err != nil || len(data) == 0 {
		t.Fatalf("scheduler events: data=%q err=%v", data, err)
	}
}

func TestConcurrentSchedulersDispatchOnlyOnce(t *testing.T) {
	root, home, registry := schedulerFixture(t, "worker")
	enqueueSchedulerTask(t, root, home, registry.Workers["worker"], "one")
	var wg sync.WaitGroup
	results := make(chan *Dispatch, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"one", "two"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			dispatch, err := (Service{Home: home, Config: config.Config{SchedulerLeaseTTL: time.Minute}, Owner: owner}).RunOnce(context.Background(), registry)
			results <- dispatch
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for dispatch := range results {
		if dispatch != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("dispatch count = %d, want 1", count)
	}
}

func TestConcurrencyLimitsDeferDispatch(t *testing.T) {
	root, home, registry := schedulerFixture(t, "busy", "idle")
	enqueueSchedulerTask(t, root, home, registry.Workers["idle"], "queued")
	state, err := workerstate.New(home).Load("project", "busy")
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveTaskID = "running"
	state, err = workerstate.New(home).Save(state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workerstate.New(home).Transition(state, workerdomain.LifecycleStarting, ""); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]config.Config{
		"project": {SchedulerProjectConcurrency: 1, SchedulerLeaseTTL: time.Minute},
		"runtime": {SchedulerRuntimeConcurrency: map[string]int{"codex": 1}, SchedulerLeaseTTL: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			dispatch, err := (Service{Home: home, Config: cfg}).RunOnce(context.Background(), registry)
			if err != nil {
				t.Fatal(err)
			}
			if dispatch != nil {
				t.Fatalf("dispatch = %#v, want deferred", dispatch)
			}
		})
	}
}
