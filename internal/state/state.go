package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	StatusCreated      = "created"
	StatusPending      = "pending"
	StatusStarting     = "starting"
	StatusRunning      = "running"
	StatusWaiting      = "waiting"
	StatusSucceeded    = "succeeded"
	StatusFailed       = "failed"
	StatusCancelled    = "cancelled"
	StatusLaunchFailed = "launch_failed"
)

type Worker struct {
	ID               string         `json:"id"`
	RecordPath       string         `json:"record_path"`
	RepositoryPath   string         `json:"repository_path"`
	TaskPath         string         `json:"task_path"`
	PromptPath       string         `json:"prompt_path"`
	Engine           string         `json:"engine"`
	RequestedEngine  string         `json:"requested_engine,omitempty"`
	EngineOverride   string         `json:"engine_override,omitempty"`
	EngineOverridden bool           `json:"engine_overridden,omitempty"`
	EngineVersion    string         `json:"engine_version,omitempty"`
	EngineExecutable string         `json:"engine_executable,omitempty"`
	ProcessID        int            `json:"process_id,omitempty"`
	ProcessGroupID   int            `json:"process_group_id,omitempty"`
	Status           string         `json:"status"`
	CreatedAt        *time.Time     `json:"created_at"`
	StartTime        *time.Time     `json:"start_time"`
	FinishTime       *time.Time     `json:"finish_time"`
	LastSeenAt       *time.Time     `json:"last_seen_at"`
	LogPath          string         `json:"log_path"`
	SummaryPath      string         `json:"summary_path,omitempty"`
	EventPath        string         `json:"event_path,omitempty"`
	TerminalCommand  string         `json:"terminal_command"`
	TerminalAdapter  string         `json:"terminal_adapter"`
	CloseOnFinish    bool           `json:"close_on_finish,omitempty"`
	CloseOnFailure   bool           `json:"close_on_failure,omitempty"`
	TerminalClosedAt *time.Time     `json:"terminal_closed_at,omitempty"`
	Metadata         map[string]any `json:"metadata"`
	ResolvedMetadata map[string]any `json:"resolved_metadata,omitempty"`
	Capabilities     any            `json:"capabilities,omitempty"`
	ExitCode         *int           `json:"exit_code,omitempty"`
	EngineResult     *EngineResult  `json:"engine_result,omitempty"`
}

type EngineResult struct {
	Summary          string         `json:"summary,omitempty"`
	SessionID        string         `json:"session_id,omitempty"`
	CompletionStatus string         `json:"completion_status,omitempty"`
	NextPromptSafe   *bool          `json:"next_prompt_safe,omitempty"`
	CompletionReason string         `json:"completion_reason,omitempty"`
	EngineExitCode   *int           `json:"engine_exit_code,omitempty"`
	TokensInput      *int64         `json:"tokens_input,omitempty"`
	TokensOutput     *int64         `json:"tokens_output,omitempty"`
	TokensTotal      *int64         `json:"tokens_total,omitempty"`
	Cost             *float64       `json:"cost,omitempty"`
	CostCurrency     string         `json:"cost_currency,omitempty"`
	Diagnostics      map[string]any `json:"diagnostics,omitempty"`
}

func (r EngineResult) Empty() bool {
	return r.Summary == "" &&
		r.SessionID == "" &&
		r.CompletionStatus == "" &&
		r.NextPromptSafe == nil &&
		r.CompletionReason == "" &&
		r.EngineExitCode == nil &&
		r.TokensInput == nil &&
		r.TokensOutput == nil &&
		r.TokensTotal == nil &&
		r.Cost == nil &&
		r.CostCurrency == "" &&
		len(r.Diagnostics) == 0
}

// OrderedCompletionError validates the completion contract used by ordered
// workflows. Independent runs may continue to use the looser engine contract.
func (r EngineResult) OrderedCompletionError() error {
	if r.CompletionReason != "" {
		return errors.New(r.CompletionReason)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("empty final answer")
	}
	if r.CompletionStatus != "PASS" {
		if r.CompletionStatus == "" {
			return fmt.Errorf("missing or malformed STATUS field")
		}
		return fmt.Errorf("STATUS is %s, not PASS", r.CompletionStatus)
	}
	if r.NextPromptSafe == nil {
		return fmt.Errorf("missing or malformed NEXT_PROMPT_SAFE field")
	}
	if !*r.NextPromptSafe {
		return fmt.Errorf("NEXT_PROMPT_SAFE is no")
	}
	return nil
}

func (r EngineResult) RejectsContinuation() bool {
	return r.CompletionStatus == "BLOCKED" || (r.NextPromptSafe != nil && !*r.NextPromptSafe)
}

type Store struct {
	WorkersDir string
}

func NewStore(homeDir string) Store {
	return Store{WorkersDir: filepath.Join(homeDir, "workers")}
}

func (s Store) Ensure() error {
	if err := os.MkdirAll(s.WorkersDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.WorkersDir, 0o700)
}

func (s Store) WorkerDir(id string) string {
	return filepath.Join(s.WorkersDir, id)
}

func (s Store) RecordPath(id string) string {
	return filepath.Join(s.WorkerDir(id), "worker.json")
}

func (s Store) Save(worker Worker) error {
	now := time.Now().UTC()
	if worker.CreatedAt == nil {
		worker.CreatedAt = &now
	}
	if worker.EventPath == "" && worker.RecordPath != "" {
		worker.EventPath = EventPathForWorker(worker)
	}
	worker.LastSeenAt = &now
	if err := os.MkdirAll(filepath.Dir(worker.RecordPath), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(worker.RecordPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(worker, "", "  ")
	if err != nil {
		return err
	}
	tmp := worker.RecordPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, worker.RecordPath)
}

func (s Store) SetProcess(recordPath string, pid, pgid int) error {
	worker, err := s.LoadPath(recordPath)
	if err != nil {
		return err
	}
	worker.ProcessID = pid
	worker.ProcessGroupID = pgid
	return s.Save(worker)
}

func (s Store) ClearProcess(recordPath string, pid int) error {
	worker, err := s.LoadPath(recordPath)
	if err != nil {
		return err
	}
	if worker.ProcessID != pid {
		return nil
	}
	worker.ProcessID = 0
	worker.ProcessGroupID = 0
	return s.Save(worker)
}

func (s Store) Load(id string) (Worker, error) {
	worker, err := s.LoadPath(s.RecordPath(id))
	if os.IsNotExist(err) {
		return Worker{}, fmt.Errorf("worker not found: %s", id)
	}
	return worker, err
}

func (s Store) LoadPath(path string) (Worker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Worker{}, err
	}
	var worker Worker
	if err := json.Unmarshal(data, &worker); err != nil {
		return Worker{}, err
	}
	return worker, nil
}

func (s Store) List() ([]Worker, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.WorkersDir)
	if err != nil {
		return nil, err
	}
	workers := []Worker{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worker, err := s.Load(entry.Name())
		if err == nil {
			workers = append(workers, worker)
		}
	}
	sort.SliceStable(workers, func(i, j int) bool {
		left, right := workers[i].StartTime, workers[j].StartTime
		if left == nil && right == nil {
			return workers[i].ID > workers[j].ID
		}
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		return left.After(*right)
	})
	return workers, nil
}

func (s Store) Prune() (int, error) {
	if err := s.Ensure(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.WorkersDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worker, err := s.Load(entry.Name())
		if err != nil || !IsPrunableStatus(worker.Status) {
			continue
		}
		_ = AppendEventForWorker(worker, NewEvent(worker.ID, EventWorkerPruned, SeverityInfo, "Worker pruned", map[string]any{"worker_id": worker.ID, "status": worker.Status}))
		if err := os.RemoveAll(filepath.Join(s.WorkersDir, entry.Name())); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func IsPrunableStatus(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusLaunchFailed:
		return true
	default:
		return false
	}
}

func IsTerminalStatus(status string) bool {
	return IsPrunableStatus(status)
}

func IsStaleCandidateStatus(status string) bool {
	switch status {
	case StatusRunning, StatusWaiting, StatusStarting:
		return true
	default:
		return false
	}
}

func (s Store) MarkStarted(id, terminalCommand, terminalAdapter string) (Worker, error) {
	worker, err := s.Load(id)
	if err != nil {
		return Worker{}, err
	}
	now := time.Now().UTC()
	worker.Status = StatusRunning
	worker.StartTime = &now
	worker.TerminalCommand = terminalCommand
	worker.TerminalAdapter = terminalAdapter
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	_ = AppendEventForWorker(worker, NewEvent(worker.ID, EventWorkerStarted, SeverityInfo, "Worker started", map[string]any{"terminal_command": terminalCommand, "terminal_adapter": terminalAdapter}))
	return s.Load(worker.ID)
}

func (s Store) MarkTerminalClosed(id string) (Worker, error) {
	worker, err := s.Load(id)
	if err != nil {
		return Worker{}, err
	}
	now := time.Now().UTC()
	worker.TerminalClosedAt = &now
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	return s.Load(worker.ID)
}

func (s Store) MarkFinished(recordPath string, exitCode int) error {
	worker, err := s.LoadPath(recordPath)
	if err != nil {
		return err
	}
	if IsTerminalStatus(worker.Status) {
		return nil
	}
	now := time.Now().UTC()
	worker.FinishTime = &now
	worker.ExitCode = &exitCode
	if exitCode == 0 {
		worker.Status = StatusSucceeded
	} else {
		worker.Status = StatusFailed
	}
	if err := s.Save(worker); err != nil {
		return err
	}
	eventType := EventWorkerFailed
	severity := SeverityError
	message := "Worker failed"
	if exitCode == 0 {
		eventType = EventWorkerSucceeded
		severity = SeverityInfo
		message = "Worker succeeded"
	}
	data := map[string]any{"exit_code": exitCode}
	if exitCode != 0 {
		data["reason"] = "engine_exit"
	}
	return AppendEventForWorker(worker, NewEvent(worker.ID, eventType, severity, message, data))
}

func (s Store) Heartbeat(id string) (Worker, error) {
	worker, err := s.Load(id)
	if err != nil {
		return Worker{}, err
	}
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	_ = AppendEventForWorker(worker, NewEvent(worker.ID, EventWorkerHeartbeat, SeverityDebug, "Worker heartbeat", nil))
	return s.Load(id)
}

func (s Store) Complete(id string) (Worker, error) {
	return s.transition(id, StatusSucceeded, allowed(StatusRunning, StatusWaiting), nil)
}

func (s Store) Fail(id string) (Worker, error) {
	exitCode := 1
	return s.transition(id, StatusFailed, allowed(StatusRunning, StatusWaiting), &exitCode)
}

func (s Store) Cancel(id string) (Worker, error) {
	return s.transition(id, StatusCancelled, allowed(StatusPending, StatusStarting, StatusRunning, StatusWaiting), nil)
}

func (s Store) MarkFailed(id string) (Worker, error) {
	exitCode := 1
	return s.transition(id, StatusFailed, allowed(StatusRunning, StatusWaiting, StatusStarting), &exitCode)
}

func (s Store) Reconcile(threshold time.Duration, markFailed bool, now time.Time) ([]Worker, error) {
	workers, err := s.List()
	if err != nil {
		return nil, err
	}
	candidates := []Worker{}
	for _, worker := range workers {
		if !IsStaleCandidateStatus(worker.Status) {
			continue
		}
		lastSeen := worker.LastSeenAt
		if lastSeen == nil {
			lastSeen = worker.StartTime
		}
		if lastSeen == nil {
			continue
		}
		if now.Sub(*lastSeen) < threshold {
			continue
		}
		if markFailed && processGroupAlive(worker.ProcessGroupID) {
			continue
		}
		candidates = append(candidates, worker)
	}
	stale := []Worker{}
	staleCount := len(candidates)
	for _, worker := range candidates {
		if markFailed {
			updated, err := s.MarkFailed(worker.ID)
			if err != nil {
				return stale, err
			}
			_ = AppendEventForWorker(updated, NewEvent(updated.ID, EventWorkerReconciled, SeverityWarning, "Stale worker reconciled as failed", map[string]any{"threshold": threshold.String(), "dry_run": false, "mark_failed": true, "stale_count": staleCount}))
			stale = append(stale, updated)
			continue
		}
		_ = AppendEventForWorker(worker, NewEvent(worker.ID, EventWorkerReconciled, SeverityWarning, "Stale worker found", map[string]any{"threshold": threshold.String(), "dry_run": true, "mark_failed": false, "stale_count": staleCount}))
		stale = append(stale, worker)
	}
	return stale, nil
}

func processGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (s Store) transition(id, next string, allowed map[string]struct{}, exitCode *int) (Worker, error) {
	worker, err := s.Load(id)
	if err != nil {
		if os.IsNotExist(err) {
			return Worker{}, fmt.Errorf("worker not found: %s", id)
		}
		return Worker{}, err
	}
	if _, ok := allowed[worker.Status]; !ok {
		return Worker{}, fmt.Errorf("invalid worker transition: %s -> %s for %s", worker.Status, next, id)
	}
	now := time.Now().UTC()
	worker.Status = next
	worker.FinishTime = &now
	worker.ExitCode = exitCode
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	eventType, severity, message := transitionEvent(next)
	data := map[string]any{}
	if exitCode != nil {
		data["exit_code"] = *exitCode
		data["reason"] = "manual"
	}
	_ = AppendEventForWorker(worker, NewEvent(worker.ID, eventType, severity, message, data))
	return worker, nil
}

func transitionEvent(status string) (string, string, string) {
	switch status {
	case StatusSucceeded:
		return EventWorkerSucceeded, SeverityInfo, "Worker succeeded"
	case StatusFailed:
		return EventWorkerFailed, SeverityError, "Worker failed"
	case StatusCancelled:
		return EventWorkerCancelled, SeverityWarning, "Worker cancelled"
	default:
		return "worker.updated", SeverityInfo, "Worker updated"
	}
}

func allowed(statuses ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, status := range statuses {
		out[status] = struct{}{}
	}
	return out
}
