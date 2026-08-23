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
	"strings"
	"sync"
	"syscall"
	"time"

	"promptgrinder/internal/config"
	"promptgrinder/internal/markdown"
	"promptgrinder/internal/repository"
	"promptgrinder/internal/state"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workeridentity"
	"promptgrinder/internal/workerpathpolicy"
)

type PromptType string

// ContextMode controls whether a slice resumes the preceding runtime session.
// Shared is the compatible default; Fresh establishes an explicit session boundary.
type ContextMode string

const (
	TypeSpecification PromptType  = "specification"
	TypeImplement     PromptType  = "implement"
	TypeTest          PromptType  = "test"
	TypeVerify        PromptType  = "verify"
	TypeReview        PromptType  = "review"
	TypeUnknown       PromptType  = "unknown"
	ContextShared     ContextMode = "shared"
	ContextFresh      ContextMode = "fresh"
)

type Options struct {
	Resume                  bool
	ResumeSequence          string
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
	SandboxOverride         string
	IncludeSpecification    bool
	RecoveryAttempts        int
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
	Adoption *SequenceAdoption
	Prompts  []Prompt
	Started  bool
	Resumed  bool
	Failed   error
	Warnings []string
}

type ProgressEvent struct {
	Type             string               `json:"type"`
	SequenceID       string               `json:"sequence_id"`
	PromptName       string               `json:"prompt_name,omitempty"`
	PromptType       PromptType           `json:"prompt_type,omitempty"`
	Status           string               `json:"status,omitempty"`
	WorkerID         string               `json:"worker_id,omitempty"`
	Scope            string               `json:"scope,omitempty"`
	Engine           string               `json:"engine,omitempty"`
	Model            string               `json:"model,omitempty"`
	LogPath          string               `json:"log_path,omitempty"`
	Completed        int                  `json:"completed"`
	Total            int                  `json:"total"`
	Folder           string               `json:"folder,omitempty"`
	Duration         time.Duration        `json:"duration,omitempty"`
	ExitCode         *int                 `json:"exit_code,omitempty"`
	CompletionStatus string               `json:"completion_status,omitempty"`
	NextPromptSafe   *bool                `json:"next_prompt_safe,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	FailureReport    *state.FailureReport `json:"failure_report,omitempty"`
	RecoveryAttempt  int                  `json:"recovery_attempt,omitempty"`
	RecoveryMode     string               `json:"recovery_mode,omitempty"`
	RecoveryArtifact string               `json:"recovery_artifact,omitempty"`
	Inventory        []ProgressPrompt     `json:"inventory,omitempty"`
	MarkdownTotal    int                  `json:"markdown_total,omitempty"`
	Ignored          []string             `json:"ignored,omitempty"`
	ResumePlan       string               `json:"resume_plan,omitempty"`
	Adoption         *SequenceAdoption    `json:"adoption,omitempty"`
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
	Path        string
	Name        string
	ID          string
	Type        PromptType
	Role        string
	RolePolicy  *RolePolicy
	DependsOn   []string
	ContextMode ContextMode
	GateOutcome string
}

type FolderInspection struct {
	Prompts       []Prompt
	MarkdownTotal int
	Ignored       []string
	Invalid       []string
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
	StateVersion     int                `json:"state_version,omitempty"`
	SequenceID       string             `json:"sequence_id"`
	Folder           string             `json:"folder"`
	RepositoryPath   string             `json:"repository_path,omitempty"`
	Status           string             `json:"status"`
	Template         string             `json:"template"`
	Engine           string             `json:"engine,omitempty"`
	SessionID        string             `json:"session_id,omitempty"`
	Items            []SequenceItem     `json:"items"`
	TokenUsage       TokenUsage         `json:"token_usage"`
	ExecutiveSummary string             `json:"executive_summary"`
	CreatedAt        *time.Time         `json:"created_at,omitempty"`
	StartedAt        *time.Time         `json:"started_at"`
	UpdatedAt        *time.Time         `json:"updated_at"`
	FinishedAt       *time.Time         `json:"finished_at,omitempty"`
	Supervisor       *Supervisor        `json:"supervisor,omitempty"`
	EventPath        string             `json:"event_path,omitempty"`
	Adoptions        []SequenceAdoption `json:"adoptions,omitempty"`
}

type SequenceAdoption struct {
	SequenceID          string             `json:"sequence_id"`
	Explicit            bool               `json:"explicit"`
	MigratedLegacyState bool               `json:"migrated_legacy_state,omitempty"`
	RetainedPrompts     []string           `json:"retained_prompts"`
	RestartAt           string             `json:"restart_at"`
	PolicyHashChanges   []PolicyHashChange `json:"policy_hash_changes,omitempty"`
	AdoptedAt           time.Time          `json:"adopted_at"`
}

type PolicyHashChange struct {
	PromptName   string `json:"prompt_name"`
	PreviousHash string `json:"previous_hash"`
	CurrentHash  string `json:"current_hash"`
	Retained     bool   `json:"retained"`
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
	PromptPath       string               `json:"prompt_path"`
	PromptName       string               `json:"prompt_name"`
	PromptID         string               `json:"prompt_id,omitempty"`
	DependsOn        []string             `json:"depends_on,omitempty"`
	ContextMode      ContextMode          `json:"context_mode,omitempty"`
	GateOutcome      string               `json:"gate_outcome,omitempty"`
	PromptHash       string               `json:"prompt_hash,omitempty"`
	PolicyHash       string               `json:"policy_hash,omitempty"`
	ContentHash      string               `json:"content_hash"`
	Status           string               `json:"status"`
	WorkerID         string               `json:"worker_id,omitempty"`
	Scope            string               `json:"scope,omitempty"`
	Engine           string               `json:"engine,omitempty"`
	Model            string               `json:"model,omitempty"`
	StartedAt        *time.Time           `json:"started_at,omitempty"`
	FinishedAt       *time.Time           `json:"finished_at,omitempty"`
	ExitCode         *int                 `json:"exit_code,omitempty"`
	LogPath          string               `json:"log_path,omitempty"`
	Error            string               `json:"error,omitempty"`
	CompletionStatus string               `json:"completion_status,omitempty"`
	NextPromptSafe   *bool                `json:"next_prompt_safe,omitempty"`
	CompletionReason string               `json:"completion_reason,omitempty"`
	FailureReport    *state.FailureReport `json:"failure_report,omitempty"`
	RecoveryAttempts int                  `json:"recovery_attempts,omitempty"`
	RecoveryMode     string               `json:"recovery_mode,omitempty"`
	RecoveryArtifact string               `json:"recovery_artifact,omitempty"`
	TokenUsage       *TokenUsage          `json:"token_usage,omitempty"`
}

type TokenUsage struct {
	Available       bool  `json:"available"`
	Input           int64 `json:"input"`
	CachedInput     int64 `json:"cached_input"`
	Output          int64 `json:"output"`
	ReasoningOutput int64 `json:"reasoning_output"`
	Total           int64 `json:"total"`
}

func (u TokenUsage) String() string {
	if !u.Available {
		return "unavailable"
	}
	return fmt.Sprintf("%d (input: %d; cached input: %d; output: %d; reasoning output: %d)", u.Total, u.Input, u.CachedInput, u.Output, u.ReasoningOutput)
}

type PromptState struct {
	Prompt           string                     `json:"prompt"`
	PromptType       PromptType                 `json:"prompt_type"`
	Status           string                     `json:"status"`
	StartedAt        *time.Time                 `json:"started_at"`
	FinishedAt       *time.Time                 `json:"finished_at"`
	ExitCode         *int                       `json:"exit_code,omitempty"`
	GitSHABefore     string                     `json:"git_sha_before"`
	GitSHAAfter      string                     `json:"git_sha_after"`
	CommitSHA        string                     `json:"commit_sha"`
	FilesChanged     []string                   `json:"files_changed"`
	WorkerID         string                     `json:"worker_id,omitempty"`
	Error            string                     `json:"error,omitempty"`
	CompletionStatus string                     `json:"completion_status,omitempty"`
	NextPromptSafe   *bool                      `json:"next_prompt_safe,omitempty"`
	CompletionReason string                     `json:"completion_reason,omitempty"`
	FailureReport    *state.FailureReport       `json:"failure_report,omitempty"`
	RecoveryAttempts int                        `json:"recovery_attempts,omitempty"`
	RecoveryMode     string                     `json:"recovery_mode,omitempty"`
	RecoveryArtifact string                     `json:"recovery_artifact,omitempty"`
	GateOutcome      string                     `json:"gate_outcome,omitempty"`
	EngineSessionID  string                     `json:"engine_session_id,omitempty"`
	GitBaseline      *workerpathpolicy.Snapshot `json:"git_baseline,omitempty"`
	Worker           state.Worker               `json:"-"`
}

func Classify(filename string) PromptType {
	name := filepath.Base(filename)
	switch {
	case matches(name, `^00(?:[A-Z]+)?-specification.*\.(?:md|pg)$`):
		return TypeSpecification
	case matches(name, `^\d\d(?:[A-Z]+)?-implement-.+\.(?:md|pg)$`):
		return TypeImplement
	case matches(name, `^\d\d(?:[A-Z]+)?-test-.+\.(?:md|pg)$`):
		return TypeTest
	case matches(name, `^\d\d(?:[A-Z]+)?-verify-.+\.(?:md|pg)$`), matches(name, `^\d\d(?:[A-Z]+)?-final-verify.*\.(?:md|pg)$`):
		return TypeVerify
	case matches(name, `^\d\d(?:[A-Z]+)?-review-.+\.(?:md|pg)$`):
		return TypeReview
	default:
		return TypeUnknown
	}
}

func Discover(folder string) ([]Prompt, error) {
	inspection, err := Inspect(folder)
	if err != nil {
		return nil, err
	}
	if len(inspection.Invalid) > 0 {
		return nil, invalidPromptNamesError(inspection)
	}
	return inspection.Prompts, nil
}

func Inspect(folder string) (FolderInspection, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return FolderInspection{}, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return FolderInspection{}, err
	}
	inspection := FolderInspection{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.IsDir() || !isPromptFile(name) {
			continue
		}
		inspection.MarkdownTotal++
		if !matches(name, `^\d\d(?:[A-Z]+)?-.+\.(?:md|pg)$`) {
			inspection.Ignored = append(inspection.Ignored, name)
			continue
		}
		prompt, err := inspectPrompt(filepath.Join(abs, name), name)
		if err != nil {
			return FolderInspection{}, err
		}
		if prompt.Type == TypeUnknown {
			inspection.Invalid = append(inspection.Invalid, name)
			continue
		}
		inspection.Prompts = append(inspection.Prompts, prompt)
	}
	sort.SliceStable(inspection.Prompts, func(i, j int) bool {
		return inspection.Prompts[i].Name < inspection.Prompts[j].Name
	})
	sort.Strings(inspection.Ignored)
	sort.Strings(inspection.Invalid)
	if err := validatePromptDependencies(inspection.Prompts); err != nil {
		return FolderInspection{}, fmt.Errorf("run-folder preflight: %w", err)
	}
	return inspection, nil
}

func inspectPrompt(promptPath, name string) (Prompt, error) {
	data, err := os.ReadFile(promptPath)
	if err != nil {
		return Prompt{}, err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return Prompt{}, fmt.Errorf("run-folder preflight: %s: %w", name, err)
	}
	if err := markdown.Validate(task, name); err != nil {
		return Prompt{}, fmt.Errorf("run-folder preflight: %w", err)
	}
	filenameType := Classify(name)
	declaredType := promptTypeValue(task.Metadata["type"])
	if filenameType != TypeUnknown && filenameType != TypeSpecification && declaredType != TypeUnknown && filenameType != declaredType {
		return Prompt{}, fmt.Errorf("run-folder preflight: %s starts with the %q slice naming convention (type %q), but its frontmatter says type: %q; rename it to NN-%s-...{.md|.pg} or change frontmatter type to %q", name, filenameType, filenameType, declaredType, declaredType, filenameType)
	}
	promptType := filenameType
	if promptType == TypeUnknown {
		promptType = declaredType
	}
	id, _ := task.Metadata["id"].(string)
	if filenameType == TypeUnknown && promptType != TypeUnknown && id == "" {
		return Prompt{}, fmt.Errorf("run-folder preflight: %s uses an untyped filename and must declare id in frontmatter", name)
	}
	if id == "" {
		id = strings.TrimSuffix(name, filepath.Ext(name))
	}
	role, _ := task.Metadata["role"].(string)
	gateOutcome, _ := task.Metadata["gate_outcome"].(string)
	return Prompt{Path: promptPath, Name: name, ID: id, Type: promptType, Role: role, DependsOn: stringListValue(task.Metadata["depends_on"]), ContextMode: contextModeValue(task.Metadata["context_mode"]), GateOutcome: gateOutcome}, nil
}

func contextModeValue(value any) ContextMode {
	if value == string(ContextFresh) {
		return ContextFresh
	}
	return ContextShared
}

func promptTypeValue(value any) PromptType {
	text, _ := value.(string)
	switch PromptType(text) {
	case TypeImplement, TypeTest, TypeVerify, TypeReview:
		return PromptType(text)
	default:
		return TypeUnknown
	}
}

func stringListValue(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func validatePromptDependencies(prompts []Prompt) error {
	positions := make(map[string]int, len(prompts))
	for index, prompt := range prompts {
		if previous, exists := positions[prompt.ID]; exists {
			return fmt.Errorf("duplicate task id %q in %s and %s", prompt.ID, prompts[previous].Name, prompt.Name)
		}
		positions[prompt.ID] = index
	}
	for index, prompt := range prompts {
		for _, dependency := range prompt.DependsOn {
			position, exists := positions[dependency]
			if !exists {
				return fmt.Errorf("%s depends on unknown task id %q", prompt.Name, dependency)
			}
			if position >= index {
				return fmt.Errorf("%s depends on %q, which must appear earlier in filename order", prompt.Name, dependency)
			}
		}
	}
	return nil
}

func invalidPromptNamesError(inspection FolderInspection) error {
	return fmt.Errorf("run-folder preflight: %d of %d prompt files included; unsupported numbered prompt name(s): %s; use a typed filename or declare id and type in frontmatter", len(inspection.Prompts), inspection.MarkdownTotal, strings.Join(inspection.Invalid, ", "))
}

func isPromptFile(name string) bool {
	extension := filepath.Ext(name)
	return strings.EqualFold(extension, ".md") || strings.EqualFold(extension, ".pg")
}

// ResolveSequenceID computes the stable identity used by Run without creating
// or mutating sequence state.
func ResolveSequenceID(folder string, options Options) (string, error) {
	if options.ResumeSequence != "" {
		preflight, err := Preflight(folder, options)
		if err != nil {
			return "", err
		}
		return preflight.SequenceID, nil
	}
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
	inspection, err := Inspect(absFolder)
	if err != nil {
		return "", err
	}
	if len(inspection.Invalid) > 0 {
		return "", invalidPromptNamesError(inspection)
	}
	prompts := inspection.Prompts
	if len(prompts) == 0 {
		return "", fmt.Errorf("no recognized numbered Markdown prompts found in %s", absFolder)
	}
	if err := applyRolePolicies(repoRoot, prompts); err != nil {
		return "", fmt.Errorf("run-folder preflight: %w", err)
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
	preflight, err := Preflight(folder, options)
	if err != nil {
		return Summary{}, err
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
	absFolder, repoRoot := preflight.Folder, preflight.Repository
	options.RepoPath = repoRoot
	inspection := preflight.Inspection
	prompts := inspection.Prompts

	sequenceStore := newSequenceStore(options.HomeDir)
	sequence, sequenceResumed, adoption, err := sequenceStore.loadOrCreate(absFolder, repoRoot, prompts, options)
	if err != nil {
		return Summary{}, err
	}
	if sequenceResumed && sequence.Status == "product-blocked" {
		return Summary{Sequence: &sequence, Prompts: prompts, Started: true, Resumed: true}, fmt.Errorf("sequence %s is product-blocked by a completed capability gate; resolve the prerequisite and start a new compatible sequence", sequence.SequenceID)
	}
	store := folderStore{Root: folderStateRoot(options.HomeDir, sequence.SequenceID), LegacyRoot: filepath.Join(absFolder, ".promptgrinder")}
	runState, resumed, err := store.loadOrCreate(absFolder, options)
	if err != nil {
		return Summary{}, err
	}
	if adoption != nil {
		runState.Completed = append([]string(nil), adoption.RetainedPrompts...)
		runState.Current = adoption.RestartAt
		runState.Status = "running"
		runState.touch()
	}
	summary = Summary{Run: runState, Sequence: &sequence, Adoption: adoption, Prompts: prompts, Started: true, Resumed: resumed || sequenceResumed}
	resumePlan := compatibleResumePlan(sequence, preflight.SequenceID, sequenceResumed)
	if adoption != nil {
		if err := refreshAdoptedFingerprints(&sequence, prompts, repoRoot); err != nil {
			return summary, err
		}
		sequence.Adoptions = append(sequence.Adoptions, *adoption)
		sequence.FinishedAt = nil
		sequence.touch()
		sequence.refreshSummary()
		if err := sequenceStore.save(sequence); err != nil {
			return summary, err
		}
		resumePlan = explicitAdoptionPlan(*adoption)
		emitProgress(options, ProgressEvent{Type: "sequence.adopted", SequenceID: sequence.SequenceID, Folder: sequence.Folder, Status: sequence.Status, Adoption: adoption, ResumePlan: resumePlan, Completed: len(adoption.RetainedPrompts), Total: len(sequence.Items)})
	}
	if options.HomeDir != "" {
		sequence.EventPath = filepath.Join(options.HomeDir, "events", "sequences", sequence.SequenceID+".jsonl")
	}
	stopSupervisor := startSupervisorHeartbeat(options, &sequence)
	if stopSupervisor != nil {
		defer func() {
			if persisted, err := sequenceStore.load(sequence.SequenceID); err == nil && persisted.Status == "cancelled" {
				sequence = persisted
				stopSupervisor("cancelled")
				return
			}
			status := sequence.Status
			if status == "" || status == "running" {
				status = "completed"
			}
			if runErr != nil {
				status = "interrupted"
				if sequence.Status == "failed" {
					status = "failed"
				}
			}
			stopSupervisor(status)
			_ = sequenceStore.save(sequence)
			eventType := "notification.success"
			if status != "completed" && status != "product-blocked" {
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
	emitProgress(options, ProgressEvent{Type: "run.started", SequenceID: sequence.SequenceID, Folder: folder, Inventory: inventory, MarkdownTotal: inspection.MarkdownTotal, Ignored: inspection.Ignored, ResumePlan: resumePlan, Completed: sequence.Progress().Succeeded, Total: len(prompts)})
	if err := store.ensure(); err != nil {
		return summary, err
	}
	if summary.Resumed && (options.CommitEach || options.RequireCleanGit) {
		clean, cleanErr := gitClean(repoRoot)
		if cleanErr != nil {
			return summary, cleanErr
		}
		if !clean {
			if err := prepareResumedRecovery(repoRoot, options.HomeDir, &sequence, prompts, options, store); err != nil {
				return summary, err
			}
			if err := sequenceStore.save(sequence); err != nil {
				return summary, err
			}
			if err := store.saveSummary(sequence); err != nil {
				return summary, err
			}
		}
	}
	specContext, err := readSpecificationContext(prompts)
	if err != nil {
		return summary, err
	}
	completed := map[string]bool{}
	if sequenceResumed {
		if adoption == nil {
			completed = setFromSlice(runState.Completed)
		}
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
		resumingFailedItem := summary.Resumed && itemStatuses[prompt.Name] == "failed"
		sequence.mark(prompt.Name, "running", state.Worker{}, nil, "")
		sequence.refreshSummary()
		if err := sequenceStore.save(sequence); err != nil {
			return summary, err
		}
		recoveryAttempt := 0
		recoveryContext := ""
		recoveryMode := ""
		validationRepairUsed := false
		var repairBaseline *workerpathpolicy.Snapshot
		repairSessionID := ""
		var promptState PromptState
		var err error
		if resumingFailedItem {
			if failed, loadErr := newSequenceStore(options.HomeDir).loadPromptState(sequence.SequenceID, prompt.Name); loadErr == nil {
				failed.Worker = failedWorkerEvidence(options.HomeDir, sequenceItem(sequence, prompt.Name), failed)
				if evidence, eligible := validationRepairEligibility(repoRoot, options.HomeDir, prompt, failed); eligible && failed.RecoveryAttempts <= options.RecoveryAttempts && failed.GitBaseline != nil {
					recoveryAttempt = failed.RecoveryAttempts
					validationRepairUsed = true
					recoveryMode = "validation-repair"
					baseline := *failed.GitBaseline
					repairBaseline = &baseline
					repairSessionID = failed.Worker.EngineResult.SessionID
					recoveryContext = validationRepairPromptContext(recoveryAttempt, options.RecoveryAttempts, evidence)
					identity := workeridentity.FromWorker(failed.Worker)
					emitProgress(options, ProgressEvent{Type: "prompt.recovering", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "repairing", WorkerID: failed.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: failed.Worker.LogPath, Duration: promptDuration(failed), ExitCode: failed.ExitCode, CompletionStatus: failed.CompletionStatus, NextPromptSafe: failed.NextPromptSafe, Reason: validationRepairMessage(recoveryAttempt, options.RecoveryAttempts, evidence), RecoveryAttempt: recoveryAttempt, RecoveryMode: recoveryMode, Completed: len(runState.Completed), Total: len(prompts)})
				}
			}
		}
		for {
			sessionID := sessionForPrompt(prompt, sequence.SessionID)
			if repairBaseline != nil {
				sessionID = repairSessionID
			}
			promptState, err = runPrompt(repoRoot, prompt, specContext, sessionID, recoveryContext, options, launcher, repairBaseline)
			promptState.RecoveryAttempts = recoveryAttempt
			promptState.RecoveryMode = recoveryMode
			if err == nil {
				break
			}
			if validationRepairUsed || recoveryAttempt >= options.RecoveryAttempts {
				break
			}
			if evidence, eligible := validationRepairEligibility(repoRoot, options.HomeDir, prompt, promptState); eligible {
				recoveryAttempt++
				validationRepairUsed = true
				recoveryMode = "validation-repair"
				promptState.RecoveryAttempts = recoveryAttempt
				promptState.RecoveryMode = recoveryMode
				baseline := *promptState.GitBaseline
				repairBaseline = &baseline
				repairSessionID = promptState.Worker.EngineResult.SessionID
				promptState.Status = "repairing"
				promptState.Error = evidence.Reason
				if err := store.savePrompt(promptState); err != nil {
					return summary, err
				}
				sequence.mark(prompt.Name, "running", promptState.Worker, nil, validationRepairMessage(recoveryAttempt, options.RecoveryAttempts, evidence))
				sequence.setRecoveryAttempts(prompt.Name, recoveryAttempt)
				sequence.setRecoveryMode(prompt.Name, recoveryMode)
				sequence.refreshSummary()
				_ = sequenceStore.save(sequence)
				_ = store.saveSummary(sequence)
				identity := workeridentity.FromWorker(promptState.Worker)
				emitProgress(options, ProgressEvent{Type: "prompt.recovering", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "repairing", WorkerID: promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Reason: validationRepairMessage(recoveryAttempt, options.RecoveryAttempts, evidence), RecoveryAttempt: recoveryAttempt, RecoveryMode: recoveryMode, Completed: len(runState.Completed), Total: len(prompts)})
				recoveryContext = validationRepairPromptContext(recoveryAttempt, options.RecoveryAttempts, evidence)
				continue
			}
			if !recoverableFailure(promptState, err) {
				break
			}
			if artifact, isolateErr := isolateRecoveryChanges(repoRoot, options.HomeDir, sequence.SequenceID, prompt, promptState); isolateErr != nil {
				promptState.RecoveryArtifact = artifact
				err = fmt.Errorf("automatic recovery blocked: %w", isolateErr)
				break
			} else {
				promptState.RecoveryArtifact = artifact
			}
			recoveryAttempt++
			recoveryMode = "runtime-recovery"
			promptState.Status = "recovering"
			promptState.Error = err.Error()
			promptState.RecoveryMode = recoveryMode
			if promptState.Worker.ID == "" {
				promptState.Worker = state.Worker{ID: promptState.WorkerID, ExitCode: promptState.ExitCode}
			}
			if err := store.savePrompt(promptState); err != nil {
				return summary, err
			}
			sequence.mark(prompt.Name, "running", promptState.Worker, nil, recoveryMessage(recoveryAttempt, options.RecoveryAttempts, err))
			sequence.setRecoveryAttempts(prompt.Name, recoveryAttempt)
			sequence.setRecoveryMode(prompt.Name, recoveryMode)
			sequence.setRecoveryArtifact(prompt.Name, promptState.RecoveryArtifact)
			sequence.refreshSummary()
			_ = sequenceStore.save(sequence)
			_ = store.saveSummary(sequence)
			identity := workeridentity.FromWorker(promptState.Worker)
			emitProgress(options, ProgressEvent{Type: "prompt.recovering", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "recovering", WorkerID: promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Reason: recoveryMessage(recoveryAttempt, options.RecoveryAttempts, err), RecoveryAttempt: recoveryAttempt, RecoveryMode: recoveryMode, RecoveryArtifact: promptState.RecoveryArtifact, Completed: len(runState.Completed), Total: len(prompts)})
			recoveryContext = recoveryPromptContext(recoveryAttempt, options.RecoveryAttempts, err, promptState)
		}
		if err != nil {
			promptState.Status = "failed"
			promptState.Error = err.Error()
			if promptState.FailureReport == nil {
				promptState.FailureReport = state.FailureReportForFailure(promptState.Worker.EngineResult, err.Error())
			}
			if promptState.Worker.EngineResult == nil {
				promptState.Worker.EngineResult = &state.EngineResult{FailureReport: promptState.FailureReport}
			} else if promptState.Worker.EngineResult.FailureReport == nil {
				promptState.Worker.EngineResult.FailureReport = promptState.FailureReport
			}
			if promptState.Worker.ID == "" {
				promptState.Worker = state.Worker{ID: promptState.WorkerID, ExitCode: promptState.ExitCode}
			}
			sequence.mark(prompt.Name, "failed", promptState.Worker, promptState.FinishedAt, err.Error())
			sequence.setRecoveryAttempts(prompt.Name, recoveryAttempt)
			sequence.setRecoveryMode(prompt.Name, recoveryMode)
			sequence.setRecoveryArtifact(prompt.Name, promptState.RecoveryArtifact)
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
			identity := workeridentity.FromWorker(promptState.Worker)
			reason := promptState.CompletionReason
			if reason == "" {
				reason = err.Error()
			}
			emitProgress(options, ProgressEvent{Type: "prompt.failed", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "failed", WorkerID: promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Reason: reason, FailureReport: promptState.FailureReport, RecoveryArtifact: promptState.RecoveryArtifact, Completed: len(runState.Completed), Total: len(prompts)})
			return summary, err
		}
		if promptState.GateOutcome == "BLOCKED" {
			if err := store.savePrompt(promptState); err != nil {
				return summary, err
			}
			sequence.mark(prompt.Name, "gate-blocked", promptState.Worker, promptState.FinishedAt, promptState.CompletionReason)
			sequence.refreshSummary()
			finished := time.Now().UTC()
			sequence.FinishedAt = &finished
			_ = sequenceStore.save(sequence)
			_ = store.saveSummary(sequence)
			runState.Status = "product-blocked"
			runState.Current = prompt.Name
			runState.Completed = append(runState.Completed, prompt.Name)
			runState.touch()
			_ = store.saveRun(runState)
			summary.Run, summary.Sequence = runState, &sequence
			identity := workeridentity.FromWorker(promptState.Worker)
			emitProgress(options, ProgressEvent{Type: "prompt.gate-blocked", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "gate-blocked", WorkerID: promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Reason: promptState.CompletionReason, FailureReport: promptState.FailureReport, Completed: len(runState.Completed), Total: len(prompts)})
			emitProgress(options, ProgressEvent{Type: "run.product-blocked", SequenceID: sequence.SequenceID, Status: "product-blocked", Reason: promptState.CompletionReason, Completed: len(runState.Completed), Total: len(prompts)})
			return summary, nil
		}
		if err := store.savePrompt(promptState); err != nil {
			return summary, err
		}
		if promptState.Worker.EngineResult != nil && promptState.Worker.EngineResult.SessionID != "" {
			sequence.SessionID = promptState.Worker.EngineResult.SessionID
		}
		sequence.mark(prompt.Name, "succeeded", promptState.Worker, promptState.FinishedAt, "")
		sequence.setRecoveryAttempts(prompt.Name, recoveryAttempt)
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
		identity := workeridentity.FromWorker(promptState.Worker)
		emitProgress(options, ProgressEvent{Type: "prompt.succeeded", SequenceID: sequence.SequenceID, PromptName: prompt.Name, PromptType: prompt.Type, Status: "succeeded", WorkerID: promptState.WorkerID, Scope: identity.Scope, Engine: identity.Engine, Model: identity.Model, LogPath: promptState.Worker.LogPath, Duration: promptDuration(promptState), ExitCode: promptState.ExitCode, CompletionStatus: promptState.CompletionStatus, NextPromptSafe: promptState.NextPromptSafe, Completed: len(runState.Completed), Total: len(prompts)})
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

func sequenceItem(sequence SequenceState, promptName string) SequenceItem {
	for _, item := range sequence.Items {
		if item.PromptName == promptName {
			return item
		}
	}
	return SequenceItem{}
}

func sessionForPrompt(prompt Prompt, sessionID string) string {
	if prompt.ContextMode == ContextFresh {
		return ""
	}
	return sessionID
}

func validatePrompts(prompts []Prompt) error {
	for _, prompt := range prompts {
		data, err := os.ReadFile(prompt.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", prompt.Name, err)
		}
		parsed, err := markdown.Parse(string(data))
		if err != nil {
			return fmt.Errorf("parse %s: %w", prompt.Name, err)
		}
		if err := markdown.Validate(parsed, prompt.Path); err != nil {
			return err
		}
		policy := workerdomain.WorkerPolicy{AllowedPaths: metadataStrings(parsed.Metadata, "allowed_paths"), ForbiddenPaths: metadataStrings(parsed.Metadata, "forbidden_paths")}
		if err := workerpathpolicy.ValidatePatterns(policy); err != nil {
			return fmt.Errorf("task frontmatter %s: %w", prompt.Name, err)
		}
		expected, err := markdown.ExpectedPaths(parsed)
		if err != nil {
			return fmt.Errorf("task frontmatter %s: %w", prompt.Name, err)
		}
		if len(expected) > 0 {
			violations, err := promptPolicyViolations(prompt, policy, expected)
			if err != nil {
				return fmt.Errorf("task frontmatter %s: validate expected_paths: %w", prompt.Name, err)
			}
			if len(violations) > 0 {
				return fmt.Errorf("task frontmatter %s: expected_paths are not permitted by the path policy: %s", prompt.Name, formatViolations(violations))
			}
		}
	}
	return nil
}

func metadataStrings(metadata map[string]any, key string) []string {
	values, _ := metadata[key].([]any)
	out := make([]string, 0, len(values))
	for _, raw := range values {
		if value, ok := raw.(string); ok {
			out = append(out, value)
		}
	}
	return out
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

func runPrompt(repoRoot string, prompt Prompt, specContext, sessionID, recoveryContext string, options Options, launcher Launcher, repairBaseline *workerpathpolicy.Snapshot) (PromptState, error) {
	started := time.Now().UTC()
	promptState := PromptState{Prompt: prompt.Name, PromptType: prompt.Type, Status: "running", StartedAt: &started}
	policy, err := taskPathPolicy(prompt.Path)
	if err != nil {
		return promptState, err
	}
	needsGitTracking := options.Checkpoint || options.CommitEach || options.RequireCleanGit || options.RecoveryAttempts > 0 || len(policy.AllowedPaths) != 0 || len(policy.ForbiddenPaths) != 0 || roleHasPathPolicy(prompt.RolePolicy)
	var baseline workerpathpolicy.Snapshot
	if needsGitTracking {
		if repairBaseline != nil {
			baseline = *repairBaseline
		} else {
			baseline, err = workerpathpolicy.Capture(repoRoot)
			if err != nil {
				return promptState, fmt.Errorf("capture worker Git baseline: %w", err)
			}
			baseline.Entries = withoutPromptGrinderPaths(baseline.Entries)
		}
		promptState.GitBaseline = &baseline
	}
	// Focused automatic commits are only attributable from a clean baseline.
	// --require-clean-git=false remains useful for non-committing runs, but is
	// deliberately not an unsafe acknowledgement for automatic commits.
	if repairBaseline == nil && (options.RequireCleanGit || options.CommitEach) && len(baseline.Entries) != 0 {
		return promptState, fmt.Errorf("working tree is dirty; automatic commits require a clean baseline (use --commit-each=false to retain unrelated changes)")
	}
	if options.Checkpoint || options.CommitEach {
		promptState.GitSHABefore, _ = gitSHA(repoRoot)
	}
	content, err := assemblePrompt(prompt.Path, specContext, options.CommitEach, prompt.RolePolicy, prompt.GateOutcome)
	if err != nil {
		return promptState, err
	}
	if recoveryContext != "" {
		content += recoveryContext
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
	if worker.EngineResult != nil {
		promptState.EngineSessionID = worker.EngineResult.SessionID
		promptState.CompletionStatus, promptState.NextPromptSafe, promptState.CompletionReason = state.ParseOrderedCompletionReport(worker.EngineResult.Summary)
		promptState.Worker.EngineResult.CompletionStatus = promptState.CompletionStatus
		promptState.Worker.EngineResult.NextPromptSafe = promptState.NextPromptSafe
		promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
		if promptState.Worker.EngineResult.FailureReport == nil {
			promptState.Worker.EngineResult.FailureReport = state.ParseFailureReport(promptState.Worker.EngineResult.Summary)
		}
		promptState.FailureReport = promptState.Worker.EngineResult.FailureReport
		if prompt.GateOutcome == "BLOCKED" {
			if promptState.CompletionStatus == "BLOCKED" && promptState.NextPromptSafe != nil && !*promptState.NextPromptSafe {
				promptState.GateOutcome = "BLOCKED"
				promptState.CompletionReason = "declared capability gate completed: product outcome BLOCKED"
				promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
			} else {
				promptState.CompletionReason = "declared gate_outcome BLOCKED requires STATUS: BLOCKED and NEXT_PROMPT_SAFE: no"
				promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
			}
		} else if semanticErr := promptState.Worker.EngineResult.OrderedCompletionError(); semanticErr != nil {
			promptState.CompletionReason = semanticErr.Error()
			promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
		}
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
	if worker.EngineResult == nil {
		promptState.CompletionReason = "worker produced no structured completion result"
		promptState.FailureReport = state.FailureReportForFailure(nil, promptState.CompletionReason)
	}
	if promptState.CompletionReason != "" && promptState.GateOutcome == "" {
		if promptState.Worker.EngineResult == nil {
			promptState.Worker.EngineResult = &state.EngineResult{CompletionReason: promptState.CompletionReason, FailureReport: promptState.FailureReport}
		} else {
			promptState.Worker.EngineResult.CompletionReason = promptState.CompletionReason
		}
		if promptState.FailureReport == nil {
			promptState.FailureReport = state.FailureReportForFailure(promptState.Worker.EngineResult, promptState.CompletionReason)
			promptState.Worker.EngineResult.FailureReport = promptState.FailureReport
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
	var changed []string
	if needsGitTracking {
		changed, err = attributedWorkerChanges(repoRoot, baseline)
	}
	if err != nil {
		return promptState, fmt.Errorf("attribute worker changes: %w", err)
	}
	violations, err := promptPolicyViolations(prompt, policy, changed)
	if err != nil {
		return promptState, fmt.Errorf("evaluate task path policy: %w", err)
	}
	if len(violations) != 0 {
		promptState.FilesChanged = changed
		reason := fmt.Sprintf("path policy violation at completion: %s; changes retained for review", formatViolations(violations))
		promptState.CompletionReason = reason
		return promptState, errors.New(reason)
	}
	if options.Checkpoint || options.CommitEach {
		promptState.GitSHAAfter, _ = gitSHA(repoRoot)
		promptState.FilesChanged = changed
	}
	if options.CommitEach && len(promptState.FilesChanged) > 0 {
		commit, err := gitCommitFocused(repoRoot, "PromptGrinder: complete "+prompt.Name, baseline, promptState.FilesChanged)
		if err != nil {
			finished := time.Now().UTC()
			promptState.FinishedAt = &finished
			return promptState, err
		}
		promptState.CommitSHA = commit
		promptState.GitSHAAfter, _ = gitSHA(repoRoot)
	}
	finished := time.Now().UTC()
	promptState.Status = "completed"
	promptState.FinishedAt = &finished
	return promptState, nil
}

func taskPathPolicy(taskPath string) (workerdomain.WorkerPolicy, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return workerdomain.WorkerPolicy{}, err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return workerdomain.WorkerPolicy{}, err
	}
	policy := workerdomain.WorkerPolicy{AllowedPaths: metadataStrings(task.Metadata, "allowed_paths"), ForbiddenPaths: metadataStrings(task.Metadata, "forbidden_paths")}
	return policy, nil
}

func withoutPromptGrinderPaths(entries map[string]string) map[string]string {
	out := make(map[string]string, len(entries))
	for name, identity := range entries {
		if !isPromptGrinderStatePath(name) {
			out[name] = identity
		}
	}
	return out
}

func attributedWorkerChanges(repo string, baseline workerpathpolicy.Snapshot) ([]string, error) {
	paths, err := workerpathpolicy.AttributedChanges(repo, baseline)
	if err != nil {
		return nil, err
	}
	result := paths[:0]
	for _, name := range paths {
		if !isPromptGrinderStatePath(name) {
			result = append(result, name)
		}
	}
	return result, nil
}

func formatViolations(violations []workerpathpolicy.Violation) string {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, fmt.Sprintf("%s (%s)", violation.Path, violation.Reason))
	}
	return strings.Join(parts, ", ")
}

func roleHasPathPolicy(role *RolePolicy) bool {
	return role != nil && len(role.AllowedPaths) > 0
}

func promptPolicyViolations(prompt Prompt, slicePolicy workerdomain.WorkerPolicy, paths []string) ([]workerpathpolicy.Violation, error) {
	violations, err := workerpathpolicy.Violations(slicePolicy, paths)
	if err != nil || prompt.RolePolicy == nil || len(prompt.RolePolicy.AllowedPaths) == 0 {
		return violations, err
	}
	rolePolicy := workerdomain.WorkerPolicy{AllowedPaths: prompt.RolePolicy.AllowedPaths}
	roleViolations, err := workerpathpolicy.Violations(rolePolicy, paths)
	if err != nil {
		return nil, err
	}
	for index := range roleViolations {
		roleViolations[index].Reason = "outside role allowed paths"
	}
	return append(violations, roleViolations...), nil
}

func assemblePrompt(path, specContext string, commitEach bool, rolePolicy *RolePolicy, gateOutcome string) (string, error) {
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
	assembledBody += rolePolicyPrompt(rolePolicy)
	assembledBody += body
	if commitEach {
		assembledBody += promptGrinderCommitOwnershipContract
	}
	assembledBody += orderedCompletionContract
	if gateOutcome == "BLOCKED" {
		assembledBody += capabilityGateCompletionContract
	}
	if ok {
		return frontmatter + assembledBody, nil
	}
	return assembledBody, nil
}

func rolePolicyPrompt(role *RolePolicy) string {
	if role == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Effective Role Policy\n\n")
	fmt.Fprintf(&b, "Role: `%s`\n\n", role.ID)
	if role.Description != "" {
		b.WriteString(role.Description + "\n\n")
	}
	if len(role.AllowedPaths) > 0 {
		b.WriteString("## Role Scope\n\nThe role boundary is enforced in addition to this slice's path policy. Do not modify files outside:\n\n")
		for _, pattern := range role.AllowedPaths {
			fmt.Fprintf(&b, "- `%s`\n", pattern)
		}
		b.WriteByte('\n')
	}
	b.WriteString("## Validation Boundary\n\nRun only validation declared by this slice. Role quality gates are readiness guidance, not required commands for this intermediate slice.\n\n")
	if role.Runtime.Model != "" || role.Runtime.MaxCost != "" || len(role.Runtime.Capabilities) > 0 {
		b.WriteString("## Model Selection\n\nThis role supplies model defaults. A slice may explicitly override them in `engine`.\n\n")
		if role.Runtime.Model != "" {
			fmt.Fprintf(&b, "- Model: `%s`\n", role.Runtime.Model)
		}
		if role.Runtime.MaxCost != "" {
			fmt.Fprintf(&b, "- Maximum cost tier: `%s`\n", role.Runtime.MaxCost)
		}
		if len(role.Runtime.Capabilities) > 0 {
			fmt.Fprintf(&b, "- Required capabilities: `%s`\n", strings.Join(role.Runtime.Capabilities, "`, `"))
		}
		b.WriteByte('\n')
	}
	if len(role.QualityGates) > 0 {
		b.WriteString("## Role Readiness Guidance\n\n")
		for _, gate := range role.QualityGates {
			fmt.Fprintf(&b, "- %s\n", gate)
		}
		b.WriteString("\nDo not run these gates unless the slice's validation or task body explicitly requires them.\n\n")
	}
	return b.String()
}

const promptGrinderCommitOwnershipContract = `

# Commit Ownership

PromptGrinder is running with --commit-each and owns the checkpoint commit for this slice. Do not run git commit, amend a commit, or otherwise move HEAD. Leave approved changes in the worktree for PromptGrinder to validate and commit.
`

const orderedCompletionContract = `

# Required Completion Report

End your final answer with exactly one occurrence of each field:

STATUS: PASS
NEXT_PROMPT_SAFE: yes

Use STATUS: BLOCKED or STATUS: PARTIAL and NEXT_PROMPT_SAFE: no when the task is not fully and safely complete. PromptGrinder will not advance to a later slice unless the final report is unambiguous PASS/yes.

When reporting PARTIAL or BLOCKED, include the following concise optional headings when evidence is available. They are rendered in the terminal and sequence JSON; keep details bounded and put exhaustive output in the worker log or a handoff file.

Failure category: product-test|environment-capability|path-policy|worker-crash|cancellation
Failure summary: one-line reason
Feature evidence:
- completed check
Blocking checks:
- check: failed or not run
  - concise detail
Evidence report: relative/path/to/report.md
Next action: one-line repair or configuration action
`

const capabilityGateCompletionContract = `

# Capability-Gate Outcome

This slice is an explicitly declared hard-gate audit. If your completed audit
establishes that the prerequisite is unavailable, end with exactly:

STATUS: BLOCKED
NEXT_PROMPT_SAFE: no

That records a successful capability gate: PromptGrinder will checkpoint only
your permitted evidence and mark product implementation blocked. Do not report
PASS merely to advance the sequence. Any other completion remains an ordinary
completion-contract failure.
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
	Root       string
	LegacyRoot string
}

// noResumableRunStateError identifies an explicit --resume request that chose
// a sequence identity without a persisted execution run state. This can occur
// after validation-only preflight selected a new identity; it is not safe to
// treat that record as resumable.
type noResumableRunStateError struct {
	Root   string
	Folder string
}

func (e noResumableRunStateError) Error() string {
	return fmt.Sprintf("No resumable run state was found in %s; this can happen when --resume selected a validation-only preflight record.\nStart fresh: promptgrinder run-folder %s --fresh", e.Root, shellQuoteRunFolder(e.Folder))
}

func shellQuoteRunFolder(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`!&;|<>()[]{}*?") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func folderStateRoot(homeDir, sequenceID string) string {
	sequenceRoot := newSequenceStore(homeDir).Root
	resolvedHome := filepath.Dir(filepath.Dir(sequenceRoot))
	return filepath.Join(resolvedHome, "state", "run-folders", sequenceID)
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
				return RunState{}, false, noResumableRunStateError{Root: s.Root, Folder: folder}
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
	if errors.Is(err, os.ErrNotExist) && s.LegacyRoot != "" {
		data, err = os.ReadFile(filepath.Join(s.LegacyRoot, "run.json"))
	}
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
		// A recovery preflight can fail before it launches a replacement worker.
		// Keep the original worker evidence rather than replacing it with an
		// empty/default identity in the final failed sequence row.
		if worker.ID != "" {
			item.WorkerID = worker.ID
		}
		if worker.LogPath != "" {
			item.LogPath = worker.LogPath
		}
		if worker.ExitCode != nil {
			item.ExitCode = worker.ExitCode
		}
		if worker.ID != "" {
			identity := workeridentity.FromWorker(worker)
			item.Scope = identity.Scope
			item.Engine = identity.Engine
			item.Model = identity.Model
		}
		item.Error = errorMessage
		if worker.EngineResult != nil {
			if worker.EngineResult.EngineExitCode != nil {
				item.ExitCode = worker.EngineResult.EngineExitCode
			}
			item.CompletionStatus = worker.EngineResult.CompletionStatus
			item.NextPromptSafe = worker.EngineResult.NextPromptSafe
			item.CompletionReason = worker.EngineResult.CompletionReason
			item.FailureReport = worker.EngineResult.FailureReport
			item.TokenUsage = addTokenUsage(item.TokenUsage, tokenUsageFromEngineResult(worker.EngineResult))
		}
		if errorMessage != "" && item.CompletionReason == "" {
			item.CompletionReason = errorMessage
		}
		break
	}
	s.Status = sequenceStatus(s.Items)
	s.touch()
}

func (s *SequenceState) setRecoveryAttempts(promptName string, attempts int) {
	for i := range s.Items {
		if s.Items[i].PromptName == promptName {
			s.Items[i].RecoveryAttempts = attempts
			return
		}
	}
}

func (s *SequenceState) setRecoveryMode(promptName, mode string) {
	if mode == "" {
		return
	}
	for i := range s.Items {
		if s.Items[i].PromptName == promptName {
			s.Items[i].RecoveryMode = mode
			return
		}
	}
}

func (s *SequenceState) setRecoveryArtifact(promptName, artifact string) {
	if artifact == "" {
		return
	}
	for i := range s.Items {
		if s.Items[i].PromptName == promptName {
			s.Items[i].RecoveryArtifact = artifact
			return
		}
	}
}

func recoverableFailure(prompt PromptState, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error() + " " + prompt.CompletionReason)
	for _, unsafe := range []string{
		"path policy violation", "outside allowed paths", "forbidden paths",
		"model preflight", "not selectable by this codex runtime",
		"working tree is dirty", "cancelled", "preflight",
	} {
		if strings.Contains(text, unsafe) {
			return false
		}
	}
	if strings.Contains(text, "did not satisfy ordered completion contract") ||
		strings.Contains(text, "path policy violation") ||
		strings.Contains(text, "timeout") {
		return false
	}
	// A launch failure has no completed worker command to preserve and remains
	// safely retryable. Once a worker ran, retry only when its durable log
	// contains the exact runtime-side client-disconnect evidence. This avoids
	// treating compiler/test failures that happen to mention cancellation as
	// infrastructure failures.
	if prompt.WorkerID == "" || prompt.Worker.Status == state.StatusLaunchFailed || strings.Contains(text, "launch failed") {
		return true
	}
	return runtimeClientDisconnected(prompt.Worker.LogPath)
}

func recoveryMessage(attempt, maximum int, err error) string {
	return fmt.Sprintf("automatic recovery attempt %d of %d after: %s", attempt, maximum, err)
}

func validationRepairMessage(attempt, maximum int, evidence validationRepairEvidence) string {
	return fmt.Sprintf("validation-repair attempt %d of %d: %s (%s)", attempt, maximum, evidence.Reason, evidence.Command)
}

func recoveryPromptContext(attempt, maximum int, err error, previous PromptState) string {
	var b strings.Builder
	b.WriteString("\n# Recovery Attempt\n\n")
	fmt.Fprintf(&b, "PromptGrinder is retrying this same slice (attempt %d of %d). Earlier successful slices remain complete; do not broaden this slice's scope.\n\n", attempt, maximum)
	b.WriteString("The previous attempt failed. Diagnose and correct a recoverable execution, reasoning, or completion-report problem before continuing. Preserve permitted existing changes, do not bypass path policy, do not change model or safety configuration, and emit the required completion report exactly once.\n\n")
	fmt.Fprintf(&b, "Previous failure: %s\n", err)
	if previous.CompletionReason != "" {
		fmt.Fprintf(&b, "Previous completion reason: %s\n", previous.CompletionReason)
	}
	return b.String()
}

func validationRepairPromptContext(attempt, maximum int, evidence validationRepairEvidence) string {
	return fmt.Sprintf(`
# Validation Repair Attempt

PromptGrinder is continuing this same slice and runtime session for one bounded validation repair attempt (%d of %d). Your earlier declared validation command failed:

    %s

The current worktree contains only this slice's approved, uncommitted changes. Inspect and repair those changes only within the declared scope. Re-run the failed validation and every required check. Do not commit, stash, reset, discard, or broaden scope. Return STATUS: PASS and NEXT_PROMPT_SAFE: yes only after completed validation evidence.
`, attempt, maximum, evidence.Command)
}

func sequenceStatus(items []SequenceItem) string {
	hasFailed := false
	hasInterrupted := false
	hasCancelled := false
	hasProductBlocked := false
	hasPending := false
	for _, item := range items {
		switch item.Status {
		case "failed":
			hasFailed = true
		case "interrupted":
			hasInterrupted = true
		case "cancelled":
			hasCancelled = true
		case "gate-blocked":
			hasProductBlocked = true
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
	if hasCancelled {
		return "cancelled"
	}
	if hasProductBlocked {
		return "product-blocked"
	}
	if hasPending {
		return "running"
	}
	return "completed"
}

type SequenceProgress struct {
	SequenceID     string     `json:"sequence_id"`
	Folder         string     `json:"folder"`
	Status         string     `json:"status"`
	Total          int        `json:"total"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Interrupted    int        `json:"interrupted"`
	Cancelled      int        `json:"cancelled"`
	ProductBlocked int        `json:"product_blocked"`
	Pending        int        `json:"pending"`
	Current        string     `json:"current"`
	Next           string     `json:"next"`
	LastWorkerID   string     `json:"last_worker_id"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
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
		case "cancelled":
			progress.Cancelled++
		case "gate-blocked":
			progress.Succeeded++
			progress.ProductBlocked++
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
	fmt.Fprintf(&b, "- Prompts: %d total, %d succeeded, %d product-blocked, %d failed, %d pending\n", progress.Total, progress.Succeeded, progress.ProductBlocked, progress.Failed, progress.Pending)
	fmt.Fprintf(&b, "- Reported token usage: %s\n", s.TokenUsage)
	if len(s.Items) > 0 {
		fmt.Fprintln(&b, "\n## Token Usage by Slice")
		for _, item := range s.Items {
			if item.Status == "skipped" {
				continue
			}
			if item.TokenUsage == nil {
				fmt.Fprintf(&b, "- `%s`: unavailable\n", item.PromptName)
				continue
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", item.PromptName, *item.TokenUsage)
		}
	}
	if len(s.Adoptions) > 0 {
		fmt.Fprintf(&b, "\n## Sequence Adoption\n")
		for _, adoption := range s.Adoptions {
			fmt.Fprintf(&b, "\n- %s: explicitly adopted `%s`; retained %d succeeded/skipped slice(s); restarted at `%s`.\n", adoption.AdoptedAt.Format(time.RFC3339), adoption.SequenceID, len(adoption.RetainedPrompts), adoption.RestartAt)
			if adoption.MigratedLegacyState {
				fmt.Fprintf(&b, "  - Legacy state fingerprints were migrated from verified prompt/checkpoint evidence.\n")
			}
			for _, change := range adoption.PolicyHashChanges {
				if change.Retained {
					fmt.Fprintf(&b, "  - `%s` role-policy fingerprint changed from `%s` to `%s`; completed work was retained.\n", change.PromptName, shortHash(change.PreviousHash), shortHash(change.CurrentHash))
				} else {
					fmt.Fprintf(&b, "  - `%s` role-policy fingerprint changed from `%s` to `%s`; the remaining slice was revalidated.\n", change.PromptName, shortHash(change.PreviousHash), shortHash(change.CurrentHash))
				}
			}
		}
	}
	fmt.Fprintf(&b, "\n## Executive Summary\n\n%s\n", valueOrDefault(s.ExecutiveSummary, "No prompt results have been recorded yet."))
	return b.String()
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return valueOrDefault(value, "none")
	}
	return value[:12]
}

func totalTokenUsage(items []SequenceItem) TokenUsage {
	total := TokenUsage{}
	for _, item := range items {
		if item.TokenUsage == nil {
			continue
		}
		total.Available = true
		total.Input += item.TokenUsage.Input
		total.CachedInput += item.TokenUsage.CachedInput
		total.Output += item.TokenUsage.Output
		total.ReasoningOutput += item.TokenUsage.ReasoningOutput
		total.Total += item.TokenUsage.Total
	}
	return total
}

func tokenUsageFromEngineResult(result *state.EngineResult) *TokenUsage {
	if result == nil || result.TokensTotal == nil {
		return nil
	}
	usage := TokenUsage{Available: true, Total: *result.TokensTotal}
	if result.TokensInput != nil {
		usage.Input = *result.TokensInput
	}
	if result.TokensCachedInput != nil {
		usage.CachedInput = *result.TokensCachedInput
	}
	if result.TokensOutput != nil {
		usage.Output = *result.TokensOutput
	}
	if result.TokensReasoningOutput != nil {
		usage.ReasoningOutput = *result.TokensReasoningOutput
	}
	return &usage
}

func addTokenUsage(existing, reported *TokenUsage) *TokenUsage {
	if reported == nil {
		return existing
	}
	if existing == nil {
		copy := *reported
		return &copy
	}
	return &TokenUsage{
		Available:       true,
		Input:           existing.Input + reported.Input,
		CachedInput:     existing.CachedInput + reported.CachedInput,
		Output:          existing.Output + reported.Output,
		ReasoningOutput: existing.ReasoningOutput + reported.ReasoningOutput,
		Total:           existing.Total + reported.Total,
	}
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

// CancelSequence marks unfinished work as cancelled while preserving completed
// checkpoints. The sequence remains resumable because a later run treats
// cancelled items as unfinished work.
func CancelSequence(homeDir, sequenceID string) (SequenceState, error) {
	store := newSequenceStore(homeDir)
	sequence, err := store.load(sequenceID)
	if err != nil {
		return SequenceState{}, err
	}
	if sequence.Status == "completed" || sequence.Status == "failed" || sequence.Status == "product-blocked" {
		return sequence, fmt.Errorf("sequence %s is already %s", sequenceID, sequence.Status)
	}
	now := time.Now().UTC()
	for i := range sequence.Items {
		switch sequence.Items[i].Status {
		case "succeeded", "skipped", "failed", "gate-blocked":
			continue
		default:
			sequence.Items[i].Status = "cancelled"
			sequence.Items[i].FinishedAt = &now
			sequence.Items[i].Error = "cancelled by user"
		}
	}
	sequence.Status = "cancelled"
	sequence.FinishedAt = &now
	sequence.UpdatedAt = &now
	sequence.refreshSummary()
	if err := store.save(sequence); err != nil {
		return SequenceState{}, err
	}
	return sequence, nil
}

// StopSequenceSupervisor signals only a process whose persisted supervisor
// record still matches the sequence state. This prevents stale sequence data
// from terminating an unrelated process after PID reuse.
func StopSequenceSupervisor(homeDir string, supervisor *Supervisor) error {
	if supervisor == nil || supervisor.ID == "" || supervisor.PID <= 0 || supervisor.PID == os.Getpid() {
		return nil
	}
	path := filepath.Join(homeDir, "state", "supervisors", safeName(supervisor.ID)+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var record Supervisor
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if record.ID != supervisor.ID || record.PID != supervisor.PID || record.Status != "running" {
		return nil
	}
	if err := syscall.Kill(record.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	now := time.Now().UTC()
	record.Status = "cancelled"
	record.HeartbeatAt = &now
	record.FinishedAt = &now
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
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

func (s sequenceStore) loadOrCreate(folder, repoRoot string, prompts []Prompt, options Options) (SequenceState, bool, *SequenceAdoption, error) {
	if options.ResumeSequence != "" {
		sequence, adoption, err := s.validateExplicitAdoption(folder, repoRoot, prompts, options.ResumeSequence)
		return sequence, err == nil, adoption, err
	}
	base, err := buildSequence(folder, repoRoot, prompts, options)
	if err != nil {
		return SequenceState{}, false, nil, err
	}
	if !options.Restart && !options.NoResume && !options.Fresh {
		existing, err := s.load(base.SequenceID)
		if err == nil {
			return existing, true, nil, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SequenceState{}, false, nil, err
		}
		compatible, found, err := s.findCompatibleResume(folder, repoRoot, prompts)
		if err != nil {
			return SequenceState{}, false, nil, err
		}
		if found {
			return compatible, true, nil, nil
		}
	}
	return base, false, nil, nil
}

// findCompatibleResume adopts only an unfinished sequence whose completed
// prefix still matches the current prompt files. It lets users repair a later
// slice or role policy without rerunning successful earlier slices. This is
// the default when an exact sequence identity no longer exists; --fresh,
// --restart, and --no-resume remain the explicit ways to discard that prefix.
func (s sequenceStore) findCompatibleResume(folder, repoRoot string, prompts []Prompt) (SequenceState, bool, error) {
	sequences, err := s.list()
	if err != nil {
		return SequenceState{}, false, err
	}
	for _, sequence := range sequences {
		if sequence.Folder != folder || (sequence.RepositoryPath != "" && sequence.RepositoryPath != repoRoot) || sequence.Status == "completed" || sequence.Status == "cancelled" || sequence.Status == "product-blocked" || len(sequence.Items) != len(prompts) {
			continue
		}
		compatible := true
		seenIncomplete := false
		for index, item := range sequence.Items {
			if item.PromptName != prompts[index].Name {
				compatible = false
				break
			}
			complete := item.Status == "succeeded" || item.Status == "skipped"
			if !complete {
				seenIncomplete = true
				continue
			}
			if seenIncomplete {
				compatible = false
				break
			}
			rawHash, rawErr := fileHash(prompts[index].Path)
			effectiveHash, hashErr := promptContentHash(prompts[index].Path, prompts[index].RolePolicy)
			contentMatches := item.PromptHash != "" && item.PromptHash == rawHash
			legacyMatches := item.PromptHash == "" && (item.ContentHash == effectiveHash || item.ContentHash == rawHash)
			if hashErr != nil || rawErr != nil || (!contentMatches && !legacyMatches) {
				compatible = false
				break
			}
		}
		if compatible {
			return sequence, true, nil
		}
	}
	return SequenceState{}, false, nil
}

func compatibleResumePlan(sequence SequenceState, requestedSequenceID string, adopted bool) string {
	if !adopted || sequence.SequenceID == requestedSequenceID {
		return ""
	}
	retained := 0
	restartAt := "the first unfinished slice"
	for _, item := range sequence.Items {
		if item.Status == "succeeded" {
			retained++
		}
		if item.Status == "succeeded" || item.Status == "skipped" {
			continue
		}
		restartAt = item.PromptName
		break
	}
	return fmt.Sprintf("Compatible sequence %s adopted automatically: retaining %d successful slice(s); restarting at %s. Use --fresh to rerun all slices.", sequence.SequenceID, retained, restartAt)
}

func explicitAdoptionPlan(adoption SequenceAdoption) string {
	policyNote := ""
	if len(adoption.PolicyHashChanges) > 0 {
		policyNote = fmt.Sprintf(" %d role-policy fingerprint change(s) recorded; completed slices remain retained.", len(adoption.PolicyHashChanges))
	}
	return fmt.Sprintf("Sequence %s explicitly adopted: retaining %d succeeded/skipped slice(s); restarting at %s.%s", adoption.SequenceID, len(adoption.RetainedPrompts), adoption.RestartAt, policyNote)
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
		hash, err := promptContentHash(prompt.Path, prompt.RolePolicy)
		if err != nil {
			return SequenceState{}, err
		}
		rawHash, err := fileHash(prompt.Path)
		if err != nil {
			return SequenceState{}, err
		}
		policyHash, err := promptPolicyHash(repoRoot, prompt)
		if err != nil {
			return SequenceState{}, err
		}
		items = append(items, SequenceItem{PromptPath: prompt.Path, PromptName: prompt.Name, PromptID: prompt.ID, DependsOn: append([]string(nil), prompt.DependsOn...), ContextMode: prompt.ContextMode, GateOutcome: prompt.GateOutcome, PromptHash: rawHash, PolicyHash: policyHash, ContentHash: hash, Status: "pending"})
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
	return SequenceState{StateVersion: sequenceStateVersion, SequenceID: id, Folder: folder, RepositoryPath: repoRoot, Status: "running", Template: options.Template, Engine: sequenceEngineLabel(engines, options.EngineOverride), Items: items, CreatedAt: &now, StartedAt: &now, UpdatedAt: &now}, nil
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

func promptContentHash(path string, role *RolePolicy) (string, error) {
	hash, err := fileHash(path)
	if err != nil || role == nil {
		return hash, err
	}
	sum := sha256.Sum256([]byte(hash + "\n" + role.identity()))
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

func gitCommitFocused(dir, message string, baseline workerpathpolicy.Snapshot, approved []string) (string, error) {
	if len(approved) == 0 {
		return "", nil
	}
	actual, err := attributedWorkerChanges(dir, baseline)
	if err != nil {
		return "", err
	}
	if !equalStrings(actual, approved) {
		return "", fmt.Errorf("worktree changed during worker attribution; refusing automatic commit")
	}
	args := []string{"-C", dir, "add", "--all", "--"}
	for _, name := range approved {
		args = append(args, ":(literal)"+name)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
	}
	staged, err := gitChangedPathSet(dir, "diff", "--cached", "--name-status", "-z", "--find-renames")
	if err != nil || !equalStrings(staged, approved) {
		return "", stagedChangeMismatchError(dir, baseline, approved)
	}
	preCommit, err := workerpathpolicy.Capture(dir)
	if err != nil {
		return "", fmt.Errorf("re-check worktree before commit: %w", err)
	}
	actual, err = attributedWorkerChanges(dir, baseline)
	if err != nil || preCommit.Head != baseline.Head || !equalStrings(actual, approved) {
		return "", fmt.Errorf("index or worktree changed before commit; refusing automatic commit")
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=PromptGrinder", "-c", "user.email=promptgrinder@example.invalid", "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
	}
	commit, err := gitSHA(dir)
	if err != nil {
		return "", err
	}
	committed, err := gitChangedPathSet(dir, "diff", "--name-status", "-z", "--find-renames", baseline.Head, commit)
	if err != nil || !equalStrings(committed, approved) {
		return "", fmt.Errorf("created commit does not exactly match approved worker changes")
	}
	return commit, nil
}

func stagedChangeMismatchError(dir string, baseline workerpathpolicy.Snapshot, approved []string) error {
	conflict, err := workerpathpolicy.DetectCommitOwnershipConflict(dir, baseline, approved)
	if err == nil && conflict != nil {
		return conflict
	}
	return fmt.Errorf("staged change set does not exactly match approved worker changes")
}

func gitNullPaths(dir string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			paths = append(paths, filepath.ToSlash(name))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func gitChangedPathSet(dir string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	var paths []string
	for i := 0; i < len(fields) && fields[i] != ""; {
		status := fields[i]
		i++
		count := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			count = 2
		}
		for j := 0; j < count && i < len(fields) && fields[i] != ""; j, i = j+1, i+1 {
			paths = append(paths, filepath.ToSlash(fields[i]))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func equalStrings(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isPromptGrinderStatePath(name string) bool {
	name = filepath.ToSlash(strings.TrimSpace(name))
	return legacyRunStatePath(name)
}
