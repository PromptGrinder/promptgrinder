package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	CurrentEventSchemaVersion = 1

	SeverityDebug   = "debug"
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

const (
	EventWorkerCreated           = "worker.created"
	EventWorkerStarted           = "worker.started"
	EventWorkerHeartbeat         = "worker.heartbeat"
	EventWorkerSucceeded         = "worker.succeeded"
	EventWorkerFailed            = "worker.failed"
	EventWorkerCancelled         = "worker.cancelled"
	EventWorkerReconciled        = "worker.reconciled"
	EventWorkerPruned            = "worker.pruned"
	EventEngineSelected          = "engine.selected"
	EventEngineValidated         = "engine.validated"
	EventEngineValidationFailed  = "engine.validation_failed"
	EventEngineCommandBuilt      = "engine.command_built"
	EventEngineStarted           = "engine.started"
	EventEngineFinished          = "engine.finished"
	EventEngineSucceeded         = "engine.succeeded"
	EventEngineFailed            = "engine.failed"
	EventEngineTimeout           = "engine.timeout"
	EventEngineResultParsed      = "engine.result_parsed"
	EventTerminalLaunchRequested = "terminal.launch_requested"
	EventTerminalLaunchSucceeded = "terminal.launch_succeeded"
	EventTerminalLaunchFailed    = "terminal.launch_failed"
)

type Event struct {
	SchemaVersion int            `json:"schema_version"`
	Timestamp     time.Time      `json:"timestamp"`
	WorkerID      string         `json:"worker_id"`
	Engine        string         `json:"engine,omitempty"`
	Type          string         `json:"type"`
	Severity      string         `json:"severity"`
	Message       string         `json:"message"`
	Data          map[string]any `json:"data"`
}

type EventFilter struct {
	Type     string
	Severity string
	Tail     int
}

type EventReadResult struct {
	Events   []Event
	Warnings []string
}

func NewEvent(workerID, eventType, severity, message string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{
		SchemaVersion: CurrentEventSchemaVersion,
		Timestamp:     time.Now().UTC(),
		WorkerID:      workerID,
		Type:          eventType,
		Severity:      severity,
		Message:       message,
		Data:          data,
	}
}

func (s Store) EventPath(workerID string) string {
	return filepath.Join(s.WorkerDir(workerID), "events.jsonl")
}

func (s Store) GlobalEventPath() string {
	return filepath.Join(filepath.Dir(s.WorkersDir), "events.jsonl")
}

func EventPathForWorker(worker Worker) string {
	return filepath.Join(filepath.Dir(worker.RecordPath), "events.jsonl")
}

func GlobalEventPathForWorker(worker Worker) string {
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(worker.RecordPath))), "events.jsonl")
}

func (s Store) AppendEvent(workerID string, event Event) error {
	worker, err := s.Load(workerID)
	if err != nil {
		return err
	}
	return AppendEventForWorker(worker, event)
}

func AppendEventForWorker(worker Worker, event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = CurrentEventSchemaVersion
	}
	if event.WorkerID == "" {
		event.WorkerID = worker.ID
	}
	if event.Engine == "" {
		event.Engine = worker.Engine
	}
	if event.Data == nil {
		event.Data = map[string]any{}
	}
	if err := appendEventFile(EventPathForWorker(worker), event); err != nil {
		return err
	}
	if isGlobalEvent(event.Type) {
		return appendEventFile(GlobalEventPathForWorker(worker), event)
	}
	return nil
}

func appendEventFile(path string, event Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (s Store) ReadEvents(workerID string, filter EventFilter) (EventReadResult, error) {
	worker, err := s.Load(workerID)
	if err != nil {
		return EventReadResult{}, err
	}
	return readEventFile(EventPathForWorker(worker), filter)
}

func (s Store) ReadGlobalEvents(filter EventFilter) (EventReadResult, error) {
	return readEventFile(s.GlobalEventPath(), filter)
}

func readEventFile(path string, filter EventFilter) (EventReadResult, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return EventReadResult{}, nil
	}
	if err != nil {
		return EventReadResult{}, err
	}
	defer file.Close()

	result := EventReadResult{}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped malformed event line %d", lineNumber))
			continue
		}
		if filter.Type != "" && event.Type != filter.Type {
			continue
		}
		if filter.Severity != "" && event.Severity != filter.Severity {
			continue
		}
		result.Events = append(result.Events, event)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if filter.Tail > 0 && len(result.Events) > filter.Tail {
		result.Events = result.Events[len(result.Events)-filter.Tail:]
	}
	return result, nil
}

func (s Store) LatestEvent(workerID string) (Event, bool, error) {
	result, err := s.ReadEvents(workerID, EventFilter{Tail: 1})
	if err != nil {
		return Event{}, false, err
	}
	if len(result.Events) == 0 {
		return Event{}, false, nil
	}
	return result.Events[0], true, nil
}

func isGlobalEvent(eventType string) bool {
	switch eventType {
	case EventWorkerCreated,
		EventWorkerStarted,
		EventWorkerSucceeded,
		EventWorkerFailed,
		EventWorkerCancelled,
		EventWorkerReconciled,
		EventWorkerPruned,
		EventEngineFinished,
		EventTerminalLaunchFailed,
		EventEngineTimeout:
		return true
	default:
		return false
	}
}

func EventTypes() []string {
	return []string{
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
}

func ValidEventType(value string) bool {
	if value == "" {
		return true
	}
	for _, eventType := range EventTypes() {
		if value == eventType {
			return true
		}
	}
	return false
}

func Severities() []string {
	return []string{
		SeverityDebug,
		SeverityInfo,
		SeverityWarning,
		SeverityError,
	}
}

func ValidSeverity(value string) bool {
	if value == "" {
		return true
	}
	for _, severity := range Severities() {
		if value == severity {
			return true
		}
	}
	return false
}
