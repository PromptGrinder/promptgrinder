package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndReadEvents(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_events", StatusRunning)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(worker.ID, NewEvent(worker.ID, EventWorkerStarted, SeverityInfo, "Worker started", nil)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(worker.ID, NewEvent(worker.ID, EventWorkerHeartbeat, SeverityDebug, "Worker heartbeat", nil)); err != nil {
		t.Fatal(err)
	}

	result, err := store.ReadEvents(worker.ID, EventFilter{Severity: SeverityDebug})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Type != EventWorkerHeartbeat {
		t.Fatalf("events = %#v", result.Events)
	}
	if result.Events[0].SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", result.Events[0].SchemaVersion, CurrentEventSchemaVersion)
	}
	if result.Events[0].Engine != "codex" {
		t.Fatalf("engine = %q, want codex", result.Events[0].Engine)
	}
	if _, err := json.Marshal(result.Events[0]); err != nil {
		t.Fatalf("event JSON is invalid: %v", err)
	}
}

func TestReadEventsSkipsMalformedLines(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_malformed", StatusRunning)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	path := EventPathForWorker(worker)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{bad json}\n{\"worker_id\":\"wrk_malformed\",\"type\":\"worker.started\",\"severity\":\"info\",\"message\":\"ok\",\"data\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := store.ReadEvents(worker.ID, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %#v", result.Events)
	}
	if result.Events[0].SchemaVersion != 0 {
		t.Fatalf("old event schema_version = %d, want 0", result.Events[0].SchemaVersion)
	}
}

func TestNewEventsIncludeSchemaVersion(t *testing.T) {
	event := NewEvent("wrk_test", EventWorkerStarted, SeverityInfo, "Worker started", nil)
	if event.SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", event.SchemaVersion, CurrentEventSchemaVersion)
	}
}

func TestEventTypeConstants(t *testing.T) {
	types := EventTypes()
	want := []string{
		EventWorkerCreated,
		EventWorkerStarted,
		EventWorkerHeartbeat,
		EventWorkerSucceeded,
		EventWorkerFailed,
		EventWorkerCancelled,
		EventWorkerReconciled,
		EventWorkerPruned,
		EventEngineSelected,
		EventEngineValidated,
		EventEngineValidationFailed,
		EventEngineCommandBuilt,
		EventEngineStarted,
		EventEngineFinished,
		EventEngineSucceeded,
		EventEngineFailed,
		EventEngineTimeout,
		EventEngineResultParsed,
		EventTerminalLaunchRequested,
		EventTerminalLaunchSucceeded,
		EventTerminalLaunchFailed,
	}
	if len(types) != len(want) {
		t.Fatalf("types = %#v, want %#v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("types = %#v, want %#v", types, want)
		}
		if !ValidEventType(want[i]) {
			t.Fatalf("event type %q is not valid", want[i])
		}
	}
	if ValidEventType("worker.typo") {
		t.Fatal("unexpected valid typo event")
	}
}

func TestGlobalEventAppendAndRead(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_global", StatusRunning)
	if err := store.Save(worker); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(worker.ID, NewEvent(worker.ID, EventWorkerStarted, SeverityInfo, "Worker started", nil)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(worker.ID, NewEvent(worker.ID, EventWorkerHeartbeat, SeverityDebug, "Worker heartbeat", nil)); err != nil {
		t.Fatal(err)
	}

	result, err := store.ReadGlobalEvents(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Type != EventWorkerStarted {
		t.Fatalf("global events = %#v", result.Events)
	}
}

func TestGlobalEventSurvivesPrune(t *testing.T) {
	store := NewStore(t.TempDir())
	worker := testWorker(store, "wrk_prune_global", StatusSucceeded)
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
	result, err := store.ReadGlobalEvents(EventFilter{Type: EventWorkerPruned})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("global prune events = %#v", result.Events)
	}
	if result.Events[0].Data["worker_id"] != worker.ID || result.Events[0].Data["status"] != StatusSucceeded {
		t.Fatalf("prune data = %#v", result.Events[0].Data)
	}
}
