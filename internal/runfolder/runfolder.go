package runfolder

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/markdown"
	"promptgrinder/internal/repository"
	"promptgrinder/internal/state"
)

type PromptType string

const (
	TypeSpecification PromptType = "specification"
	TypeImplement     PromptType = "implement"
	TypeTest          PromptType = "test"
	TypeVerify        PromptType = "verify"
	TypeReview        PromptType = "review"
	TypeUnknown       PromptType = "unknown"
)

type Options struct {
	Resume                  bool
	Fresh                   bool
	Restart                 bool
	NoResume                bool
	Checkpoint              bool
	CommitEach              bool
	RequireCleanGit         bool
	AllowConcurrentWorktree bool
	RepoPath                string
	Template                string
	EngineOverride          string
	IncludeSpecification    bool
	HomeDir                 string
	BaseConfig              config.Config
	UseRepoConfig           bool
	Progress                func(ProgressEvent)
	SupervisorID            string
	SupervisorLogPath       string
	ExecutionPolicy         ExecutionPolicy
	Notifier                Notifier
}

type Notification struct {
	Type       string    `json:"type"`
	SequenceID string    `json:"sequence_id"`
	Folder     string    `json:"folder"`
	Status     string    `json:"status"`
	Timestamp  time.Time `json:"timestamp"`
}

type Notifier interface {
	Notify(Notification) error
}

type LocalNotifier struct{ Path string }

func (n LocalNotifier) Notify(notification Notification) error {
	if err := os.MkdirAll(filepath.Dir(n.Path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(n.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

// ExecutionPolicy controls where run-folder workers are launched. The zero
// value preserves the configured terminal adapter, as required by detached
// supervisors. Foreground runs execute each worker in the invoking process.
type ExecutionPolicy string

const (
	ExecutionConfigured ExecutionPolicy = ""
	ExecutionForeground ExecutionPolicy = "foreground"
)

type Launcher interface {
	LaunchPrompt(path, content, sessionID string) (state.Worker, error)
}

type WaitLauncher interface {
	WaitPrompt(worker state.Worker) (state.Worker, error)
}

type Summary struct {
	Run      RunState
	Sequence *SequenceState
	Prompts  []Prompt
	Started  bool
	Resumed  bool
	Failed   error
	Warnings []string
}

type ProgressEvent struct {
	Type             string           `json:"type"`
	SequenceID       string           `json:"sequence_id"`
	PromptName       string           `json:"prompt_name,omitempty"`
	PromptType       PromptType       `json:"prompt_type,omitempty"`
	Status           string           `json:"status,omitempty"`
	WorkerID         string           `json:"worker_id,omitempty"`
	LogPath          string           `json:"log_path,omitempty"`
	Completed        int              `json:"completed"`
	Total            int              `json:"total"`
	Folder           string           `json:"folder,omitempty"`
	Duration         time.Duration    `json:"duration,omitempty"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	CompletionStatus string           `json:"completion_status,omitempty"`
	NextPromptSafe   *bool            `json:"next_prompt_safe,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	Inventory        []ProgressPrompt `json:"inventory,omitempty"`
}

// ProgressPrompt is the prompt metadata needed to render an ordered run
// inventory. Content is deliberately excluded from progress events.
type ProgressPrompt struct {
	Name   string     `json:"name"`
	Type   PromptType `json:"type"`
	Status string     `json:"status"`
}

type persistedProgressEvent struct {
	Timestamp time.Time `json:"timestamp"`
	ProgressEvent
}

type Prompt struct {
	Path string
	Name string
	Type PromptType
}

type RunState struct {
	RunID     string     `json:"run_id"`
	Folder    string     `json:"folder"`
	Status    string     `json:"status"`
	Current   string     `json:"current"`
	Completed []string   `json:"completed"`
	StartedAt *time.Time `json:"started_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type SequenceState struct {
	SequenceID       string         `json:"sequence_id"`
	Folder           string         `json:"folder"`
	Status           string         `json:"status"`
	Template         string         `json:"template"`
	Engine           string         `json:"engine,omitempty"`
	SessionID        string         `json:"session_id,omitempty"`
	Items            []SequenceItem `json:"items"`
	TokenUsage       TokenUsage     `json:"token_usage"`
	ExecutiveSummary string         `json:"executive_summary"`
	CreatedAt        *time.Time     `json:"created_at,omitempty"`
	StartedAt        *time.Time     `json:"started_at"`
	UpdatedAt        *time.Time     `json:"updated_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	Supervisor       *Supervisor    `json:"supervisor,omitempty"`
	EventPath        string         `json:"event_path,omitempty"`
}

type Supervisor struct {
	ID          string     `json:"id"`
	PID         int        `json:"pid"`
	LogPath     string     `json:"log_path,omitempty"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at"`
	HeartbeatAt *time.Time `json:"heartbeat_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type SequenceItem struct {
	PromptPath       string     `json:"prompt_path"`
	PromptName       string     `json:"prompt_name"`
	ContentHash      string     `json:"content_hash"`
	Status           string     `json:"status"`
	WorkerID         string     `json:"worker_id,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	LogPath          string     `json:"log_path,omitempty"`
	Error            string     `json:"error,omitempty"`
	CompletionStatus string     `json:"completion_status,omitempty"`
	NextPromptSafe   *bool      `json:"next_prompt_safe,omitempty"`
	CompletionReason string     `json:"completion_reason,omitempty"`
}

type TokenUsage struct {
	Available bool `json:"available"`
	Total     int  `json:"total"`
}

type PromptState struct {
	Prompt           string       `json:"prompt"`
	PromptType       PromptType   `json:"prompt_type"`
	Status           string       `json:"status"`
	StartedAt        *time.Time   `json:"started_at"`
	FinishedAt       *time.Time   `json:"finished_at"`
	ExitCode         *int         `json:"exit_code,omitempty"`
	GitSHABefore     string       `json:"git_sha_before"`
	GitSHAAfter      string       `json:"git_sha_after"`
	CommitSHA        string       `json:"commit_sha"`
	FilesChanged     []string     `json:"files_changed"`
	WorkerID         string       `json:"worker_id,omitempty"`
	Error            string       `json:"error,omitempty"`
	CompletionStatus string       `json:"completion_status,omitempty"`
	NextPromptSafe   *bool        `json:"next_prompt_safe,omitempty"`
	CompletionReason string       `json:"completion_reason,omitempty"`
	Worker           state.Worker `json:"-"`
}

func Classify(filename string) PromptType {
	name := filepath.Base(filename)
	switch {
	case matches(name, `^00-specification.*\.md$`):
		return TypeSpecification
	case matches(name, `^\d\d-implement-.+\.md$`):
		return TypeImplement
	case matches(name, `^\d\d-test-.+\.md$`):
		return TypeTest
	case matches(name, `^\d\d-verify-.+\.md$`), matches(name, `^\d\d-final-verify.*\.md$`):
		return TypeVerify
	case matches(name, `^\d\d-review-.+\.md$`):
		return TypeReview
	default:
		return TypeUnknown
	}
}

func Discover(folder string) ([]Prompt, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	prompts := []Prompt{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.IsDir() || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if !matches(name, `^\d\d-.+\.md$`) {
			continue
		}
		promptType := Classify(name)
		if promptType == TypeUnknown {
			continue
		}
		prompts = append(prompts, Prompt{Path: filepath.Join(abs, name), Name: name, Type: promptType})
	}
	sort.SliceStable(prompts, func(i, j int) bool {
		return prompts[i].Name < prompts[j].Name
	})
	return prompts, nil
}

// ResolveSequenceID computes the stable identity used by Run without creating
// or mutating sequence state.
func ResolveSequenceID(folder string, options Options) (string, error) {
	absFolder, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	repoPath := options.RepoPath
	if repoPath == "" {
		repoPath = "."
	}
	repoRoot, err := repository.DetectRoot(repoPath)
	if err != nil {
		return "", err
	}
	prompts, err := Discover(absFolder)
	if err != nil {
		return "", err
	}
	if len(prompts) == 0 {
		return "", fmt.Errorf("no recognized numbered Markdown prompts found in %s", absFolder)
	}
	if options.Template == "" {
		options.Template = "codex"
	}
	sequence, err := buildSequence(absFolder, repoRoot, prompts, options)
	if err != nil {
		return "", err
	}
	return sequence.SequenceID, nil
}

func Run(folder string, options Options, launcher Launcher) (summary Summary, runErr error) {
	if options.Resume && options.Fresh {
		return Summary{}, fmt.Errorf("--resume and --fresh are mutually exclusive")
	}
	if options.Restart && options.NoResume {
		return Summary{}, fmt.Errorf("--restart and --no-resume are mutually exclusive")
	}
	if options.Restart || options.NoResume {
		options.Fresh = true
	}
	if options.Template == "" {
		options.Template = "codex"
	}
	if options.Template != "codex" {
		return Summary{}, fmt.Errorf("unsupported template %q: use codex", options.Template)
	}
	if launcher == nil {
		return Summary{}, fmt.Errorf("run-folder requires an execution backend")
	}
	absFolder, err := filepath.Abs(folder)
	if err != nil {
		return Summary{}, err
	}
	repoPath := options.RepoPath
	if repoPath == "" {
		repoPath = "."
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return Summary{}, err
	}
	repoRoot, err := repository.DetectRoot(absRepoPath)
	if err != nil {
		return Summary{}, err
	}
	options.RepoPath = repoRoot
	prompts, err := Discover(absFolder)
	if err != nil {
		return Summary{}, err
	}
	if len(prompts) == 0 {
		return Summary{}, fmt.Errorf("no Markdown prompts found in %s", absFolder)
	}

	sequenceStore := newSequenceStore(options.HomeDir)
	sequence, sequenceResumed, err := sequenceStore.loadOrCreate(absFolder, repoRoot, prompts, options)
	if err != nil {
		return Summary{}, err
	}
	store := folderStore{Root: filepath.Join(absFolder, ".promptgrinder")}
	runState, resumed, err := store.loadOrCreate(absFolder, options)
	if err != nil {
		return Summary{}, err
	}
	summary = Summary{Run: runState, Sequence: &sequence, Prompts: prompts, Started: true, Resumed: resumed || sequenceResumed}
	if options.HomeDir != "" {
		sequence.EventPath = filepath.Join(options.HomeDir, "events", "sequences", sequence.SequenceID+".jsonl")
	}
	stopSupervisor := startSupervisorHeartbeat(options, &sequence)
	if stopSupervisor != nil {
		defer func() {
			status := "completed"
			if runErr != nil {
				status = "interrupted"
				if sequence.Status == "failed" {
					status = "failed"
				}
			}
			stopSupervisor(status)
			_ = sequenceStore.save(sequence)
			eventType := "notification.success"
			if status != "completed" {
				eventType = "notification.failure"
			}
			emitProgress(options, ProgressEvent{Type: eventType, SequenceID: sequence.SequenceID, Folder: sequence.Folder, Status: sequence.Status, Completed: sequence.Progress().Succeeded, Total: len(sequence.Items)})
			if options.Notifier != nil {
				_ = options.Notifier.Notify(Notification{Type: eventType, SequenceID: sequence.SequenceID, Folder: sequence.Folder, Status: sequence.Status, Timestamp: time.Now().UTC()})
			}
		}()
		if err := sequenceStore.save(sequence); err != nil {
			return summary, err
		}
	}
	inventory := make([]ProgressPrompt, 0, len(prompts))
	itemStatuses := make(map[string]string, len(sequence.Items))
	for _, item := range sequence.Items {
		itemStatuses[item.PromptName] = item.Status
	}
	for _, prompt := range prompts {
		status := itemStatuses[prompt.Name]
		if status == "" || status == "running" {
			status = "pending"
		}
		inventory = append(inventory, ProgressPrompt{Name: prompt.Name, Type: prompt.Type, Status: status})
	}
	emitProgress(options, ProgressEvent{Type: "run.started", SequenceID: sequence.SequenceID, Folder: folder, Inventory: inventory, Completed: sequence.Progress().Succeeded, Total: len(prompts)})
	if err := store.ensure(); err != nil {
		return summary, err
	}
	specContext, err := readSpecificationContext(prompts)
	if err != nil {
		return summary, err
	}
	completed := map[string]bool{}
	if sequenceResumed {
		completed = setFromSlice(runState.Completed)
		for _, item := range sequence.Items {
			if item.Status == "succeeded" || item.Status == "skipped" {
				completed[item.PromptName] = true
			}
		}
	}
	for _, prompt := range prompts {
		if completed[prompt.Name] {
			continue
		}
		emitProgress(options, ProgressEvent{Type: "prompt.started", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "running", Completed: len(runState.Completed), Total: len(prompts)})
		if prompt.Type == TypeUnknown {
			continue
		}
		if prompt.Type == TypeSpecification && !options.IncludeSpecification {
			now := time.Now().UTC()
			promptState := PromptState{Prompt: prompt.Name, PromptType: prompt.Type, Status: "completed", StartedAt: &now, FinishedAt: &now}
			if err := store.savePrompt(promptState); err != nil {
				return summary, err
			}
			sequence.mark(prompt.Name, "skipped", state.Worker{}, nil, "")
			sequence.refreshSummary()
			_ = sequenceStore.save(sequence)
			_ = store.saveSummary(sequence)
			runState.Completed = append(runState.Completed, prompt.Name)
			runState.Current = prompt.Name
			runState.Status = "running"
			runState.touch()
			if err := store.saveRun(runState); err != nil {
				return summary, err
			}
			completed[prompt.Name] = true
			emitProgress(options, ProgressEvent{Type: "prompt.skipped", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "skipped", Completed: len(runState.Completed), Total: len(prompts)})
			continue
		}
		sequence.mark(prompt.Name, "running", state.Worker{}, nil, "")
		sequence.refreshSummary()
		if err := sequenceStore.save(sequence); err != nil {
			return summary, err
		}
		promptState, err := runPrompt(repoRoot, prompt, specContext, sequence.SessionID, options, launcher)
		if err != nil {
			promptState.Status = "failed"
			promptState.Error = err.Error()
			if promptState.Worker.ID == "" {
				promptState.Worker = state.Worker{ID: promptState.WorkerID, ExitCode: promptState.ExitCode}
			}
			sequence.mark(prompt.Name, "failed", promptState.Worker, promptState.FinishedAt, err.Error())
			finished := time.Now().UTC()
			sequence.FinishedAt = &finished
			sequence.refreshSummary()
			_ = sequenceStore.save(sequence)
			_ = store.saveSummary(sequence)
			runState.Status = "failed"
			runState.Current = prompt.Name
			runState.touch()
			_ = store.savePrompt(promptState)
			_ = store.saveRun(runState)
			summary.Run = runState
			summary.Failed = err
			emitProgress(options, ProgressEvent{Type: "prompt.failed", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "failed", WorkerID: promptState.WorkerID, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Reason: promptState.CompletionReason, Completed: len(runState.Completed), Total: len(prompts)})
			return summary, err
		}
		if err := store.savePrompt(promptState); err != nil {
			return summary, err
		}
		if promptState.Worker.EngineResult != nil && promptState.Worker.EngineResult.SessionID != "" {
			sequence.SessionID = promptState.Worker.EngineResult.SessionID
		}
		sequence.mark(prompt.Name, "succeeded", promptState.Worker, promptState.FinishedAt, "")
		sequence.refreshSummary()
		_ = sequenceStore.save(sequence)
		_ = store.saveSummary(sequence)
		runState.Completed = append(runState.Completed, prompt.Name)
		runState.Current = prompt.Name
		runState.Status = "running"
		runState.touch()
		if err := store.saveRun(runState); err != nil {
			return summary, err
		}
		completed[prompt.Name] = true
		emitProgress(options, ProgressEvent{Type: "prompt.succeeded", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "succeeded", WorkerID: promptState.WorkerID, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Completed: len(runState.Completed), Total: len(prompts)})
	}
	runState.Status = "completed"
	runState.Current = ""
	runState.touch()
	if err := store.saveRun(runState); err != nil {
		return summary, err
	}
	sequence.Status = "completed"
	finished := time.Now().UTC()
	sequence.FinishedAt = &finished
	sequence.refreshSummary()
	sequence.touch()
	if err := sequenceStore.save(sequence); err != nil {
		return summary, err
	}
	if err := store.saveSummary(sequence); err != nil {
		return summary, err
	}
	summary.Run = runState
	summary.Sequence = &sequence
	emitProgress(options, ProgressEvent{Type: "run.completed", SequenceID: sequence.SequenceID, Completed: len(runState.Completed), Total: len(prompts)})
	return summary, nil
}

func promptDuration(prompt PromptState) time.Duration {
	if prompt.StartedAt == nil || prompt.FinishedAt == nil {
		return 0
	}
	return prompt.FinishedAt.Sub(*prompt.StartedAt)
}

func emitProgress(options Options, event ProgressEvent) {
	if options.HomeDir != "" && event.SequenceID != "" {
		eventsDir := filepath.Join(options.HomeDir, "events", "sequences")
		if err := os.MkdirAll(eventsDir, 0o755); err == nil {
			path := filepath.Join(eventsDir, event.SequenceID+".jsonl")
			if data, err := json.Marshal(persistedProgressEvent{Timestamp: time.Now().UTC(), ProgressEvent: event}); err == nil {
				if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
					_, _ = file.Write(append(data, '\n'))
					_ = file.Close()
				}
			}
		}
	}
	if options.Progress != nil {
		options.Progress(event)
	}
}

func startSupervisorHeartbeat(options Options, sequence *SequenceState) func(string) {
	if options.SupervisorID == "" || options.HomeDir == "" {
		return nil
	}
	now := time.Now().UTC()
	record := Supervisor{
		ID:          options.SupervisorID,
		PID:         os.Getpid(),
		LogPath:     options.SupervisorLogPath,
		Status:      "running",
		StartedAt:   &now,
		HeartbeatAt: &now,
	}
	sequence.Supervisor = &record
	root := filepath.Join(options.HomeDir, "state", "supervisors")
	path := filepath.Join(root, safeName(record.ID)+".json")
	var mu sync.Mutex
	write := func() {
		mu.Lock()
		defer mu.Unlock()
		_ = os.MkdirAll(root, 0o755)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err == nil {
			_ = os.Rename(tmp, path)
		}
	}
	write()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				heartbeat := time.Now().UTC()
				record.HeartbeatAt = &heartbeat
				mu.Unlock()
				write()
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func(status string) {
		once.Do(func() {
			close(stop)
			<-done
			mu.Lock()
			finished := time.Now().UTC()
			record.Status = status
			record.HeartbeatAt = &finished
			record.FinishedAt = &finished
			*sequence.Supervisor = record
			mu.Unlock()
			write()
		})
	}
}

func runPrompt(repoRoot string, prompt Prompt, specContext, sessionID string, options Options, launcher Launcher) (PromptState, error) {
	started := time.Now().UTC()
	promptState := PromptState{Prompt: prompt.Name, PromptType: prompt.Type, Status: "running", StartedAt: &started}
	if options.RequireCleanGit {
		clean, err := gitClean(repoRoot)
		if err != nil {
			return promptState, err
		}
		if !clean {
			return promptState, fmt.Errorf("working tree is dirty")
		}
	}
	if options.Checkpoint || options.CommitEach {
		promptState.GitSHABefore, _ = gitSHA(repoRoot)
	}
	content, err := assemblePrompt(prompt.Path, specContext)
	if err != nil {
		return promptState, err
	}
	worker, err := launcher.LaunchPrompt(prompt.Path, content, sessionID)
	promptState.Worker = worker
	promptState.WorkerID = worker.ID
	exitCode := 0
	if err != nil {
		exitCode = 1
		promptState.ExitCode = &exitCode
		finished := time.Now().UTC()
		promptState.FinishedAt = &finished
		return promptState, err
	}
	if !state.IsTerminalStatus(worker.Status) {
		waiter, ok := launcher.(WaitLauncher)
		if !ok {
			finished := time.Now().UTC()
			promptState.FinishedAt = &finished
			return promptState, fmt.Errorf("%s launched worker %s but it has not finished yet", prompt.Name, worker.ID)
		}
		worker, err = waiter.WaitPrompt(worker)
		promptState.Worker = worker
		if err != nil {
			finished := time.Now().UTC()
			promptState.FinishedAt = &finished
			return promptState, err
		}
	}
	if worker.ExitCode != nil {
		exitCode = *worker.ExitCode
	}
	promptState.ExitCode = &exitCode
	if worker.EngineResult != nil && worker.EngineResult.EngineExitCode != nil {
		engineExitCode := *worker.EngineResult.EngineExitCode
		promptState.ExitCode = &engineExitCode
	}
	engineFailed := exitCode != 0
	if worker.EngineResult != nil && worker.EngineResult.EngineExitCode != nil {
		engineFailed = *worker.EngineResult.EngineExitCode != 0
	}
	if engineFailed || worker.Status == state.StatusLaunchFailed {
		finished := time.Now().UTC()
		promptState.FinishedAt = &finished
		return promptState, fmt.Errorf("%s failed with worker status %s", prompt.Name, worker.Status)
	}
	if worker.EngineResult != nil {
		promptState.CompletionStatus = worker.EngineResult.CompletionStatus
		promptState.NextPromptSafe = worker.EngineResult.NextPromptSafe
		promptState.CompletionReason = worker.EngineResult.CompletionReason
	}
	if worker.EngineResult == nil {
		promptState.CompletionReason = "worker produced no structured completion result"
	} else if err := worker.EngineResult.OrderedCompletionError(); err != nil {
		promptState.CompletionReason = err.Error()
	}
	if promptState.CompletionReason != "" {
		if promptState.Worker.EngineResult == nil {
			promptState.Worker.EngineResult = &state.EngineResult{CompletionReason: promptState.CompletionReason}
		} else {
			promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
		}
		finished := time.Now().UTC()
		promptState.FinishedAt = &finished
		return promptState, fmt.Errorf("%s did not satisfy ordered completion contract: %s", prompt.Name, promptState.CompletionReason)
	}
	if worker.Status == state.StatusFailed {
		finished := time.Now().UTC()
		promptState.FinishedAt = &finished
		return promptState, fmt.Errorf("%s failed with worker status %s", prompt.Name, worker.Status)
	}
	if options.Checkpoint || options.CommitEach {
		promptState.GitSHAAfter, _ = gitSHA(repoRoot)
		promptState.FilesChanged, _ = gitChangedFiles(repoRoot)
	}
	if options.CommitEach && len(promptState.FilesChanged) > 0 {
		commit, err := gitCommit(repoRoot, "PromptGrinder: complete "+prompt.Name)
		if err != nil {
			finished := time.Now().UTC()
			promptState.FinishedAt = &finished
			return promptState, err
		}
		promptState.CommitSHA = commit
		promptState.GitSHAAfter, _ = gitSHA(repoRoot)
		promptState.FilesChanged, _ = gitChangedFiles(repoRoot)
	}
	finished := time.Now().UTC()
	promptState.Status = "completed"
	promptState.FinishedAt = &finished
	return promptState, nil
}

func assemblePrompt(path, specContext string) (string, error) {
	active, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	activeText := string(active)
	frontmatter, body, ok := splitFrontmatter(activeText)
	assembledBody := ""
	if strings.TrimSpace(specContext) != "" {
		assembledBody = "# Shared Context\n\n" + specContext + "\n\n# Active Prompt\n\n"
	}
	assembledBody += body + orderedCompletionContract
	if ok {
		return frontmatter + assembledBody, nil
	}
	return assembledBody, nil
}

const orderedCompletionContract = `

# Required Completion Report

End your final answer with exactly one occurrence of each field:

STATUS: PASS
NEXT_PROMPT_SAFE: yes

Use STATUS: BLOCKED or STATUS: PARTIAL and NEXT_PROMPT_SAFE: no when the task is not fully and safely complete. PromptGrinder will stop this ordered sequence unless the final report is unambiguous PASS/yes.
`

func splitFrontmatter(text string) (string, string, bool) {
	if !strings.HasPrefix(text, "---") {
		return "", text, false
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", text, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[:i+1], ""), strings.Join(lines[i+1:], ""), true
		}
	}
	return "", text, false
}

func readSpecificationContext(prompts []Prompt) (string, error) {
	parts := []string{}
	for _, prompt := range prompts {
		if prompt.Type != TypeSpecification {
			continue
		}
		data, err := os.ReadFile(prompt.Path)
		if err != nil {
			return "", err
		}
		parsed, err := markdown.Parse(string(data))
		if err != nil {
			return "", fmt.Errorf("parse specification %s: %w", prompt.Path, err)
		}
		if err := markdown.Validate(parsed, prompt.Path); err != nil {
			return "", err
		}
		parts = append(parts, string(markdown.Render(parsed)))
	}
	return strings.Join(parts, "\n\n"), nil
}

type folderStore struct {
	Root string
}

func (s folderStore) ensure() error {
	return os.MkdirAll(filepath.Join(s.Root, "prompts"), 0o755)
}

func (s folderStore) loadOrCreate(folder string, options Options) (RunState, bool, error) {
	if !options.Fresh {
		runState, err := s.loadRun()
		if err == nil && runState.Status != "completed" {
			if options.Resume || !options.Fresh {
				return runState, true, nil
			}
		}
		if options.Resume {
			if err == nil {
				return RunState{}, false, fmt.Errorf("no unfinished run found in %s", s.Root)
			}
			if errors.Is(err, os.ErrNotExist) {
				return RunState{}, false, fmt.Errorf("no run state found in %s", s.Root)
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RunState{}, false, err
		}
	}
	now := time.Now().UTC()
	runID, err := newRunID()
	if err != nil {
		return RunState{}, false, err
	}
	return RunState{RunID: runID, Folder: folder, Status: "running", Completed: []string{}, StartedAt: &now, UpdatedAt: &now}, false, nil
}

func (s folderStore) loadRun() (RunState, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, "run.json"))
	if err != nil {
		return RunState{}, err
	}
	var runState RunState
	if err := json.Unmarshal(data, &runState); err != nil {
		return RunState{}, err
	}
	return runState, nil
}

func (s folderStore) saveRun(runState RunState) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(runState, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "run.json"), append(data, '\n'), 0o644)
}

func (s folderStore) savePrompt(promptState PromptState) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(promptState, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "prompts", safeName(promptState.Prompt)+".json"), append(data, '\n'), 0o644)
}

func (s folderStore) saveSummary(sequence SequenceState) error {
	if err := s.ensure(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "summary.md"), []byte(sequence.SummaryMarkdown()), 0o644)
}

func (r *RunState) touch() {
	now := time.Now().UTC()
	r.UpdatedAt = &now
}

func (s *SequenceState) touch() {
	now := time.Now().UTC()
	s.UpdatedAt = &now
}

func (s *SequenceState) refreshSummary() {
	s.TokenUsage = totalTokenUsage(s.Items)
	s.ExecutiveSummary = buildExecutiveSummary(s.Items)
}

func (s *SequenceState) mark(promptName, status string, worker state.Worker, finishedAt *time.Time, errorMessage string) {
	now := time.Now().UTC()
	for i := range s.Items {
		if s.Items[i].PromptName != promptName {
			continue
		}
		item := &s.Items[i]
		item.Status = status
		if item.StartedAt == nil {
			item.StartedAt = &now
		}
		if finishedAt != nil {
			item.FinishedAt = finishedAt
		} else if status == "succeeded" || status == "failed" || status == "skipped" {
			item.FinishedAt = &now
		}
		item.WorkerID = worker.ID
		item.LogPath = worker.LogPath
		item.ExitCode = worker.ExitCode
		item.Error = errorMessage
		if worker.EngineResult != nil {
			if worker.EngineResult.EngineExitCode != nil {
				item.ExitCode = worker.EngineResult.EngineExitCode
			}
			item.CompletionStatus = worker.EngineResult.CompletionStatus
			item.NextPromptSafe = worker.EngineResult.NextPromptSafe
			item.CompletionReason = worker.EngineResult.CompletionReason
		}
		if errorMessage != "" && item.CompletionReason == "" {
			item.CompletionReason = errorMessage
		}
		break
	}
	s.Status = sequenceStatus(s.Items)
	s.touch()
}

func sequenceStatus(items []SequenceItem) string {
	hasFailed := false
	hasInterrupted := false
	hasPending := false
	for _, item := range items {
		switch item.Status {
		case "failed":
			hasFailed = true
		case "interrupted":
			hasInterrupted = true
		case "pending", "running":
			hasPending = true
		}
	}
	if hasFailed {
		return "failed"
	}
	if hasInterrupted {
		return "interrupted"
	}
	if hasPending {
		return "running"
	}
	return "completed"
}

type SequenceProgress struct {
	SequenceID   string     `json:"sequence_id"`
	Folder       string     `json:"folder"`
	Status       string     `json:"status"`
	Total        int        `json:"total"`
	Succeeded    int        `json:"succeeded"`
	Failed       int        `json:"failed"`
	Interrupted  int        `json:"interrupted"`
	Pending      int        `json:"pending"`
	Current      string     `json:"current"`
	Next         string     `json:"next"`
	LastWorkerID string     `json:"last_worker_id"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func (s SequenceState) Progress() SequenceProgress {
	progress := SequenceProgress{SequenceID: s.SequenceID, Folder: s.Folder, Status: s.Status, Total: len(s.Items), CreatedAt: s.CreatedAt, StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt, FinishedAt: s.FinishedAt}
	for _, item := range s.Items {
		switch item.Status {
		case "succeeded", "skipped":
			progress.Succeeded++
		case "failed":
			progress.Failed++
			if progress.Current == "" {
				progress.Current = item.PromptName
			}
		case "interrupted":
			progress.Interrupted++
			if progress.Current == "" {
				progress.Current = item.PromptName
			}
		case "running":
			progress.Pending++
			if progress.Current == "" {
				progress.Current = item.PromptName
			}
		default:
			progress.Pending++
			if progress.Next == "" {
				progress.Next = item.PromptName
			}
		}
		if item.WorkerID != "" {
			progress.LastWorkerID = item.WorkerID
		}
	}
	return progress
}

func (s SequenceState) SummaryMarkdown() string {
	progress := s.Progress()
	var b strings.Builder
	fmt.Fprintf(&b, "# PromptGrinder Sequence Summary\n\n")
	fmt.Fprintf(&b, "- Sequence: `%s`\n", s.SequenceID)
	fmt.Fprintf(&b, "- Status: `%s`\n", s.Status)
	fmt.Fprintf(&b, "- Prompts: %d total, %d succeeded, %d failed, %d pending\n", progress.Total, progress.Succeeded, progress.Failed, progress.Pending)
	if s.TokenUsage.Available {
		fmt.Fprintf(&b, "- Total tokens used: %d\n", s.TokenUsage.Total)
	} else {
		fmt.Fprintf(&b, "- Total tokens used: not reported by worker logs\n")
	}
	fmt.Fprintf(&b, "\n## Executive Summary\n\n%s\n", valueOrDefault(s.ExecutiveSummary, "No prompt results have been recorded yet."))
	return b.String()
}

func totalTokenUsage(items []SequenceItem) TokenUsage {
	total := 0
	available := false
	for _, item := range items {
		if item.LogPath == "" {
			continue
		}
		data, err := os.ReadFile(item.LogPath)
		if err != nil {
			continue
		}
		usage := parseTokenUsage(string(data))
		if usage.Available {
			available = true
			total += usage.Total
		}
	}
	return TokenUsage{Available: available, Total: total}
}

func parseTokenUsage(text string) TokenUsage {
	total := 0
	available := false
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\btotal[_ ]tokens\b[^0-9]*([0-9][0-9,]*)`),
		regexp.MustCompile(`(?i)\btokens[_ ]used\b[^0-9]*([0-9][0-9,]*)`),
		regexp.MustCompile(`(?i)\bused\b[^0-9]*([0-9][0-9,]*)[^0-9]*\btokens\b`),
	}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			value, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
			if err != nil {
				continue
			}
			total += value
			available = true
		}
	}
	return TokenUsage{Available: available, Total: total}
}

func buildExecutiveSummary(items []SequenceItem) string {
	lines := []string{}
	for _, item := range items {
		switch item.Status {
		case "succeeded":
			lines = append(lines, fmt.Sprintf("- %s succeeded%s.", item.PromptName, workerSuffix(item)))
		case "skipped":
			lines = append(lines, fmt.Sprintf("- %s was used as shared context and skipped as an execution prompt.", item.PromptName))
		case "failed":
			detail := item.Error
			if detail == "" {
				detail = "no error summary recorded"
			}
			lines = append(lines, fmt.Sprintf("- %s failed%s: %s.", item.PromptName, workerSuffix(item), detail))
		case "running":
			lines = append(lines, fmt.Sprintf("- %s is running%s.", item.PromptName, workerSuffix(item)))
		default:
			lines = append(lines, fmt.Sprintf("- %s is pending.", item.PromptName))
		}
	}
	return strings.Join(lines, "\n")
}

func workerSuffix(item SequenceItem) string {
	if item.WorkerID == "" {
		return ""
	}
	return " via worker " + item.WorkerID
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type sequenceStore struct {
	Root        string
	SummaryRoot string
}

func newSequenceStore(homeDir string) sequenceStore {
	if homeDir == "" {
		homeDir = os.Getenv("PROMPTGRINDER_HOME")
	}
	if homeDir == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			homeDir = filepath.Join(userHome, ".promptgrinder")
		}
	}
	return sequenceStore{
		Root:        filepath.Join(homeDir, "state", "sequences"),
		SummaryRoot: filepath.Join(homeDir, "summaries"),
	}
}

func ListSequences(homeDir string) ([]SequenceProgress, error) {
	store := newSequenceStore(homeDir)
	sequences, err := store.list()
	if err != nil {
		return nil, err
	}
	out := []SequenceProgress{}
	for _, sequence := range sequences {
		out = append(out, sequence.Progress())
	}
	return out, nil
}

func LoadSequence(homeDir, sequenceID string) (SequenceState, error) {
	store := newSequenceStore(homeDir)
	return store.load(sequenceID)
}

func CurrentSequence(homeDir string) (SequenceState, error) {
	store := newSequenceStore(homeDir)
	sequences, err := store.list()
	if err != nil {
		return SequenceState{}, err
	}
	if len(sequences) == 0 {
		return SequenceState{}, os.ErrNotExist
	}
	return sequences[0], nil
}

func ReconcileSequences(homeDir string, threshold time.Duration, markInterrupted bool) ([]SequenceState, error) {
	store := newSequenceStore(homeDir)
	sequences, err := store.list()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stale := []SequenceState{}
	for _, sequence := range sequences {
		if sequence.Status != "running" {
			continue
		}
		if sequence.Supervisor != nil {
			if supervisorHealthy(homeDir, sequence.Supervisor, threshold, now) {
				continue
			}
		} else if sequence.UpdatedAt == nil || now.Sub(*sequence.UpdatedAt) <= threshold {
			continue
		}
		if markInterrupted {
			markedItem := false
			for i := range sequence.Items {
				if sequence.Items[i].Status == "running" {
					finished := now
					sequence.Items[i].Status = "interrupted"
					sequence.Items[i].FinishedAt = &finished
					sequence.Items[i].Error = "supervisor heartbeat expired"
					markedItem = true
				}
			}
			if !markedItem {
				for i := range sequence.Items {
					if sequence.Items[i].Status == "pending" {
						finished := now
						sequence.Items[i].Status = "interrupted"
						sequence.Items[i].FinishedAt = &finished
						sequence.Items[i].Error = "supervisor stopped before prompt launch"
						break
					}
				}
			}
			sequence.Status = "interrupted"
			sequence.touch()
			sequence.refreshSummary()
			if err := store.save(sequence); err != nil {
				return stale, err
			}
		}
		stale = append(stale, sequence)
	}
	return stale, nil
}

func supervisorHealthy(homeDir string, supervisor *Supervisor, threshold time.Duration, now time.Time) bool {
	if supervisor == nil || supervisor.ID == "" {
		return false
	}
	path := filepath.Join(homeDir, "state", "supervisors", safeName(supervisor.ID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var record Supervisor
	if json.Unmarshal(data, &record) != nil || record.ID != supervisor.ID || record.PID != supervisor.PID || record.Status != "running" || record.HeartbeatAt == nil {
		return false
	}
	if now.Sub(*record.HeartbeatAt) > threshold || record.PID <= 0 {
		return false
	}
	// Signal 0 only probes the PID recorded by this sequence's matching
	// supervisor record; it never signals or terminates the process.
	if err := syscall.Kill(record.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return true
}

func (s sequenceStore) list() ([]SequenceState, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []SequenceState{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []SequenceState{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sequence, err := s.loadPath(filepath.Join(s.Root, entry.Name()))
		if err != nil {
			continue
		}
		out = append(out, sequence)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].UpdatedAt, out[j].UpdatedAt
		if left == nil || right == nil {
			return out[i].SequenceID > out[j].SequenceID
		}
		return left.After(*right)
	})
	return out, nil
}

func (s sequenceStore) loadOrCreate(folder, repoRoot string, prompts []Prompt, options Options) (SequenceState, bool, error) {
	base, err := buildSequence(folder, repoRoot, prompts, options)
	if err != nil {
		return SequenceState{}, false, err
	}
	if !options.Restart && !options.NoResume && !options.Fresh {
		existing, err := s.load(base.SequenceID)
		if err == nil {
			return existing, true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SequenceState{}, false, err
		}
	}
	return base, false, nil
}

func (s sequenceStore) load(sequenceID string) (SequenceState, error) {
	return s.loadPath(filepath.Join(s.Root, sequenceID+".json"))
}

func (s sequenceStore) loadPath(path string) (SequenceState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SequenceState{}, err
	}
	var sequence SequenceState
	if err := json.Unmarshal(data, &sequence); err != nil {
		return SequenceState{}, err
	}
	return sequence, nil
}

func (s sequenceStore) save(sequence SequenceState) error {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sequence, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.Root, sequence.SequenceID+".json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(s.SummaryRoot, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.SummaryRoot, sequence.SequenceID+".md"), []byte(sequence.SummaryMarkdown()), 0o644)
}

func buildSequence(folder, repoRoot string, prompts []Prompt, options Options) (SequenceState, error) {
	now := time.Now().UTC()
	items := make([]SequenceItem, 0, len(prompts))
	cfg, err := sequenceConfig(repoRoot, options)
	if err != nil {
		return SequenceState{}, err
	}
	hashInput := []string{
		"folder=" + folder,
		"repo=" + repoRoot,
		"template=" + options.Template,
		"engine_override=" + options.EngineOverride,
		fmt.Sprintf("include_specification=%t", options.IncludeSpecification),
		fmt.Sprintf("checkpoint=%t", options.Checkpoint),
		fmt.Sprintf("commit_each=%t", options.CommitEach),
		fmt.Sprintf("require_clean_git=%t", options.RequireCleanGit),
	}
	engines := []string{}
	for _, prompt := range prompts {
		hash, err := fileHash(prompt.Path)
		if err != nil {
			return SequenceState{}, err
		}
		items = append(items, SequenceItem{PromptPath: prompt.Path, PromptName: prompt.Name, ContentHash: hash, Status: "pending"})
		engineName, taskEngine, err := effectivePromptEngine(prompt.Path, cfg, options.EngineOverride)
		if err != nil {
			return SequenceState{}, err
		}
		engines = append(engines, engineName)
		hashInput = append(hashInput,
			"prompt_path="+prompt.Path,
			"prompt_hash="+hash,
			"prompt_engine="+engineName,
			"prompt_engine_source="+engineSource(taskEngine, options.EngineOverride),
		)
		hashInput = append(hashInput, engineConfigIdentity(engineName, cfg)...)
	}
	sum := sha256.Sum256([]byte(strings.Join(hashInput, "\n")))
	id := "seq_" + hex.EncodeToString(sum[:])[:16]
	return SequenceState{SequenceID: id, Folder: folder, Status: "running", Template: options.Template, Engine: sequenceEngineLabel(engines, options.EngineOverride), Items: items, CreatedAt: &now, StartedAt: &now, UpdatedAt: &now}, nil
}

func sequenceConfig(repoRoot string, options Options) (config.Config, error) {
	cfg := options.BaseConfig
	if cfg.HomeDir == "" {
		cfg.HomeDir = options.HomeDir
	}
	if !options.UseRepoConfig && cfg.Engine != "" {
		return cfg, nil
	}
	loaded, err := config.LoadWithHome(repoRoot, cfg.HomeDir)
	if err != nil {
		return cfg, err
	}
	return loaded, nil
}

func effectivePromptEngine(path string, cfg config.Config, override string) (string, string, error) {
	taskEngine := ""
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return "", "", err
	}
	taskEngine = engineNameFromMetadata(task.Metadata)
	engineName := cfg.Engine
	if engineName == "" {
		engineName = "codex"
	}
	if taskEngine != "" {
		engineName = taskEngine
	}
	if override != "" {
		engineName = override
	}
	return engineName, taskEngine, nil
}

func engineNameFromMetadata(metadata map[string]any) string {
	switch value := metadata["engine"].(type) {
	case string:
		return value
	case map[string]any:
		if name, ok := value["name"].(string); ok {
			return name
		}
	}
	return ""
}

func engineSource(taskEngine, override string) string {
	switch {
	case override != "":
		return "override"
	case taskEngine != "":
		return "prompt"
	default:
		return "default"
	}
}

func engineConfigIdentity(engineName string, cfg config.Config) []string {
	parts := []string{}
	if engineName == "codex" {
		parts = append(parts,
			"engine.codex.sandbox="+cfg.CodexSandbox,
			"engine.codex.approval="+cfg.CodexApproval,
		)
	}
	if cfg.WorkerTimeout > 0 {
		parts = append(parts, "worker.timeout="+cfg.WorkerTimeout.String())
	}
	return parts
}

func sequenceEngineLabel(engines []string, override string) string {
	if override != "" {
		return override
	}
	if len(engines) == 0 {
		return ""
	}
	first := engines[0]
	for _, engineName := range engines[1:] {
		if engineName != first {
			return "mixed"
		}
	}
	return first
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func matches(value, pattern string) bool {
	ok, _ := regexp.MatchString(pattern, value)
	return ok
}

func setFromSlice(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func safeName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return replacer.Replace(value)
}

func newRunID() (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "run_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(suffix[:]), nil
}

func gitClean(dir string) (bool, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name := ""
		if len(line) > 3 {
			name = strings.TrimSpace(line[3:])
		}
		if isPromptGrinderStatePath(name) {
			continue
		}
		return false, nil
	}
	return true, nil
}

func gitSHA(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitChangedFiles(dir string) ([]string, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > 3 {
			name := strings.TrimSpace(line[3:])
			if isPromptGrinderStatePath(name) {
				continue
			}
			files = append(files, name)
		}
	}
	return files, nil
}

func gitCommit(dir, message string) (string, error) {
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output(); err == nil {
		for _, name := range strings.Split(string(out), "\n") {
			name = strings.TrimSpace(name)
			if isPromptGrinderStatePath(name) {
				_ = exec.Command("git", "-C", dir, "reset", "-q", "--", name).Run()
			}
		}
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=PromptGrinder", "-c", "user.email=promptgrinder@example.invalid", "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
	}
	return gitSHA(dir)
}

func isPromptGrinderStatePath(name string) bool {
	name = filepath.ToSlash(strings.TrimSpace(name))
	return name == ".promptgrinder" || strings.HasPrefix(name, ".promptgrinder/") || strings.Contains(name, "/.promptgrinder/")
}
