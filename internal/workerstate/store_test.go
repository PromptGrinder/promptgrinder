package workerstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"promptgrinder/internal/workerdomain"
)

func testDefinition(project, worker string) workerdomain.WorkerDefinition {
	return workerdomain.WorkerDefinition{
		ID: worker, ProjectID: project, DisplayName: "Test Worker", Role: "Test.",
		Runtime: workerdomain.RuntimeRef{Name: "codex"},
		Policy: workerdomain.WorkerPolicy{
			BranchPrefix: "worker/" + worker, DefaultWorktree: ".",
			AllowedPaths:   []string{"src/**", "docs/**"},
			ForbiddenPaths: []string{"src/secrets/**"},
		},
	}
}

func TestAtomicPersistenceAndRestrictivePermissions(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store := New(home)
	state, err := store.Ensure(testDefinition("project-one", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveTaskID = "task-one"
	state, err = store.Save(state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path("project-one", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded workerdomain.WorkerState
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.ActiveTaskID != "task-one" {
		t.Fatalf("persisted state = %#v, error %v", decoded, err)
	}
	for _, path := range []string{
		home,
		filepath.Join(home, "projects"),
		filepath.Join(home, "projects", "project-one"),
		filepath.Dir(store.Path("project-one", "backend")),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s permissions = %o, want 700", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(store.Path("project-one", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state permissions = %o, want 600", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(store.Path("project-one", "backend")), ".state-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files after atomic write = %v, error %v", matches, err)
	}
	orphan := filepath.Join(filepath.Dir(store.Path("project-one", "backend")), ".state-interrupted.tmp")
	if err := os.WriteFile(orphan, []byte("{partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.Load("project-one", "backend"); err != nil || recovered.ActiveTaskID != "task-one" {
		t.Fatalf("interrupted-write recovery = %#v, %v", recovered, err)
	}
}

func TestRevisionConflictsIncludingConcurrentWriters(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "home"))
	original, err := store.Ensure(testDefinition("project-one", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	first := original
	first.ActiveRunID = "run-one"
	if _, err := store.Save(first, original.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(original, original.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}

	current, err := store.Load("project-one", "backend")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := current
			copy.ActiveRunID = time.Now().String()
			_, err := store.Save(copy, current.Revision)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent saves: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestCorruptFilesAndProjectIsolation(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "home"))
	one, err := store.Ensure(testDefinition("project-one", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.Ensure(testDefinition("project-two", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	if store.Path(one.ProjectID, one.WorkerID) == store.Path(two.ProjectID, two.WorkerID) {
		t.Fatal("identical worker IDs across projects shared a path")
	}
	if err := os.WriteFile(store.Path("project-one", "backend"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("project-one", "backend"); err == nil || !strings.Contains(err.Error(), "corrupt named-worker state") {
		t.Fatalf("corrupt load error = %v", err)
	}
	if loaded, err := store.Load("project-two", "backend"); err != nil || loaded.ProjectID != "project-two" {
		t.Fatalf("isolated project load = %#v, %v", loaded, err)
	}
}

func TestEveryValidAndInvalidTransitionPersistsAuthoritatively(t *testing.T) {
	valid := [][2]workerdomain.Lifecycle{
		{workerdomain.LifecycleIdle, workerdomain.LifecycleStarting},
		{workerdomain.LifecycleStarting, workerdomain.LifecycleExecuting},
		{workerdomain.LifecycleStarting, workerdomain.LifecycleFailed},
		{workerdomain.LifecycleExecuting, workerdomain.LifecycleIdle},
		{workerdomain.LifecycleExecuting, workerdomain.LifecycleBlocked},
		{workerdomain.LifecycleExecuting, workerdomain.LifecycleAwaitingReview},
		{workerdomain.LifecycleExecuting, workerdomain.LifecycleFailed},
		{workerdomain.LifecycleBlocked, workerdomain.LifecycleExecuting},
		{workerdomain.LifecycleAwaitingReview, workerdomain.LifecycleIdle},
		{workerdomain.LifecycleFailed, workerdomain.LifecycleIdle},
	}
	all := []workerdomain.Lifecycle{
		workerdomain.LifecycleIdle, workerdomain.LifecycleStarting,
		workerdomain.LifecycleExecuting, workerdomain.LifecycleBlocked,
		workerdomain.LifecycleAwaitingReview, workerdomain.LifecycleFailed,
	}
	validSet := map[[2]workerdomain.Lifecycle]bool{}
	for _, pair := range valid {
		validSet[pair] = true
		t.Run(string(pair[0])+"-"+string(pair[1]), func(t *testing.T) {
			store := New(filepath.Join(t.TempDir(), "home"))
			state, err := store.Ensure(testDefinition("project-one", "backend"))
			if err != nil {
				t.Fatal(err)
			}
			state.Lifecycle = pair[0]
			state, err = store.Save(state, state.Revision)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := store.Transition(state, pair[1], "reason")
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load("project-one", "backend")
			if err != nil || loaded.Lifecycle != pair[1] || updated.Revision != loaded.Revision {
				t.Fatalf("transition not authoritative: updated=%#v loaded=%#v err=%v", updated, loaded, err)
			}
		})
	}
	for _, from := range all {
		for _, to := range all {
			if validSet[[2]workerdomain.Lifecycle{from, to}] {
				continue
			}
			store := New(filepath.Join(t.TempDir(), "home"))
			state, err := store.Ensure(testDefinition("project-one", "backend"))
			if err != nil {
				t.Fatal(err)
			}
			state.Lifecycle = from
			state, err = store.Save(state, state.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Transition(state, to, "reason"); err == nil {
				t.Errorf("invalid transition %s -> %s succeeded", from, to)
			}
		}
	}
}

func TestDefinitionReconciliationNeverWidensPermissions(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "home"))
	definition := testDefinition("project-one", "backend")
	state, err := store.Ensure(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition.Policy.AllowedPaths = []string{"src/**", "docs/**", "infrastructure/**"}
	definition.Policy.ForbiddenPaths = []string{"production/**"}
	definition.Policy.BranchPrefix = "broader/branch"
	definition.Policy.DefaultWorktree = "another"
	reconciled, err := store.Ensure(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reconciled.EffectivePolicy.AllowedPaths, []string{"src/**", "docs/**"}) {
		t.Fatalf("allowed paths widened: %v", reconciled.EffectivePolicy.AllowedPaths)
	}
	if !slices.Equal(reconciled.EffectivePolicy.ForbiddenPaths, []string{"src/secrets/**", "production/**"}) {
		t.Fatalf("forbidden paths not accumulated: %v", reconciled.EffectivePolicy.ForbiddenPaths)
	}
	if reconciled.EffectivePolicy.BranchPrefix != state.EffectivePolicy.BranchPrefix ||
		reconciled.EffectivePolicy.DefaultWorktree != state.EffectivePolicy.DefaultWorktree {
		t.Fatalf("working policy changed: %#v", reconciled.EffectivePolicy)
	}
	again, err := store.Ensure(definition)
	if err != nil || again.Revision != reconciled.Revision {
		t.Fatalf("repeat reconciliation was not deterministic: %#v, %v", again, err)
	}
}

func TestResetOnlySafeTerminalStatesAndIsIdempotent(t *testing.T) {
	for _, lifecycle := range []workerdomain.Lifecycle{workerdomain.LifecycleFailed, workerdomain.LifecycleAwaitingReview} {
		store := New(filepath.Join(t.TempDir(), "home"))
		state, err := store.Ensure(testDefinition("project-one", "backend"))
		if err != nil {
			t.Fatal(err)
		}
		state.Lifecycle = lifecycle
		state.ActiveTaskID, state.ActiveRunID = "task-one", "run-one"
		state.RuntimeSession = &workerdomain.SessionRef{Runtime: "codex", SessionID: "session-one"}
		state, err = store.Save(state, state.Revision)
		if err != nil {
			t.Fatal(err)
		}
		reset, err := store.Reset(state)
		if err != nil || reset.Lifecycle != workerdomain.LifecycleIdle || reset.ActiveTaskID != "" || reset.RuntimeSession != nil {
			t.Fatalf("reset = %#v, %v", reset, err)
		}
		again, err := store.Reset(reset)
		if err != nil || again.Revision != reset.Revision {
			t.Fatalf("repeated reset = %#v, %v", again, err)
		}
	}
	for _, lifecycle := range []workerdomain.Lifecycle{
		workerdomain.LifecycleStarting, workerdomain.LifecycleExecuting, workerdomain.LifecycleBlocked,
	} {
		store := New(filepath.Join(t.TempDir(), "home"))
		state, err := store.Ensure(testDefinition("project-one", "backend"))
		if err != nil {
			t.Fatal(err)
		}
		state.Lifecycle = lifecycle
		state, err = store.Save(state, state.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Reset(state); !errors.Is(err, ErrUnsafeReset) {
			t.Errorf("reset from %s error = %v", lifecycle, err)
		}
	}
}
