package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerPersistenceListAndPrune(t *testing.T) {
	store := NewStore(t.TempDir())
	tokensIn := int64(12)
	cost := 0.25
	worker := Worker{
		ID:             "wrk_test",
		RecordPath:     store.RecordPath("wrk_test"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_test"), "prompt.md"),
		Engine:         "codex",
		Status:         "succeeded",
		LogPath:        filepath.Join(store.WorkerDir("wrk_test"), "worker.log"),
		Metadata:       map[string]any{"priority": "high"},
		ResolvedMetadata: map[string]any{
			"engine": map[string]any{"name": "codex", "sandbox": "workspace-write"},
		},
		Capabilities: map[string]any{"supports_model": true},
		EngineResult: &EngineResult{
			Summary:      "completed",
			SessionID:    "session-1",
			TokensInput:  &tokensIn,
			Cost:         &cost,
			CostCurrency: "USD",
		},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("wrk_test")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != worker.ID || loaded.Metadata["priority"] != "high" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if loaded.EventPath != store.EventPath(worker.ID) {
		t.Fatalf("event_path = %q, want %q", loaded.EventPath, store.EventPath(worker.ID))
	}
	if loaded.ResolvedMetadata["engine"] == nil || loaded.Capabilities == nil {
		t.Fatalf("new metadata fields were not persisted: %#v", loaded)
	}
	if loaded.EngineResult == nil || loaded.EngineResult.TokensInput == nil || *loaded.EngineResult.TokensInput != 12 || loaded.EngineResult.Cost == nil {
		t.Fatalf("engine_result = %#v", loaded.EngineResult)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != worker.ID {
		t.Fatalf("listed = %#v", listed)
	}
	count, err := store.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	listed, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("listed = %#v, want empty", listed)
	}
}

func TestLoadLegacyWorkerRecordWithoutV2Fields(t *testing.T) {
	store := NewStore(t.TempDir())
	path := store.RecordPath("wrk_legacy")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
  "id": "wrk_legacy",
  "record_path": "` + path + `",
  "repository_path": "/repo",
  "task_path": "/repo/task.md",
  "prompt_path": "/state/workers/wrk_legacy/prompt.md",
  "engine": "codex",
  "status": "succeeded",
  "log_path": "/state/workers/wrk_legacy/worker.log",
  "metadata": {"priority": "high"}
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("wrk_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "wrk_legacy" || loaded.Metadata["priority"] != "high" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if loaded.EventPath != "" || loaded.ResolvedMetadata != nil || loaded.EngineResult != nil {
		t.Fatalf("legacy optional fields should remain absent on load: %#v", loaded)
	}
}

func TestPruneSkipsRunningWorkers(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := Worker{
		ID:             "wrk_running",
		RecordPath:     store.RecordPath("wrk_running"),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir("wrk_running"), "prompt.md"),
		Engine:         "codex",
		Status:         "running",
		LogPath:        filepath.Join(store.WorkerDir("wrk_running"), "worker.log"),
		Metadata:       map[string]any{},
	}
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}

	count, err := store.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if _, err := store.Load("wrk_running"); err != nil {
		t.Fatalf("running worker was pruned: %v", err)
	}
}

func TestMarkTerminalClosed(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_terminal", StatusSucceeded)
	worker.TerminalAdapter = "terminal"
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	closed, err := store.MarkTerminalClosed(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.TerminalClosedAt == nil {
		t.Fatalf("closed = %#v", closed)
	}
}

func TestLifecycleTransitions(t *testing.T) {
	store := NewStore(t.TempDir())
	running := testWorker(store, "wrk_running", StatusRunning)
	if err := store.Save(running); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Complete(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusSucceeded || completed.FinishTime == nil {
		t.Fatalf("completed = %#v", completed)
	}
	assertLatestEvent(t, store, completed.ID, EventWorkerSucceeded)

	waiting := testWorker(store, "wrk_waiting", StatusWaiting)
	if err := store.Save(waiting); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Fail(waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.FinishTime == nil {
		t.Fatalf("failed = %#v", failed)
	}
	assertLatestEvent(t, store, failed.ID, EventWorkerFailed)

	starting := testWorker(store, "wrk_starting", StatusStarting)
	if err := store.Save(starting); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Cancel(starting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || cancelled.FinishTime == nil {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	assertLatestEvent(t, store, cancelled.ID, EventWorkerCancelled)
}

func TestInvalidLifecycleTransition(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_done", StatusSucceeded)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Fail(worker.ID); err == nil {
		t.Fatal("expected invalid transition error")
	}
}

func TestMarkFinishedDoesNotOverwriteTerminalManualState(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_cancelled", StatusRunning)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Cancel(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}

	if err := store.MarkFinished(cancelled.RecordPath, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", loaded.Status)
	}
	if loaded.ExitCode != nil {
		t.Fatalf("exit code = %v, want nil", *loaded.ExitCode)
	}
	if loaded.FinishTime == nil || !loaded.FinishTime.Equal(*cancelled.FinishTime) {
		t.Fatalf("finish time changed: got %v want %v", loaded.FinishTime, cancelled.FinishTime)
	}
}

func TestPruneIncludesCancelled(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_cancelled", StatusCancelled)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}

	count, err := store.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestReconcileIdentifiesStaleWorkersWithoutMutation(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	stale := testWorker(store, "wrk_stale", StatusRunning)
	stale.LastSeenAt = ptrTime(now.Add(-25 * time.Hour))
	fresh := testWorker(store, "wrk_fresh", StatusRunning)
	fresh.LastSeenAt = ptrTime(now.Add(-1 * time.Hour))
	done := testWorker(store, "wrk_done", StatusSucceeded)
	done.LastSeenAt = ptrTime(now.Add(-48 * time.Hour))
	writeWorkerRaw(t, stale)
	writeWorkerRaw(t, fresh)
	writeWorkerRaw(t, done)

	workers, err := store.Reconcile(24*time.Hour, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].ID != stale.ID {
		t.Fatalf("workers = %#v", workers)
	}
	loaded, err := store.Load(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusRunning {
		t.Fatalf("status = %q, want running", loaded.Status)
	}
	assertLatestEvent(t, store, stale.ID, EventWorkerReconciled)
	event, _, err := store.LatestEvent(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if event.Data["dry_run"] != true || event.Data["mark_failed"] != false || event.Data["stale_count"] == nil {
		t.Fatalf("reconcile data = %#v", event.Data)
	}
}

func TestReconcileMarkFailedMutatesStaleWorkers(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	stale := testWorker(store, "wrk_stale", StatusWaiting)
	stale.LastSeenAt = ptrTime(now.Add(-25 * time.Hour))
	writeWorkerRaw(t, stale)

	workers, err := store.Reconcile(24*time.Hour, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].Status != StatusFailed {
		t.Fatalf("workers = %#v", workers)
	}
	loaded, err := store.Load(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusFailed || loaded.FinishTime == nil {
		t.Fatalf("loaded = %#v", loaded)
	}
	assertLatestEvent(t, store, stale.ID, EventWorkerReconciled)
	event, _, err := store.LatestEvent(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if event.Data["dry_run"] != false || event.Data["mark_failed"] != true || event.Data["stale_count"] == nil {
		t.Fatalf("reconcile data = %#v", event.Data)
	}
}

func TestUnknownWorkerError(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Load("missing"); err == nil {
		t.Fatal("expected missing worker error")
	}
	if _, err := store.Complete("missing"); err == nil {
		t.Fatal("expected missing worker transition error")
	}
}

func TestHeartbeatUpdatesLastSeenWithoutChangingStatus(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_heartbeat", StatusRunning)
	old := time.Now().UTC().Add(-1 * time.Hour)
	worker.LastSeenAt = &old
	writeWorkerRaw(t, worker)

	updated, err := store.Heartbeat(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusRunning {
		t.Fatalf("status = %q, want running", updated.Status)
	}
	if updated.LastSeenAt == nil || !updated.LastSeenAt.After(old) {
		t.Fatalf("last_seen_at = %v, want after %v", updated.LastSeenAt, old)
	}
	assertLatestEvent(t, store, worker.ID, EventWorkerHeartbeat)
}

func TestWorkerStateUsesOwnerOnlyPermissions(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_private", StatusRunning)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.WorkersDir, store.WorkerDir(worker.ID), worker.RecordPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o, want owner-only", path, info.Mode().Perm())
		}
	}
}

func testWorker(store Store, id, status string) Worker {
	now := time.Now().UTC()
	return Worker{
		ID:             id,
		RecordPath:     store.RecordPath(id),
		RepositoryPath: "/repo",
		TaskPath:       "/repo/task.md",
		PromptPath:     filepath.Join(store.WorkerDir(id), "prompt.md"),
		Engine:         "codex",
		Status:         status,
		StartTime:      &now,
		LastSeenAt:     &now,
		LogPath:        filepath.Join(store.WorkerDir(id), "worker.log"),
		Metadata:       map[string]any{},
	}
}

func writeWorkerRaw(t *testing.T, worker Worker) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(worker.RecordPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(worker, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker.RecordPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func assertLatestEvent(t *testing.T, store Store, workerID, eventType string) {
	t.Helper()
	event, ok, err := store.LatestEvent(workerID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("no latest event for %s", workerID)
	}
	if event.Type != eventType {
		t.Fatalf("event type = %q, want %q", event.Type, eventType)
	}
}
