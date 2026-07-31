package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"promptgrinder/internal/buildinfo"
	"promptgrinder/internal/config"
	"promptgrinder/internal/discovery"
	"promptgrinder/internal/engine"
	"promptgrinder/internal/firstuse"
	"promptgrinder/internal/review"
	"promptgrinder/internal/roleenhance"
	"promptgrinder/internal/runfolder"
	pgruntime "promptgrinder/internal/runtime"
	pgscheduler "promptgrinder/internal/scheduler"
	"promptgrinder/internal/state"
	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/taskstore"
	"promptgrinder/internal/ui"
	"promptgrinder/internal/worker"
	"promptgrinder/internal/workeradapter"
	"promptgrinder/internal/workercontrol"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerlaunch"
	"promptgrinder/internal/workerregistry"
	"promptgrinder/internal/workerstart"
	"promptgrinder/internal/workerstate"

	"github.com/spf13/cobra"
)

type Service interface {
	RunPath(path string) (pgruntime.RunSummary, error)
	RunPathWithOptions(path string, options pgruntime.RunOptions) (pgruntime.RunSummary, error)
	RunPathsWithOptions(paths []string, options pgruntime.RunOptions) (pgruntime.RunSummary, error)
	RunPromptFolder(path string, options pgruntime.RunFolderOptions) (pgruntime.RunFolderSummary, error)
	Engines() []engine.Descriptor
	DescribeEngine(name string) (engine.Descriptor, error)
	Defaults() config.DefaultsReport
	Validate(path, engineOverride string) (worker.ValidationPlan, error)
	Sequences() ([]pgruntime.SequenceProgress, error)
	SequenceID(path string, options pgruntime.RunFolderOptions) (string, error)
	Sequence(sequenceID string) (pgruntime.SequenceState, error)
	TerminalCandidates() ([]pgruntime.TerminalCandidate, error)
	CloseTerminals(workerIDs []string) ([]pgruntime.TerminalCandidate, error)
	List() ([]state.Worker, error)
	Status(id string) (pgruntime.StatusSummary, error)
	LatestEvent(id string) (pgruntime.EventSummary, error)
	Events(id string, filter state.EventFilter) (state.EventReadResult, error)
	GlobalEvents(filter state.EventFilter) (state.EventReadResult, error)
	EventPath(id string) (string, error)
	GlobalEventPath() string
	Logs(id string) (string, error)
	Prune() (int, error)
	Complete(id string) (state.Worker, error)
	Fail(id string) (state.Worker, error)
	Cancel(id string) (state.Worker, error)
	Reconcile(threshold time.Duration, markFailed bool) (pgruntime.ReconcileSummary, error)
	FinishWorker(recordPath string, exitCode int) error
	Heartbeat(id string) error
	RunCodexWorker(recordPath, command string) (int, error)
}

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

type StructuredError struct {
	Err  error
	Code int
}

func (e StructuredError) Error() string {
	if e.Err == nil {
		return "structured error"
	}
	return e.Err.Error()
}

func (e StructuredError) Unwrap() error {
	return e.Err
}

const (
	ExitSuccess           = 0
	ExitGeneralError      = 1
	ExitInvalidInput      = 2
	ExitWorkerNotFound    = 3
	ExitInvalidTransition = 4
	ExitExecutionFailed   = 5
	ExitTimeout           = 6
	ExitReadinessFailed   = 7
	ExitSetupFailed       = 8
)

func ExitCode(err error) (int, bool) {
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	var structuredErr StructuredError
	if errors.As(err, &structuredErr) {
		if structuredErr.Code != 0 {
			return structuredErr.Code, true
		}
		return ExitGeneralError, true
	}
	return 0, false
}

type listJSONOutput struct {
	Workers []state.Worker `json:"workers"`
}

type workerDefinitionsJSONOutput struct {
	Project workerdomain.Project            `json:"project"`
	Workers []workerdomain.WorkerDefinition `json:"workers"`
}

type workerDefinitionJSONOutput struct {
	Project workerdomain.Project          `json:"project"`
	Worker  workerdomain.WorkerDefinition `json:"worker"`
}

type namedWorkerStateJSONOutput struct {
	Project workerdomain.Project     `json:"project"`
	State   workerdomain.WorkerState `json:"state"`
}

type namedWorkerStartJSONOutput struct {
	State  workerdomain.WorkerState  `json:"state"`
	Launch workerlaunch.LaunchResult `json:"launch"`
}

type tasksJSONOutput struct {
	Project workerdomain.Project `json:"project"`
	Tasks   []workerdomain.Task  `json:"tasks"`
}

type taskJSONOutput struct {
	Project workerdomain.Project `json:"project"`
	Task    workerdomain.Task    `json:"task"`
}

type statusJSONOutput struct {
	Worker      state.Worker `json:"worker"`
	EventPath   string       `json:"event_path"`
	LatestEvent *state.Event `json:"latest_event"`
}

type eventsJSONOutput struct {
	Events []state.Event `json:"events"`
}

type logsJSONOutput struct {
	WorkerID string `json:"worker_id"`
	LogPath  string `json:"log_path"`
	Exists   bool   `json:"exists"`
	Content  string `json:"content"`
}

type runJSONOutput struct {
	Workers        []runWorkerJSON  `json:"workers"`
	Failures       []runFailureJSON `json:"failures"`
	ValidatedPaths []string         `json:"validated_paths,omitempty"`
	DryRun         bool             `json:"dry_run,omitempty"`
}

type enginesJSONOutput struct {
	Engines []engine.Descriptor `json:"engines"`
}

type engineJSONOutput struct {
	Engine engine.Descriptor `json:"engine"`
}

type defaultsJSONOutput struct {
	Defaults config.DefaultsReport `json:"defaults"`
}

type runWorkerJSON struct {
	WorkerID        string `json:"worker_id"`
	Status          string `json:"status"`
	TaskPath        string `json:"task_path"`
	Engine          string `json:"engine"`
	TerminalAdapter string `json:"terminal_adapter"`
}

type runFailureJSON struct {
	Message string `json:"message"`
}

type sequencesJSONOutput struct {
	Sequences []pgruntime.SequenceProgress `json:"sequences"`
}

type sequenceJSONOutput struct {
	Sequence pgruntime.SequenceState `json:"sequence"`
}

type terminalsJSONOutput struct {
	Terminals []pgruntime.TerminalCandidate `json:"terminals"`
	Closed    []pgruntime.TerminalCandidate `json:"closed,omitempty"`
}

type lifecycleJSONOutput struct {
	WorkerID       string     `json:"worker_id"`
	PreviousStatus string     `json:"previous_status"`
	Status         string     `json:"status"`
	FinishedAt     *time.Time `json:"finished_at"`
}

type reconcileJSONOutput struct {
	OlderThan        string                    `json:"older_than"`
	MarkFailed       bool                      `json:"mark_failed"`
	StaleWorkers     []state.Worker            `json:"stale_workers"`
	UpdatedWorkers   []state.Worker            `json:"updated_workers"`
	StaleSequences   []pgruntime.SequenceState `json:"stale_sequences"`
	UpdatedSequences []pgruntime.SequenceState `json:"updated_sequences"`
}

type pruneJSONOutput struct {
	PrunedWorkers []prunedWorkerJSON `json:"pruned_workers"`
	Count         int                `json:"count"`
}

type prunedWorkerJSON struct {
	WorkerID       string `json:"worker_id"`
	PreviousStatus string `json:"previous_status"`
	TaskPath       string `json:"task_path"`
}

type errorJSONOutput struct {
	Error errorJSON `json:"error"`
}

type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewRootCommand(service Service, stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(service, stdout, stderr, os.Getwd, discovery.Discover)
}

// NewRootCommandWithRoleAdvisor supplies the runtime-neutral advisor used by
// roles enhance. Production integrations and tests can configure it without
// giving the advisor filesystem access.
func NewRootCommandWithRoleAdvisor(service Service, stdout, stderr io.Writer, advisor roleenhance.Advisor) *cobra.Command {
	return newRootCommandWithRoleAdvisor(service, stdout, stderr, os.Getwd, discovery.Discover, advisor)
}

type discoverFunc func(string) (discovery.Result, error)

func newRootCommand(service Service, stdout, stderr io.Writer, getwd func() (string, error), discover discoverFunc) *cobra.Command {
	return newRootCommandWithRoleAdvisor(service, stdout, stderr, getwd, discover, nil)
}

func newRootCommandWithRoleAdvisor(service Service, stdout, stderr io.Writer, getwd func() (string, error), discover discoverFunc, roleAdvisor roleenhance.Advisor) *cobra.Command {
	var compactJSON bool
	var plainOutput bool
	var themeName string
	root := &cobra.Command{
		Use:           "promptgrinder",
		Short:         "Run Markdown work orders through Codex locally.",
		Version:       buildinfo.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("promptgrinder {{.Version}}\n")
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().BoolVar(&compactJSON, "compact", false, "emit compact single-line JSON when used with --json")
	root.PersistentFlags().BoolVar(&plainOutput, "plain", false, "disable color and decorative box styling")
	root.PersistentFlags().StringVar(&themeName, "theme", "default", "interactive theme: default, minimal, or loud")

	discoverCmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover repository technologies and generate PromptGrinder roles.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			currentPath, err := getwd()
			if err != nil {
				wrapped := fmt.Errorf("determine current repository: %w", err)
				fmt.Fprintf(stderr, "Error: %v\n", wrapped)
				return StructuredError{Err: wrapped, Code: ExitInvalidInput}
			}
			rootPath, err := findRepositoryRoot(currentPath)
			if err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			result, err := discover(rootPath)
			if err != nil {
				wrapped := fmt.Errorf("discover repository: %w", err)
				fmt.Fprintf(stderr, "Error: %v\n", wrapped)
				return StructuredError{Err: wrapped, Code: ExitInvalidInput}
			}
			fmt.Fprintln(stdout, "Discovered:")
			if len(result.Roles) == 0 {
				fmt.Fprintln(stdout, "  (no supported roles)")
			} else {
				for _, role := range result.Roles {
					fmt.Fprintf(stdout, "  %s\n", role.Name)
				}
			}
			fmt.Fprintln(stdout, "Generated:")
			for _, path := range result.Files {
				fmt.Fprintf(stdout, "  %s\n", path)
			}
			fmt.Fprintln(stdout, "  .promptgrinder/context/")
			return nil
		},
	}
	root.AddCommand(discoverCmd)

	rolesCmd := &cobra.Command{Use: "roles", Short: "Inspect and enhance discovered roles."}
	var enhanceApplyAll, enhanceRejectAll, enhanceJSON bool
	var enhanceSelected []string
	enhanceCmd := &cobra.Command{
		Use: "enhance", Short: "Review AI-assisted role recommendations and optionally apply them.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			modes := 0
			if enhanceApplyAll {
				modes++
			}
			if enhanceRejectAll {
				modes++
			}
			if cmd.Flags().Changed("apply-selected") {
				modes++
			}
			if modes > 1 {
				err := fmt.Errorf("--apply-all, --apply-selected, and --reject-all are mutually exclusive")
				if enhanceJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			if cmd.Flags().Changed("apply-selected") && len(enhanceSelected) == 0 {
				err := fmt.Errorf("--apply-selected requires at least one recommendation ID")
				if enhanceJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			cwd, err := getwd()
			if err != nil {
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			rootPath, err := findRepositoryRoot(cwd)
			if err != nil {
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			svc := roleenhance.EnhanceService{Advisor: roleAdvisor}
			current, plan, err := svc.Review(cmd.Context(), rootPath)
			if err != nil {
				if enhanceJSON {
					return writeJSONCommandError(stdout, "roles_enhance_error", err, compactJSON)
				}
				fmt.Fprintf(stderr, "Error: enhance roles: %v\n", err)
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			selection := roleenhance.ApprovalSelection{Mode: roleenhance.ApprovalRejectAll}
			if enhanceApplyAll {
				selection.Mode = roleenhance.ApprovalApplyAll
			} else if cmd.Flags().Changed("apply-selected") {
				selection.Mode = roleenhance.ApprovalApplySelected
				selection.RecommendationIDs = enhanceSelected
			}
			var result roleenhance.MergeResult
			if selection.Mode != roleenhance.ApprovalRejectAll {
				result, err = (roleenhance.RoleMergeService{}).Apply(rootPath, current, plan, selection)
				if err != nil {
					if enhanceJSON {
						return writeJSONCommandError(stdout, "merge_conflict", err, compactJSON)
					}
					fmt.Fprintf(stderr, "Error: apply role enhancements: %v\n", err)
					return StructuredError{Err: err, Code: ExitInvalidInput}
				}
			} else {
				for _, item := range plan.Items {
					result.Rejected = append(result.Rejected, item.Recommendation.ID)
				}
			}
			if enhanceJSON {
				return writeJSON(stdout, struct {
					Review roleenhance.ReviewPlan   `json:"review"`
					Result roleenhance.MergeResult  `json:"result"`
					Mode   roleenhance.ApprovalMode `json:"mode"`
				}{plan, result, selection.Mode}, compactJSON)
			}
			if err := (roleenhance.ReviewRenderer{}).Render(stdout, plan); err != nil {
				return err
			}
			if selection.Mode == roleenhance.ApprovalRejectAll {
				fmt.Fprintln(stdout, "\nReview only: no files written.")
			} else {
				fmt.Fprintf(stdout, "\nApplied %d recommendation(s) to %d file(s).\n", len(result.Applied), len(result.Files))
			}
			return nil
		},
	}
	enhanceCmd.Flags().BoolVar(&enhanceApplyAll, "apply-all", false, "apply every recommendation (removals require individual selection)")
	enhanceCmd.Flags().StringSliceVar(&enhanceSelected, "apply-selected", nil, "apply only the listed recommendation IDs")
	enhanceCmd.Flags().BoolVar(&enhanceRejectAll, "reject-all", false, "reject all recommendations and write nothing")
	enhanceCmd.Flags().BoolVar(&enhanceJSON, "json", false, "print machine-readable JSON")
	rolesCmd.AddCommand(enhanceCmd)
	root.AddCommand(rolesCmd)

	var runJSON bool
	var runEngine string
	var runSandbox string
	var runSharedContext bool
	var runDryRun bool
	var runCommitEach bool
	var runRequireCleanGit bool
	var runRollbackOnFailure bool
	var runAllowConcurrentWorktree bool
	run := &cobra.Command{
		Use:   "run <task.md|tasks/|pattern>...",
		Short: "Run Markdown tasks, folders, or wildcard patterns.",
		Long: `Run one or more Markdown task files, folders, or wildcard patterns.

Wildcard patterns may be expanded by the shell or quoted for PromptGrinder to
expand. Add --shared-context to run selected files sequentially in one Codex
context.

Examples:
  promptgrinder run task.md
  promptgrinder run tasks/
  promptgrinder run ./prompts/02*.md
  promptgrinder run --dry-run './prompts/02*.md'
  promptgrinder run --shared-context './prompts/02*.md'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			theme, themeErr := ui.NormalizeTheme(themeName)
			if themeErr != nil {
				if runJSON {
					return writeJSONCommandError(stdout, "validation_error", themeErr, compactJSON)
				}
				return themeErr
			}
			interactive := shouldRenderInteractive(stdout, runJSON, compactJSON)
			uiOptions := ui.Options{Theme: theme, Plain: plainOutput || ui.PlainFromEnv()}
			if interactive {
				ui.Banner(stdout, uiOptions)
			}
			restorePlainEnv := setPlainEnvForRun(plainOutput)
			defer restorePlainEnv()
			defaults := service.Defaults().Config
			engineOverride := runEngine
			if engineOverride == "" {
				engineOverride = defaults.RunEngine
			}
			var progress *ui.SharedProgressRenderer
			runOptions := pgruntime.RunOptions{
				EngineOverride:          engineOverride,
				SandboxOverride:         runSandbox,
				SharedContext:           runSharedContext,
				DryRun:                  runDryRun,
				CommitEach:              runSharedContext && runCommitEach,
				RequireCleanGit:         runSharedContext && runRequireCleanGit,
				RollbackOnFailure:       runSharedContext && runRollbackOnFailure,
				AllowConcurrentWorktree: runAllowConcurrentWorktree,
			}
			if runSharedContext && !runDryRun && !runJSON {
				progress = ui.NewSharedProgressRenderer(stdout, interactive, uiOptions)
				runOptions.OnProgress = progress.Update
			}
			summary, err := service.RunPathsWithOptions(args, runOptions)
			if progress != nil {
				progress.Close()
			}
			if runJSON {
				output := runJSONOutput{Workers: runWorkersJSON(summary.Workers), Failures: runFailuresJSON(summary.Failed), ValidatedPaths: summary.ValidatedPaths, DryRun: runDryRun}
				if err != nil && len(summary.Workers) == 0 && len(summary.Failed) == 0 {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				if writeErr := writeJSON(stdout, output, compactJSON); writeErr != nil {
					return writeErr
				}
				if err != nil {
					return StructuredError{Err: err, Code: errorExitCode(classifyError(err))}
				}
				return nil
			}
			if runDryRun && err == nil {
				mode := "independent contexts"
				if runSharedContext {
					mode = "one shared context"
				}
				fmt.Fprintf(stdout, "Dry run valid: %d prompt(s), %s. No workers created.\n", len(summary.ValidatedPaths), mode)
				for i, path := range summary.ValidatedPaths {
					fmt.Fprintf(stdout, "%d. %s\n", i+1, path)
				}
			}
			for _, worker := range summary.Workers {
				if worker.Status == "running" {
					if interactive {
						ui.WorkerLaunchHeader(stdout, ui.LaunchHeader{
							WorkerID:       worker.ID,
							TaskPath:       worker.TaskPath,
							RepositoryPath: worker.RepositoryPath,
							StartedAt:      worker.StartTime,
							LogPath:        worker.LogPath,
						}, uiOptions)
					} else {
						fmt.Fprintf(stdout, "Started worker %s\n", worker.ID)
						fmt.Fprintf(stdout, "Task: %s\n", worker.TaskPath)
						fmt.Fprintf(stdout, "Log: %s\n", worker.LogPath)
					}
				}
			}
			if len(summary.Workers) > 1 || len(summary.Failed) > 0 {
				fmt.Fprintf(stdout, "Summary: %d worker(s) created, %d failed.\n", len(summary.Workers), len(summary.Failed))
			}
			if err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
				return StructuredError{Err: err, Code: errorExitCode(classifyError(err))}
			}
			return nil
		},
	}
	run.Flags().BoolVar(&runJSON, "json", false, "print machine-readable JSON")
	run.Flags().StringVar(&runEngine, "engine", "", "override the task engine for this run")
	run.Flags().StringVar(&runSandbox, "sandbox", "", "override the Codex sandbox for this run: read-only, workspace-write, or danger-full-access")
	run.Flags().BoolVar(&runSharedContext, "shared-context", false, "run selected Markdown files sequentially in one Codex context")
	run.Flags().BoolVar(&runDryRun, "dry-run", false, "resolve and validate all selected prompts without creating workers")
	run.Flags().BoolVar(&runCommitEach, "commit-each", true, "commit changes after each successful shared-context prompt")
	run.Flags().BoolVar(&runRequireCleanGit, "require-clean-git", true, "require a clean working tree before a shared-context run")
	run.Flags().BoolVar(&runRollbackOnFailure, "rollback-on-failure", true, "restore the previous prompt checkpoint after a failed shared-context prompt")
	run.Flags().BoolVar(&runAllowConcurrentWorktree, "allow-concurrent-worktree", false, "allow another PromptGrinder batch to use the same git worktree")
	root.AddCommand(run)

	var doctorJSON bool
	var doctorRepo string
	var doctorTerminal string
	var doctorActive bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check first-run prerequisites without changing files.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if doctorActive && !doctorJSON {
				fmt.Fprintln(stdout, "Active check requested: a visible, short-lived terminal probe may open. It never starts Codex or an AI session.")
			}
			report := firstuse.Doctor(cmd.Context(), firstuse.DoctorOptions{
				Repo: doctorRepo, Terminal: doctorTerminal, Active: doctorActive,
				HomeDir: service.Defaults().HomeDir,
			})
			if doctorJSON {
				if err := writeJSON(stdout, report, compactJSON); err != nil {
					return err
				}
			} else {
				printDoctor(stdout, report)
			}
			if !report.OK {
				return StructuredError{Err: fmt.Errorf("doctor found %d failed required check(s)", report.Failures), Code: ExitReadinessFailed}
			}
			return nil
		},
	}
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "print deterministic machine-readable JSON")
	doctorCmd.Flags().StringVar(&doctorRepo, "repo", "", "check a repository and its effective configuration")
	doctorCmd.Flags().StringVar(&doctorTerminal, "terminal", "", "check terminal, iterm, or headless (defaults to effective config)")
	doctorCmd.Flags().BoolVar(&doctorActive, "active", false, "opt in to a visible no-AI terminal launch probe")
	root.AddCommand(doctorCmd)

	var setupJSON bool
	var setupDryRun bool
	var setupYes bool
	var setupNonInteractive bool
	var setupReplace bool
	var setupBackup bool
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Create minimal PromptGrinder-owned state and configuration.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if setupReplace && !setupBackup {
				err := fmt.Errorf("--replace requires --backup so an edited config is preserved")
				if setupJSON {
					return writeJSONCommandError(stdout, "setup_error", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			report, err := firstuse.Setup(firstuse.SetupOptions{
				HomeDir: service.Defaults().HomeDir, DryRun: setupDryRun,
				NonInteractive: setupNonInteractive, Yes: setupYes,
				Replace: setupReplace, Backup: setupBackup,
				Input: cmd.InOrStdin(), Output: func() io.Writer {
					if setupJSON {
						return io.Discard
					}
					return stdout
				}(),
			})
			if setupJSON {
				if writeErr := writeJSON(stdout, report, compactJSON); writeErr != nil {
					return writeErr
				}
			}
			if err != nil {
				return StructuredError{Err: err, Code: ExitSetupFailed}
			}
			if !setupJSON {
				if report.Changed {
					fmt.Fprintln(stdout, "Setup complete. Next: promptgrinder doctor")
				} else {
					fmt.Fprintln(stdout, "Setup is already complete; no files changed.")
				}
			}
			return nil
		},
	}
	setupCmd.Flags().BoolVar(&setupJSON, "json", false, "print machine-readable JSON")
	setupCmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "show proposed writes without changing files")
	setupCmd.Flags().BoolVarP(&setupYes, "yes", "y", false, "confirm proposed PromptGrinder-owned writes")
	setupCmd.Flags().BoolVar(&setupNonInteractive, "non-interactive", false, "never prompt; requires --yes when writes are proposed")
	setupCmd.Flags().BoolVar(&setupReplace, "replace", false, "replace an existing user config (requires --backup)")
	setupCmd.Flags().BoolVar(&setupBackup, "backup", false, "back up an existing config before replacement")
	root.AddCommand(setupCmd)

	var defaultsJSON bool
	defaultsCmd := &cobra.Command{
		Use:   "defaults",
		Short: "Show current PromptGrinder defaults and override locations.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := service.Defaults()
			if defaultsJSON {
				return writeJSON(stdout, defaultsJSONOutput{Defaults: report}, compactJSON)
			}
			printDefaults(stdout, report)
			return nil
		},
	}
	defaultsCmd.Flags().BoolVar(&defaultsJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(defaultsCmd)

	var enginesJSON bool
	engines := &cobra.Command{
		Use:   "engines",
		Short: "List supported engine adapters.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			items := service.Engines()
			if enginesJSON {
				return writeJSON(stdout, enginesJSONOutput{Engines: items}, compactJSON)
			}
			if len(items) == 0 {
				fmt.Fprintln(stdout, "No engines registered.")
				return nil
			}
			fmt.Fprintf(stdout, "%-16s %-36s CAPABILITIES\n", "ENGINE", "DESCRIPTION")
			for _, item := range items {
				fmt.Fprintf(stdout, "%-16s %-36s %s\n", item.Name, item.Description, strings.Join(activeCapabilityNames(item.Capabilities), ", "))
			}
			return nil
		},
	}
	engines.Flags().BoolVar(&enginesJSON, "json", false, "print machine-readable JSON")
	var engineDescribeJSON bool
	engineDescribe := &cobra.Command{
		Use:   "describe <engine>",
		Short: "Describe one engine adapter.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := service.DescribeEngine(args[0])
			if err != nil {
				if engineDescribeJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			if engineDescribeJSON {
				return writeJSON(stdout, engineJSONOutput{Engine: item}, compactJSON)
			}
			fmt.Fprintf(stdout, "Engine: %s\n", item.Name)
			fmt.Fprintf(stdout, "Description: %s\n", item.Description)
			fmt.Fprintf(stdout, "Capabilities:\n")
			printCapabilities(stdout, item.Capabilities)
			fmt.Fprintln(stdout, "Notes: capability flags are adapter metadata; option handling is delegated to the engine adapter.")
			return nil
		},
	}
	engineDescribe.Flags().BoolVar(&engineDescribeJSON, "json", false, "print machine-readable JSON")
	engines.AddCommand(engineDescribe)
	root.AddCommand(engines)

	var validateJSON bool
	var validateEngine string
	var validateRender bool
	validateCmd := &cobra.Command{
		Use:   "validate <task.md>",
		Short: "Validate a Markdown task without launching a worker.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if validateRender && validateJSON {
				return StructuredError{Err: fmt.Errorf("--render cannot be combined with --json"), Code: ExitInvalidInput}
			}
			plan, err := service.Validate(args[0], validateEngine)
			if validateJSON {
				if writeErr := writeJSON(stdout, plan, compactJSON); writeErr != nil {
					return writeErr
				}
				if err != nil {
					return StructuredError{Err: err, Code: ExitInvalidInput}
				}
				return nil
			}
			if err != nil {
				printValidationPlan(stdout, plan)
				return StructuredError{Err: err, Code: ExitInvalidInput}
			}
			if validateRender {
				_, err := io.WriteString(stdout, plan.RenderedPrompt)
				return err
			}
			printValidationPlan(stdout, plan)
			return nil
		},
	}
	validateCmd.Flags().BoolVar(&validateJSON, "json", false, "print machine-readable JSON")
	validateCmd.Flags().BoolVar(&validateRender, "render", false, "print the exact prompt bytes the engine would receive")
	validateCmd.Flags().StringVar(&validateEngine, "engine", "", "override the task engine for validation")
	root.AddCommand(validateCmd)

	var runFolderResume bool
	var runFolderFresh bool
	var runFolderCheckpoint bool
	var runFolderCommitEach bool
	var runFolderRequireCleanGit bool
	var runFolderRepo string
	var runFolderTemplate string
	var runFolderEngine string
	var runFolderIncludeSpecification bool
	var runFolderRestart bool
	var runFolderNoResume bool
	var runFolderDetach bool
	var runFolderAllowConcurrentWorktree bool
	var runFolderSupervisorID string
	var runFolderSupervisorLog string
	buildRunFolderOptions := func(cmd *cobra.Command) pgruntime.RunFolderOptions {
		defaults := service.Defaults().Config
		if cmd.Flags().Lookup("detach") != nil && !cmd.Flags().Changed("detach") {
			runFolderDetach = defaults.RunFolderDetach
		}
		if !cmd.Flags().Changed("resume") {
			runFolderResume = defaults.RunFolderResume
		}
		if !cmd.Flags().Changed("fresh") {
			runFolderFresh = defaults.RunFolderFresh
		}
		if !cmd.Flags().Changed("restart") {
			runFolderRestart = defaults.RunFolderRestart
		}
		if !cmd.Flags().Changed("no-resume") {
			runFolderNoResume = defaults.RunFolderNoResume
		}
		if !cmd.Flags().Changed("checkpoint") {
			runFolderCheckpoint = defaults.RunFolderCheckpoint
		}
		if !cmd.Flags().Changed("commit-each") {
			runFolderCommitEach = defaults.RunFolderCommitEach
		}
		if !cmd.Flags().Changed("require-clean-git") {
			runFolderRequireCleanGit = defaults.RunFolderRequireCleanGit
		}
		if !cmd.Flags().Changed("repo") {
			runFolderRepo = defaults.RunFolderRepo
		}
		if !cmd.Flags().Changed("template") {
			runFolderTemplate = defaults.RunFolderTemplate
		}
		if !cmd.Flags().Changed("engine") {
			runFolderEngine = defaults.RunFolderEngine
		}
		if !cmd.Flags().Changed("include-specification") {
			runFolderIncludeSpecification = defaults.RunFolderIncludeSpecification
		}
		return pgruntime.RunFolderOptions{
			Resume:                  runFolderResume,
			Fresh:                   runFolderFresh,
			Restart:                 runFolderRestart,
			NoResume:                runFolderNoResume,
			Checkpoint:              runFolderCheckpoint,
			CommitEach:              runFolderCommitEach,
			RequireCleanGit:         runFolderRequireCleanGit,
			AllowConcurrentWorktree: runFolderAllowConcurrentWorktree,
			RepoPath:                runFolderRepo,
			Template:                runFolderTemplate,
			EngineOverride:          runFolderEngine,
			IncludeSpecification:    runFolderIncludeSpecification,
			SupervisorID:            runFolderSupervisorID,
			SupervisorLogPath:       runFolderSupervisorLog,
		}
	}
	bindRunFolderFlags := func(cmd *cobra.Command, includeDetach bool) {
		cmd.Flags().BoolVar(&runFolderResume, "resume", false, "resume an unfinished folder run")
		cmd.Flags().BoolVar(&runFolderFresh, "fresh", false, "start a new folder run and ignore previous state")
		cmd.Flags().BoolVar(&runFolderRestart, "restart", false, "restart this exact sequence from the beginning")
		cmd.Flags().BoolVar(&runFolderNoResume, "no-resume", false, "do not use existing sequence resume state")
		cmd.Flags().BoolVar(&runFolderCheckpoint, "checkpoint", false, "record git metadata after successful prompts")
		cmd.Flags().BoolVar(&runFolderCommitEach, "commit-each", false, "commit changes after each successful runnable prompt")
		cmd.Flags().BoolVar(&runFolderRequireCleanGit, "require-clean-git", false, "require a clean git working tree before each runnable prompt")
		cmd.Flags().StringVar(&runFolderRepo, "repo", ".", "repository path where workers run and git operations apply")
		cmd.Flags().StringVar(&runFolderTemplate, "template", "codex", "execution template")
		cmd.Flags().StringVar(&runFolderEngine, "engine", "", "override every prompt engine for this run")
		cmd.Flags().BoolVar(&runFolderIncludeSpecification, "include-specification", false, "execute specification prompts instead of using them only as context")
		cmd.Flags().BoolVar(&runFolderAllowConcurrentWorktree, "allow-concurrent-worktree", false, "allow another PromptGrinder batch to use the same git worktree")
		if includeDetach {
			cmd.Flags().BoolVar(&runFolderDetach, "detach", false, "run the folder sequence in the background")
		}
	}
	runFolder := &cobra.Command{
		Use:   "run-folder <folder>",
		Short: "Run numbered Markdown prompts in a folder sequentially.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("run-folder requires exactly one folder")
			}
			if runFolderResume && runFolderFresh {
				return fmt.Errorf("--resume and --fresh are mutually exclusive")
			}
			if runFolderRestart && runFolderNoResume {
				return fmt.Errorf("--restart and --no-resume are mutually exclusive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			options := buildRunFolderOptions(cmd)
			if runFolderDetach {
				return startDetachedRunFolder(stdout, service, service.Defaults().HomeDir, args[0], options)
			}
			theme, themeErr := ui.NormalizeTheme(themeName)
			if themeErr != nil {
				return StructuredError{Err: themeErr, Code: ExitInvalidInput}
			}
			plain := plainOutput || ui.PlainFromEnv()
			renderer := ui.NewRunFolderRenderer(stdout, shouldRenderInteractive(stdout, false, false), ui.Options{Theme: theme, Plain: plain})
			defer renderer.Close()
			restorePlainEnv := setPlainEnvForRun(plainOutput)
			defer restorePlainEnv()
			options.ExecutionPolicy = pgruntime.RunFolderExecutionForeground
			options.Progress = func(event pgruntime.RunFolderProgressEvent) {
				renderer.Update(event)
			}
			summary, err := service.RunPromptFolder(args[0], options)
			renderer.Finish(err == nil)
			printRunFolderAggregate(stdout, summary)
			if err != nil {
				return StructuredError{Err: err, Code: errorExitCode(classifyError(err))}
			}
			return nil
		},
	}
	bindRunFolderFlags(runFolder, true)
	root.AddCommand(runFolder)

	runFolderSupervisor := &cobra.Command{
		Use:    "__run-folder-supervisor <folder>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := service.RunPromptFolder(args[0], buildRunFolderOptions(cmd))
			return err
		},
	}
	bindRunFolderFlags(runFolderSupervisor, false)
	runFolderSupervisor.Flags().StringVar(&runFolderSupervisorID, "supervisor-id", "", "internal detached supervisor identity")
	runFolderSupervisor.Flags().StringVar(&runFolderSupervisorLog, "supervisor-log", "", "internal detached supervisor log path")
	_ = runFolderSupervisor.Flags().MarkHidden("supervisor-id")
	_ = runFolderSupervisor.Flags().MarkHidden("supervisor-log")
	root.AddCommand(runFolderSupervisor)

	var sequencesJSON bool
	var sequencesFolder string
	sequences := &cobra.Command{
		Use:   "sequences",
		Short: "List prompt sequence progress.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := service.Sequences()
			if err != nil {
				if sequencesJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if sequencesFolder != "" {
				wanted, absErr := filepath.Abs(sequencesFolder)
				if absErr != nil {
					return absErr
				}
				filtered := items[:0]
				for _, item := range items {
					folder, folderErr := filepath.Abs(item.Folder)
					if folderErr == nil && filepath.Clean(folder) == filepath.Clean(wanted) {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
			if sequencesJSON {
				return writeJSON(stdout, sequencesJSONOutput{Sequences: items}, compactJSON)
			}
			if len(items) == 0 {
				fmt.Fprintln(stdout, "No sequences found.")
				return nil
			}
			fmt.Fprintf(stdout, "%-18s %-12s %-8s %-8s %-8s %-20s %-20s %-20s %-20s CURRENT\n", "SEQUENCE", "STATUS", "DONE", "FAILED", "PENDING", "CREATED (UTC)", "STARTED (UTC)", "UPDATED (UTC)", "FINISHED (UTC)")
			for _, item := range items {
				fmt.Fprintf(stdout, "%-18s %-12s %-8s %-8d %-8d %-20s %-20s %-20s %-20s %s\n", item.SequenceID, item.Status, fmt.Sprintf("%d/%d", item.Succeeded, item.Total), item.Failed, item.Pending, formatTime(item.CreatedAt), formatTime(item.StartedAt), formatTime(item.UpdatedAt), formatTime(item.FinishedAt), valueOrDash(item.Current))
			}
			return nil
		},
	}
	sequences.Flags().BoolVar(&sequencesJSON, "json", false, "print machine-readable JSON")
	sequences.Flags().StringVar(&sequencesFolder, "folder", "", "show only sequences for this folder (relative or absolute path)")
	root.AddCommand(sequences)

	var sequenceJSON bool
	sequenceCmd := &cobra.Command{
		Use:   "sequence <sequence-id|current>",
		Short: "Show prompt-level progress for one sequence.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sequence, err := service.Sequence(args[0])
			if err != nil {
				if sequenceJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if sequenceJSON {
				return writeJSON(stdout, sequenceJSONOutput{Sequence: sequence}, compactJSON)
			}
			printSequence(stdout, sequence)
			return nil
		},
	}
	sequenceCmd.Flags().BoolVar(&sequenceJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(sequenceCmd)

	var terminalsJSON bool
	terminals := &cobra.Command{
		Use:   "terminals",
		Short: "List PromptGrinder terminal tabs.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			candidates, err := service.TerminalCandidates()
			if err != nil {
				if terminalsJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if terminalsJSON {
				return writeJSON(stdout, terminalsJSONOutput{Terminals: candidates}, compactJSON)
			}
			return printTerminalCandidates(stdout, candidates)
		},
	}
	terminals.Flags().BoolVar(&terminalsJSON, "json", false, "print machine-readable JSON")
	var terminalKillAll bool
	var terminalKillJSON bool
	terminalKill := &cobra.Command{
		Use:   "kill [number|worker-id|list]",
		Short: "Close PromptGrinder terminal tabs.",
		Args: func(cmd *cobra.Command, args []string) error {
			if terminalKillAll {
				if len(args) != 0 {
					return fmt.Errorf("terminals kill --all does not accept an argument")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("terminals kill requires a number, worker id, or --all")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			candidates, err := service.TerminalCandidates()
			if err != nil {
				if terminalKillJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			workerIDs := []string{}
			if !terminalKillAll {
				selected, err := terminalSelections(args[0], candidates)
				if err != nil {
					if terminalKillJSON {
						return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
					}
					return err
				}
				workerIDs = append(workerIDs, selected...)
			}
			closed, err := service.CloseTerminals(workerIDs)
			if err != nil {
				if terminalKillJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if terminalKillJSON {
				return writeJSON(stdout, terminalsJSONOutput{Terminals: candidates, Closed: closed}, compactJSON)
			}
			if terminalKillAll {
				fmt.Fprintf(stdout, "Requested close for all PromptGrinder terminal tab(s); %d were matched from worker state.\n", len(closed))
				return nil
			}
			fmt.Fprintf(stdout, "Closed %d PromptGrinder terminal tab(s).\n", len(closed))
			return nil
		},
	}
	terminalKill.Flags().BoolVar(&terminalKillAll, "all", false, "close all listed PromptGrinder terminal tabs")
	terminalKill.Flags().BoolVar(&terminalKillJSON, "json", false, "print machine-readable JSON")
	terminals.AddCommand(terminalKill)
	root.AddCommand(terminals)

	var listEvents bool
	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List local workers.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workers, err := service.List()
			if err != nil {
				if listJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if listJSON {
				return writeJSON(stdout, listJSONOutput{Workers: workers}, compactJSON)
			}
			if len(workers) == 0 {
				fmt.Fprintln(stdout, "No workers found.")
				return nil
			}
			return printWorkers(stdout, service, workers, listEvents)
		},
	}
	list.Flags().BoolVar(&listEvents, "events", false, "show latest worker event")
	list.Flags().BoolVar(&listJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(list)

	var workersEvents bool
	var workersJSON bool
	workersCmd := &cobra.Command{
		Use:   "workers",
		Short: "List local workers.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workers, err := service.List()
			if err != nil {
				if workersJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if workersJSON {
				return writeJSON(stdout, listJSONOutput{Workers: workers}, compactJSON)
			}
			if len(workers) == 0 {
				fmt.Fprintln(stdout, "No workers found.")
				return nil
			}
			return printWorkers(stdout, service, workers, workersEvents)
		},
	}
	workersCmd.Flags().BoolVar(&workersEvents, "events", false, "show latest worker event")
	workersCmd.Flags().BoolVar(&workersJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(workersCmd)

	var workerListJSON bool
	var workerShowJSON bool
	var workerStartDryRun bool
	var workerStartJSON bool
	var workerStartRuntime string
	var workerStatusJSON bool
	var workerResetJSON bool
	var workerControlJSON bool
	var workerPauseTimeout time.Duration
	workerCmd := &cobra.Command{
		Use:   "worker",
		Short: "Inspect project-owned named worker definitions.",
	}
	workerListCmd := &cobra.Command{
		Use:   "list",
		Short: "List project-owned named worker definitions.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				if workerListJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return err
			}
			definitions := registry.List()
			if workerListJSON {
				return writeJSON(stdout, workerDefinitionsJSONOutput{Project: registry.Project, Workers: definitions}, compactJSON)
			}
			fmt.Fprintf(stdout, "PROJECT\t%s\t%s\n", registry.Project.ID, registry.Project.Name)
			fmt.Fprintln(stdout, "WORKER ID\tDISPLAY NAME\tRUNTIME\tWORKTREE")
			for _, definition := range definitions {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", definition.ID, definition.DisplayName, definition.Runtime.Name, definition.Policy.DefaultWorktree)
			}
			return nil
		},
	}
	workerListCmd.Flags().BoolVar(&workerListJSON, "json", false, "print machine-readable JSON")
	workerShowCmd := &cobra.Command{
		Use:   "show <worker-id>",
		Short: "Show one project-owned named worker definition.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				if workerShowJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return err
			}
			definition, err := registry.Get(args[0])
			if err != nil {
				if workerShowJSON {
					return writeJSONCommandError(stdout, "worker_not_found", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitWorkerNotFound}
			}
			if workerShowJSON {
				return writeJSON(stdout, workerDefinitionJSONOutput{Project: registry.Project, Worker: definition}, compactJSON)
			}
			fmt.Fprintf(stdout, "Project: %s (%s)\n", registry.Project.Name, registry.Project.ID)
			fmt.Fprintf(stdout, "Worker: %s\n", definition.ID)
			fmt.Fprintf(stdout, "Display name: %s\n", definition.DisplayName)
			fmt.Fprintf(stdout, "Role: %s\n", definition.Role)
			fmt.Fprintf(stdout, "Runtime: %s\n", definition.Runtime.Name)
			fmt.Fprintf(stdout, "Branch prefix: %s\n", definition.Policy.BranchPrefix)
			fmt.Fprintf(stdout, "Default worktree: %s\n", definition.Policy.DefaultWorktree)
			fmt.Fprintf(stdout, "Allowed paths: %s\n", displayPaths(definition.Policy.AllowedPaths))
			fmt.Fprintf(stdout, "Forbidden paths: %s\n", displayPaths(definition.Policy.ForbiddenPaths))
			return nil
		},
	}
	workerShowCmd.Flags().BoolVar(&workerShowJSON, "json", false, "print machine-readable JSON")
	workerStartCmd := &cobra.Command{
		Use:   "start <worker-id>",
		Short: "Start a project-owned named worker.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				if workerStartJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return err
			}
			definition, err := registry.Get(args[0])
			if err != nil {
				if workerStartJSON {
					return writeJSONCommandError(stdout, "worker_not_found", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitWorkerNotFound}
			}
			cfg := service.Defaults().Config
			if workerStartDryRun {
				request, err := workerlaunch.Build(workerlaunch.BuildOptions{
					Project: registry.Project, Worker: definition, Repository: registry.Root,
					RuntimeOverride: workerStartRuntime, RuntimeDefault: cfg.WorkerRuntime,
					RuntimeOptions: cfg.RuntimeOptions,
				})
				if err != nil {
					if workerStartJSON {
						return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
					}
					return StructuredError{Err: err, Code: ExitInvalidInput}
				}
				if workerStartJSON {
					return writeJSON(stdout, workerlaunch.Redacted(request), compactJSON)
				}
				fmt.Fprint(stdout, workerlaunch.PlanDocument(request))
				return nil
			}
			runtimeService := pgruntime.NewService(cfg)
			starter := workerstart.Service{
				Home: cfg.HomeDir, Config: cfg,
				Launchers: namedWorkerLaunchers(runtimeService),
			}
			result, err := starter.Start(cmd.Context(), registry.Root, definition.ID, workerStartRuntime)
			if err != nil {
				if workerStartJSON {
					return writeJSONCommandError(stdout, "launch_error", err, compactJSON)
				}
				return StructuredError{Err: err, Code: ExitExecutionFailed}
			}
			if workerStartJSON {
				return writeJSON(stdout, namedWorkerStartJSONOutput{State: result.State, Launch: result.Launch}, compactJSON)
			}
			fmt.Fprintf(stdout, "Worker %s started run %s with runtime %s (lifecycle %s).\n", result.State.WorkerID, result.Launch.RunID, result.Launch.RuntimeName, result.State.Lifecycle)
			return nil
		},
	}
	workerStartCmd.Flags().BoolVar(&workerStartDryRun, "dry-run", false, "resolve and validate the launch without starting a process or creating state")
	workerStartCmd.Flags().BoolVar(&workerStartJSON, "json", false, "print the redacted launch request as machine-readable JSON")
	workerStartCmd.Flags().StringVar(&workerStartRuntime, "runtime", "", "override the worker runtime for this launch")
	workerStatusCmd := &cobra.Command{
		Use:   "status <worker-id>",
		Short: "Show PromptGrinder-owned lifecycle state for a named worker.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerStatusJSON, compactJSON)
			}
			store := workerstate.New(service.Defaults().Config.HomeDir)
			workerState, err := store.Ensure(definition)
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerStatusJSON, compactJSON)
			}
			if workerStatusJSON {
				return writeJSON(stdout, namedWorkerStateJSONOutput{Project: registry.Project, State: workerState}, compactJSON)
			}
			printNamedWorkerState(stdout, registry.Project, workerState)
			return nil
		},
	}
	workerStatusCmd.Flags().BoolVar(&workerStatusJSON, "json", false, "print machine-readable JSON")
	workerResetCmd := &cobra.Command{
		Use:   "reset <worker-id>",
		Short: "Reset a safely terminal named worker to idle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerResetJSON, compactJSON)
			}
			store := workerstate.New(service.Defaults().Config.HomeDir)
			workerState, err := store.Ensure(definition)
			if err == nil {
				workerState, err = store.Reset(workerState)
			}
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerResetJSON, compactJSON)
			}
			if workerResetJSON {
				return writeJSON(stdout, namedWorkerStateJSONOutput{Project: registry.Project, State: workerState}, compactJSON)
			}
			fmt.Fprintf(stdout, "Worker %s is idle (revision %d).\n", workerState.WorkerID, workerState.Revision)
			return nil
		},
	}
	workerResetCmd.Flags().BoolVar(&workerResetJSON, "json", false, "print machine-readable JSON")
	workerPauseCmd := &cobra.Command{
		Use: "pause <worker-id>", Short: "Gracefully stop and pause a running named worker.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			cfg := service.Defaults().Config
			state, err := workerstate.New(cfg.HomeDir).Ensure(definition)
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			result, err := (workercontrol.Service{Home: cfg.HomeDir, Timeout: workerPauseTimeout}).Pause(cmd.Context(), state)
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			if workerControlJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Worker %s paused (forced: %t, idempotent: %t).\n", registry.Project.ID+"/"+definition.ID, result.Forced, result.Idempotent)
			return nil
		},
	}
	workerPauseCmd.Flags().DurationVar(&workerPauseTimeout, "timeout", 10*time.Second, "graceful stop timeout before forced termination")
	workerPauseCmd.Flags().BoolVar(&workerControlJSON, "json", false, "print machine-readable JSON")
	workerResumeCmd := &cobra.Command{
		Use: "resume <worker-id>", Short: "Resume a paused named worker.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			cfg := service.Defaults().Config
			runtimeService := pgruntime.NewService(cfg)
			launchers := namedWorkerLaunchers(runtimeService)
			starter := workerstart.Service{Home: cfg.HomeDir, Config: cfg, Launchers: launchers}
			state, err := workerstate.New(cfg.HomeDir).Ensure(definition)
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			result, err := (workercontrol.Service{Home: cfg.HomeDir, Launchers: launchers, StartNew: func(ctx context.Context, id string) error {
				_, startErr := starter.Start(ctx, ".", id, "")
				return startErr
			}}).Resume(cmd.Context(), state)
			if err != nil {
				return namedWorkerCommandError(stdout, err, workerControlJSON, compactJSON)
			}
			if workerControlJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Worker %s resumed (session: %t, idempotent: %t).\n", definition.ID, result.Resumed, result.Idempotent)
			return nil
		},
	}
	workerResumeCmd.Flags().BoolVar(&workerControlJSON, "json", false, "print machine-readable JSON")
	workerCmd.AddCommand(workerListCmd, workerShowCmd, workerStartCmd, workerStatusCmd, workerResetCmd, workerPauseCmd, workerResumeCmd)
	root.AddCommand(workerCmd)

	var taskAssignJSON bool
	var taskListJSON bool
	var taskListWorker string
	var taskShowJSON bool
	var taskQueueJSON bool
	var taskControlJSON bool
	taskCmd := &cobra.Command{Use: "task", Short: "Assign and inspect project-owned tasks."}
	taskAssignCmd := &cobra.Command{
		Use:   "assign <worker-id> <task.md>",
		Short: "Assign an immutable task snapshot to a named worker.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskAssignJSON, compactJSON)
			}
			store := taskstore.New(service.Defaults().Config.HomeDir)
			task, err := store.Assign(registry.Root, definition, args[1])
			if err != nil {
				return taskCommandError(stdout, err, taskAssignJSON, compactJSON)
			}
			if taskAssignJSON {
				return writeJSON(stdout, taskJSONOutput{Project: registry.Project, Task: task}, compactJSON)
			}
			fmt.Fprintf(stdout, "Assigned task %s to worker %s.\n", task.ID, task.WorkerID)
			fmt.Fprintf(stdout, "Source: %s\n", task.SourceReference)
			fmt.Fprintf(stdout, "Status: %s\n", task.Status)
			return nil
		},
	}
	taskAssignCmd.Flags().BoolVar(&taskAssignJSON, "json", false, "print machine-readable JSON")
	taskEnqueueCmd := &cobra.Command{
		Use:   "enqueue <worker-id> <task.md>",
		Short: "Append an immutable task snapshot to a worker FIFO.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			task, err := taskstore.New(service.Defaults().Config.HomeDir).Enqueue(registry.Root, definition, args[1])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			if taskQueueJSON {
				return writeJSON(stdout, taskJSONOutput{Project: registry.Project, Task: task}, compactJSON)
			}
			fmt.Fprintf(stdout, "Enqueued task %s for worker %s.\n", task.ID, task.WorkerID)
			return nil
		},
	}
	taskEnqueueCmd.Flags().BoolVar(&taskQueueJSON, "json", false, "print machine-readable JSON")
	taskListCmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks owned by the current project.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, taskListJSON, compactJSON)
			}
			if taskListWorker != "" {
				if _, err := registry.Get(taskListWorker); err != nil {
					return taskCommandError(stdout, err, taskListJSON, compactJSON)
				}
			}
			tasks, err := taskstore.New(service.Defaults().Config.HomeDir).List(registry.Project.ID, taskListWorker)
			if err != nil {
				return taskCommandError(stdout, err, taskListJSON, compactJSON)
			}
			if taskListJSON {
				return writeJSON(stdout, tasksJSONOutput{Project: registry.Project, Tasks: tasks}, compactJSON)
			}
			if len(tasks) == 0 {
				fmt.Fprintln(stdout, "No tasks found.")
				return nil
			}
			fmt.Fprintln(stdout, "TASK ID\tWORKER ID\tSTATUS\tATTEMPTS\tSOURCE")
			for _, task := range tasks {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\t%s\n", task.ID, task.WorkerID, task.Status, task.AttemptCount, task.SourceReference)
			}
			return nil
		},
	}
	taskListCmd.Flags().StringVar(&taskListWorker, "worker", "", "show tasks assigned to this worker")
	taskListCmd.Flags().BoolVar(&taskListJSON, "json", false, "print machine-readable JSON")
	taskShowCmd := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show one immutable task snapshot.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, taskShowJSON, compactJSON)
			}
			task, err := taskstore.New(service.Defaults().Config.HomeDir).Load(registry.Project.ID, args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskShowJSON, compactJSON)
			}
			if _, err := registry.Get(task.WorkerID); err != nil {
				return taskCommandError(stdout, fmt.Errorf("persisted task worker mismatch: %w", err), taskShowJSON, compactJSON)
			}
			if taskShowJSON {
				return writeJSON(stdout, taskJSONOutput{Project: registry.Project, Task: task}, compactJSON)
			}
			printTask(stdout, task)
			return nil
		},
	}
	taskShowCmd.Flags().BoolVar(&taskShowJSON, "json", false, "print machine-readable JSON")
	queueCmd := &cobra.Command{Use: "queue", Short: "Inspect and edit pending worker task queues."}
	queueListCmd := &cobra.Command{
		Use: "list <worker-id>", Args: cobra.ExactArgs(1), Short: "List a worker queue in FIFO order.",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			queue, err := taskqueue.New(service.Defaults().Config.HomeDir).List(registry.Project.ID, definition.ID)
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			if taskQueueJSON {
				return writeJSON(stdout, queue, compactJSON)
			}
			if len(queue.Entries) == 0 {
				fmt.Fprintln(stdout, "Queue is empty.")
				return nil
			}
			fmt.Fprintln(stdout, "POSITION\tTASK ID\tENQUEUED AT\tLEASE")
			for i, entry := range queue.Entries {
				lease := "-"
				if entry.Lease != nil {
					lease = entry.Lease.ExpiresAt.Format(time.RFC3339)
				}
				fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\n", i+1, entry.TaskID, entry.EnqueuedAt.Format(time.RFC3339), lease)
			}
			return nil
		},
	}
	queueReorderCmd := &cobra.Command{
		Use: "reorder <worker-id> <task-id> <position>", Args: cobra.ExactArgs(3), Short: "Move a pending task to a one-based queue position.",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			position, err := strconv.Atoi(args[2])
			if err != nil || position < 1 {
				return taskCommandError(stdout, fmt.Errorf("position must be a positive integer"), taskQueueJSON, compactJSON)
			}
			queue, err := taskqueue.New(service.Defaults().Config.HomeDir).Reorder(registry.Project.ID, definition.ID, args[1], position-1)
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			if taskQueueJSON {
				return writeJSON(stdout, queue, compactJSON)
			}
			fmt.Fprintf(stdout, "Moved task %s to position %d.\n", args[1], position)
			return nil
		},
	}
	queueRemoveCmd := &cobra.Command{
		Use: "remove <worker-id> <task-id>", Args: cobra.ExactArgs(2), Short: "Remove a pending task from a worker queue.",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, definition, err := loadNamedWorker(args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			queue, err := taskqueue.New(service.Defaults().Config.HomeDir).Remove(registry.Project.ID, definition.ID, args[1])
			if err != nil {
				return taskCommandError(stdout, err, taskQueueJSON, compactJSON)
			}
			if taskQueueJSON {
				return writeJSON(stdout, queue, compactJSON)
			}
			fmt.Fprintf(stdout, "Removed pending task %s from worker %s.\n", args[1], definition.ID)
			return nil
		},
	}
	queueListCmd.Flags().BoolVar(&taskQueueJSON, "json", false, "print machine-readable JSON")
	queueReorderCmd.Flags().BoolVar(&taskQueueJSON, "json", false, "print machine-readable JSON")
	queueRemoveCmd.Flags().BoolVar(&taskQueueJSON, "json", false, "print machine-readable JSON")
	queueCmd.AddCommand(queueListCmd, queueReorderCmd, queueRemoveCmd)
	taskRetryCmd := &cobra.Command{
		Use: "retry <task-id>", Short: "Start a new attempt without replacing prior evidence.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			cfg := service.Defaults().Config
			task, err := taskstore.New(cfg.HomeDir).Load(registry.Project.ID, args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			definition, err := registry.Get(task.WorkerID)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			state, err := workerstate.New(cfg.HomeDir).Ensure(definition)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			runtimeService := pgruntime.NewService(cfg)
			launchers := namedWorkerLaunchers(runtimeService)
			starter := workerstart.Service{Home: cfg.HomeDir, Config: cfg, Launchers: launchers}
			result, err := (workercontrol.Service{Home: cfg.HomeDir, StartNew: func(ctx context.Context, id string) error {
				_, startErr := starter.Start(ctx, registry.Root, id, "")
				return startErr
			}}).Retry(cmd.Context(), state, task)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			if taskControlJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Retry requested for task %s; prior attempts retained.\n", task.ID)
			return nil
		},
	}
	taskCancelCmd := &cobra.Command{
		Use: "cancel <task-id>", Short: "Cancel active or queued work without deleting evidence.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			cfg := service.Defaults().Config
			task, err := taskstore.New(cfg.HomeDir).Load(registry.Project.ID, args[0])
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			definition, err := registry.Get(task.WorkerID)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			state, err := workerstate.New(cfg.HomeDir).Ensure(definition)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			result, err := (workercontrol.Service{Home: cfg.HomeDir}).Cancel(cmd.Context(), state, task)
			if err != nil {
				return taskCommandError(stdout, err, taskControlJSON, compactJSON)
			}
			if taskControlJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Task %s canceled (idempotent: %t); evidence retained.\n", task.ID, result.Idempotent)
			return nil
		},
	}
	taskRetryCmd.Flags().BoolVar(&taskControlJSON, "json", false, "print machine-readable JSON")
	taskCancelCmd.Flags().BoolVar(&taskControlJSON, "json", false, "print machine-readable JSON")
	taskCmd.AddCommand(taskAssignCmd, taskEnqueueCmd, taskListCmd, taskShowCmd, taskRetryCmd, taskCancelCmd, queueCmd)
	root.AddCommand(taskCmd)

	var reviewJSON bool
	var reviewSummary string
	var reviewChangedPaths []string
	var reviewCommits []string
	var reviewValidations []string
	var reviewReason string
	reviewCmd := &cobra.Command{
		Use:   "review",
		Short: "Create and decide local task review handoffs.",
		Long: `Review handoffs are local and never push, open a pull request, merge,
tag, publish, or release. Rejection preserves all evidence and requeues the
task at the tail of its original implementing worker's FIFO.`,
	}
	reviewSubmitCmd := &cobra.Command{
		Use: "submit <task-id> <implementer-worker-id>", Short: "Submit complete implementation evidence for local review.", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			validations, err := parseReviewValidations(reviewValidations)
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			result, err := (review.Service{Home: service.Defaults().Config.HomeDir}).Submit(registry, args[0], args[1], review.Evidence{
				Summary: reviewSummary, ChangedPaths: reviewChangedPaths, Commits: reviewCommits, Validations: validations,
			})
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			if reviewJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Task %s is awaiting local review (handoff %s).\n", result.Task.ID, result.Handoff.ID)
			return nil
		},
	}
	reviewSubmitCmd.Flags().StringVar(&reviewSummary, "summary", "", "completion summary (required)")
	reviewSubmitCmd.Flags().StringSliceVar(&reviewChangedPaths, "changed-path", nil, "repository-relative changed path (repeatable)")
	reviewSubmitCmd.Flags().StringSliceVar(&reviewCommits, "commit", nil, "commit identifier recorded as evidence (repeatable)")
	reviewSubmitCmd.Flags().StringSliceVar(&reviewValidations, "validation", nil, "validation evidence as command=result (repeatable, required)")
	reviewSubmitCmd.Flags().BoolVar(&reviewJSON, "json", false, "print machine-readable JSON")
	reviewShowCmd := &cobra.Command{
		Use: "show <task-id>", Aliases: []string{"inspect"}, Short: "Inspect the latest local review handoff.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			result, err := (review.Service{Home: service.Defaults().Config.HomeDir}).Inspect(registry, args[0])
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			if reviewJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			printReviewHandoff(stdout, result.Handoff)
			return nil
		},
	}
	reviewShowCmd.Flags().BoolVar(&reviewJSON, "json", false, "print machine-readable JSON")
	reviewAcceptCmd := &cobra.Command{
		Use: "accept <task-id> <reviewer-worker-id>", Short: "Accept a local review without publishing externally.", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			result, err := (review.Service{Home: service.Defaults().Config.HomeDir}).Accept(registry, args[0], args[1], reviewReason)
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			if reviewJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Accepted task %s locally as reviewer %s. No external publication was performed.\n", result.Task.ID, args[1])
			return nil
		},
	}
	reviewAcceptCmd.Flags().StringVar(&reviewReason, "reason", "", "review decision reason (required)")
	reviewAcceptCmd.Flags().BoolVar(&reviewJSON, "json", false, "print machine-readable JSON")
	reviewRejectCmd := &cobra.Command{
		Use: "reject <task-id> <reviewer-worker-id>", Short: "Reject and requeue work while preserving evidence.", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			result, err := (review.Service{Home: service.Defaults().Config.HomeDir}).Reject(registry, args[0], args[1], reviewReason)
			if err != nil {
				return taskCommandError(stdout, err, reviewJSON, compactJSON)
			}
			if reviewJSON {
				return writeJSON(stdout, result, compactJSON)
			}
			fmt.Fprintf(stdout, "Rejected task %s; evidence retained and task requeued to worker %s FIFO tail.\n", result.Task.ID, result.Task.WorkerID)
			return nil
		},
	}
	reviewRejectCmd.Flags().StringVar(&reviewReason, "reason", "", "review decision reason (required)")
	reviewRejectCmd.Flags().BoolVar(&reviewJSON, "json", false, "print machine-readable JSON")
	reviewCmd.AddCommand(reviewSubmitCmd, reviewShowCmd, reviewAcceptCmd, reviewRejectCmd)
	root.AddCommand(reviewCmd)

	var schedulerOnce bool
	var schedulerInterval time.Duration
	schedulerCmd := &cobra.Command{Use: "scheduler", Short: "Dispatch eligible idle named workers."}
	schedulerRunCmd := &cobra.Command{
		Use: "run", Args: cobra.NoArgs, Short: "Run the local FIFO scheduler.",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := workerregistry.Load(".")
			if err != nil {
				return err
			}
			cfg := service.Defaults().Config
			runtimeService := pgruntime.NewService(cfg)
			starter := workerstart.Service{Home: cfg.HomeDir, Config: cfg, Launchers: namedWorkerLaunchers(runtimeService)}
			schedulerService := pgscheduler.Service{
				Home: cfg.HomeDir, Config: cfg,
				Dispatch: func(ctx context.Context, dispatch pgscheduler.Dispatch) error {
					_, err := starter.Start(ctx, registry.Root, dispatch.Worker.ID, "")
					return err
				},
			}
			for {
				dispatched, err := schedulerService.RunOnce(cmd.Context(), registry)
				if err != nil {
					return err
				}
				if dispatched != nil {
					fmt.Fprintf(stdout, "Dispatched task %s to worker %s.\n", dispatched.Task.ID, dispatched.Worker.ID)
				}
				if schedulerOnce {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(schedulerInterval):
				}
			}
		},
	}
	schedulerRunCmd.Flags().BoolVar(&schedulerOnce, "once", false, "perform one scheduling decision and exit")
	schedulerRunCmd.Flags().DurationVar(&schedulerInterval, "interval", time.Second, "poll interval")
	schedulerCmd.AddCommand(schedulerRunCmd)
	root.AddCommand(schedulerCmd)

	var eventTail int
	var eventType string
	var eventSeverity string
	var eventFollow bool
	var eventGlobal bool
	var eventJSON bool
	eventsCommand := &cobra.Command{
		Use:   "events [worker-id]",
		Short: "Show structured worker events.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateEventsArgs(args, eventGlobal); err != nil {
				if eventJSON {
					return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
				}
				return err
			}
			filter := state.EventFilter{Tail: eventTail, Type: eventType, Severity: eventSeverity}
			if err := validateEventFilter(filter); err != nil {
				if eventJSON {
					return writeJSONCommandError(stdout, "invalid_filter", err, compactJSON)
				}
				return err
			}
			var result state.EventReadResult
			var err error
			var path string
			if eventGlobal {
				path = service.GlobalEventPath()
			} else {
				path, err = service.EventPath(args[0])
			}
			if err == nil && eventFollow {
				var offset int64
				result, offset, err = readEventsForFollow(path, filter)
				if err == nil {
					if eventJSON {
						for _, warning := range result.Warnings {
							fmt.Fprintf(stderr, "warning: %s\n", warning)
						}
						if err := printEventsNDJSON(stdout, result.Events); err != nil {
							return err
						}
						return followEvents(cmd.Context(), path, offset, filter, stdout, stderr, true)
					}
					for _, warning := range result.Warnings {
						fmt.Fprintf(stderr, "warning: %s\n", warning)
					}
					if len(result.Events) > 0 || eventFollow {
						printEventHeader(stdout)
					}
					printEvents(stdout, result.Events)
					return followEvents(cmd.Context(), path, offset, filter, stdout, stderr, false)
				}
			}
			if err == nil {
				if eventGlobal {
					result, err = service.GlobalEvents(filter)
				} else {
					result, err = service.Events(args[0], filter)
				}
			}
			if err != nil {
				if eventJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if eventJSON {
				for _, warning := range result.Warnings {
					fmt.Fprintf(stderr, "warning: %s\n", warning)
				}
				return writeJSON(stdout, eventsJSONOutput{Events: result.Events}, compactJSON)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(stderr, "warning: %s\n", warning)
			}
			if len(result.Events) == 0 && !eventFollow {
				fmt.Fprintln(stdout, "No events found.")
				return nil
			}
			if len(result.Events) > 0 || eventFollow {
				printEventHeader(stdout)
			}
			printEvents(stdout, result.Events)
			return nil
		},
	}
	eventsCommand.Flags().IntVar(&eventTail, "tail", 50, "number of events to show")
	eventsCommand.Flags().StringVar(&eventType, "type", "", "filter by event type")
	eventsCommand.Flags().StringVar(&eventSeverity, "severity", "", "filter by severity")
	eventsCommand.Flags().BoolVar(&eventFollow, "follow", false, "follow appended events until interrupted")
	eventsCommand.Flags().BoolVar(&eventGlobal, "global", false, "show global event log")
	eventsCommand.Flags().BoolVar(&eventJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(eventsCommand)

	var statusJSON bool
	status := &cobra.Command{
		Use:   "status <worker-id>",
		Short: "Show detailed worker status.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return nil
			}
			err := fmt.Errorf("status requires exactly one worker id")
			if statusJSON {
				return writeJSONCommandError(stdout, "validation_error", err, compactJSON)
			}
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			summary, err := service.Status(args[0])
			if err != nil {
				if statusJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if statusJSON {
				latest := (*state.Event)(nil)
				if summary.LatestEvent.Found {
					event := summary.LatestEvent.Event
					latest = &event
				}
				return writeJSON(stdout, statusJSONOutput{Worker: summary.Worker, EventPath: summary.EventPath, LatestEvent: latest}, compactJSON)
			}
			printStatus(stdout, summary)
			return nil
		},
	}
	status.Flags().BoolVar(&statusJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(status)

	var logsJSON bool
	logsCommand := &cobra.Command{
		Use:   "logs <worker-id>",
		Short: "Print a worker log.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if logsJSON {
				summary, err := service.Status(args[0])
				if err != nil {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				data, err := os.ReadFile(summary.Worker.LogPath)
				if os.IsNotExist(err) {
					return writeJSON(stdout, logsJSONOutput{WorkerID: summary.Worker.ID, LogPath: summary.Worker.LogPath, Exists: false, Content: ""}, compactJSON)
				}
				if err != nil {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return writeJSON(stdout, logsJSONOutput{WorkerID: summary.Worker.ID, LogPath: summary.Worker.LogPath, Exists: true, Content: string(data)}, compactJSON)
			}
			logs, err := service.Logs(args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(stdout, logs)
			return nil
		},
	}
	logsCommand.Flags().BoolVar(&logsJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(logsCommand)

	var pruneJSON bool
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Delete completed local worker state.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var prunable []prunedWorkerJSON
			if pruneJSON {
				workers, err := service.List()
				if err != nil {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				for _, worker := range workers {
					if state.IsPrunableStatus(worker.Status) {
						prunable = append(prunable, prunedWorkerJSON{WorkerID: worker.ID, PreviousStatus: worker.Status, TaskPath: worker.TaskPath})
					}
				}
			}
			count, err := service.Prune()
			if err != nil {
				if pruneJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if pruneJSON {
				return writeJSON(stdout, pruneJSONOutput{PrunedWorkers: prunable, Count: count}, compactJSON)
			}
			suffix := "s"
			if count == 1 {
				suffix = ""
			}
			fmt.Fprintf(stdout, "Pruned %d worker%s.\n", count, suffix)
			return nil
		},
	}
	prune.Flags().BoolVar(&pruneJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(prune)

	root.AddCommand(lifecycleCommand("complete", "Mark a running or waiting worker as succeeded.", service.Complete, service, stdout, func() bool { return compactJSON }))
	root.AddCommand(lifecycleCommand("fail", "Mark a running or waiting worker as failed.", service.Fail, service, stdout, func() bool { return compactJSON }))
	root.AddCommand(lifecycleCommand("cancel", "Mark a pending, starting, running, or waiting worker as cancelled.", service.Cancel, service, stdout, func() bool { return compactJSON }))

	var olderThan string
	var markFailed bool
	var reconcileJSON bool
	reconcile := &cobra.Command{
		Use:   "reconcile",
		Short: "Find stale active workers and prompt sequences.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			threshold, err := ParseDuration(olderThan)
			if err != nil {
				if reconcileJSON {
					return writeJSONCommandError(stdout, "invalid_duration", err, compactJSON)
				}
				return err
			}
			summary, err := service.Reconcile(threshold, markFailed)
			if err != nil {
				if reconcileJSON {
					return writeJSONCommandError(stdout, classifyError(err), err, compactJSON)
				}
				return err
			}
			if reconcileJSON {
				output := reconcileJSONOutput{OlderThan: threshold.String(), MarkFailed: markFailed}
				if markFailed {
					output.UpdatedWorkers = summary.Workers
					output.UpdatedSequences = summary.Sequences
				} else {
					output.StaleWorkers = summary.Workers
					output.StaleSequences = summary.Sequences
				}
				return writeJSON(stdout, output, compactJSON)
			}
			action := "stale"
			if summary.MarkFailed {
				action = "marked failed"
			}
			fmt.Fprintf(stdout, "Reconcile: %d worker(s) %s older than %s.\n", len(summary.Workers), action, summary.Threshold)
			for _, worker := range summary.Workers {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", worker.ID, formatStatus(worker.Status), formatElapsed(worker), worker.TaskPath)
			}
			sequenceAction := "stale"
			if summary.MarkFailed {
				sequenceAction = "marked interrupted"
			}
			fmt.Fprintf(stdout, "Reconcile: %d sequence(s) %s older than %s.\n", len(summary.Sequences), sequenceAction, summary.Threshold)
			for _, sequence := range summary.Sequences {
				fmt.Fprintf(stdout, "%s\t%s\t%s\n", sequence.SequenceID, sequence.Status, sequence.Folder)
			}
			return nil
		},
	}
	reconcile.Flags().StringVar(&olderThan, "older-than", "24h", "stale threshold duration, for example 30m, 2h, or 24h")
	reconcile.Flags().BoolVar(&markFailed, "mark-failed", false, "mark stale workers as failed")
	reconcile.Flags().BoolVar(&reconcileJSON, "json", false, "print machine-readable JSON")
	root.AddCommand(reconcile)

	finish := &cobra.Command{
		Use:    "__worker-finish <record-path> <exit-code>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			exitCode, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			return service.FinishWorker(args[0], exitCode)
		},
	}
	root.AddCommand(finish)

	heartbeat := &cobra.Command{
		Use:    "__worker-heartbeat <worker-id>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return service.Heartbeat(args[0])
		},
	}
	root.AddCommand(heartbeat)

	terminalClose := &cobra.Command{
		Use:    "__terminal-close <worker-id>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := service.CloseTerminals([]string{args[0]})
			return err
		},
	}
	root.AddCommand(terminalClose)

	engineCodex := &cobra.Command{
		Use:    "__engine-codex <record-path> [command]",
		Hidden: true,
		Args:   cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := ""
			if len(args) == 2 {
				command = args[1]
			}
			exitCode, err := service.RunCodexWorker(args[0], command)
			if err != nil {
				return err
			}
			if exitCode != 0 {
				return ExitError{Code: exitCode}
			}
			return nil
		},
	}
	root.AddCommand(engineCodex)

	return root
}

func findRepositoryRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve current repository: %w", err)
	}
	for current := abs; ; current = filepath.Dir(current) {
		if _, statErr := os.Stat(filepath.Join(current, ".git")); statErr == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("current directory is not inside a repository: %s", abs)
		}
	}
}

func loadNamedWorker(id string) (*workerregistry.Registry, workerdomain.WorkerDefinition, error) {
	registry, err := workerregistry.Load(".")
	if err != nil {
		return nil, workerdomain.WorkerDefinition{}, err
	}
	definition, err := registry.Get(id)
	return registry, definition, err
}

func namedWorkerCommandError(stdout io.Writer, err error, jsonOutput, compact bool) error {
	code := classifyError(err)
	if errors.Is(err, workerregistry.ErrWorkerNotFound) {
		code = "worker_not_found"
	} else if errors.Is(err, workerstate.ErrUnsafeReset) || strings.Contains(err.Error(), "lifecycle transition") {
		code = "invalid_transition"
	}
	if jsonOutput {
		return writeJSONCommandError(stdout, code, err, compact)
	}
	return StructuredError{Err: err, Code: errorExitCode(code)}
}

func taskCommandError(stdout io.Writer, err error, jsonOutput, compact bool) error {
	code := classifyError(err)
	switch {
	case errors.Is(err, workerregistry.ErrWorkerNotFound):
		code = "worker_not_found"
	case errors.Is(err, taskstore.ErrNotFound):
		code = "task_not_found"
	case errors.Is(err, taskstore.ErrActiveAssignment), errors.Is(err, taskstore.ErrTaskExists):
		code = "invalid_transition"
	}
	if jsonOutput {
		return writeJSONCommandError(stdout, code, err, compact)
	}
	return StructuredError{Err: err, Code: errorExitCode(code)}
}

func printTask(stdout io.Writer, task workerdomain.Task) {
	fmt.Fprintf(stdout, "Task: %s\n", task.ID)
	fmt.Fprintf(stdout, "Project: %s\n", task.ProjectID)
	fmt.Fprintf(stdout, "Worker: %s\n", task.WorkerID)
	fmt.Fprintf(stdout, "Status: %s\n", task.Status)
	fmt.Fprintf(stdout, "Attempts: %d\n", task.AttemptCount)
	fmt.Fprintf(stdout, "Source: %s\n", task.SourceReference)
	fmt.Fprintf(stdout, "Created: %s\n", formatTime(&task.CreatedAt))
	fmt.Fprintf(stdout, "Updated: %s\n", formatTime(&task.UpdatedAt))
	fmt.Fprintln(stdout, "Instructions:")
	fmt.Fprint(stdout, task.Instructions)
	if !strings.HasSuffix(task.Instructions, "\n") {
		fmt.Fprintln(stdout)
	}
}

func parseReviewValidations(values []string) ([]workerdomain.Validation, error) {
	result := make([]workerdomain.Validation, 0, len(values))
	for _, value := range values {
		command, outcome, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(command) == "" || strings.TrimSpace(outcome) == "" {
			return nil, fmt.Errorf("validation %q must use command=result", value)
		}
		result = append(result, workerdomain.Validation{Command: strings.TrimSpace(command), Result: strings.TrimSpace(outcome)})
	}
	return result, nil
}

func printReviewHandoff(stdout io.Writer, handoff workerdomain.ReviewHandoff) {
	fmt.Fprintf(stdout, "Review: %s\n", handoff.ID)
	fmt.Fprintf(stdout, "Task: %s\n", handoff.TaskID)
	fmt.Fprintf(stdout, "Implementer: %s\n", handoff.ImplementerID)
	fmt.Fprintf(stdout, "Status: %s\n", handoff.Status)
	fmt.Fprintf(stdout, "Summary: %s\n", handoff.Summary)
	fmt.Fprintf(stdout, "Changed paths: %s\n", strings.Join(handoff.ChangedPaths, ", "))
	fmt.Fprintf(stdout, "Commits: %s\n", strings.Join(handoff.Commits, ", "))
	fmt.Fprintf(stdout, "Attempts: %d\n", len(handoff.TaskAttempts))
	fmt.Fprintf(stdout, "Runtime evidence: %d\n", len(handoff.RuntimeEvidence))
	fmt.Fprintf(stdout, "Validations: %d\n", len(handoff.Validations))
	fmt.Fprintf(stdout, "Reviewer: %s\n", valueOrDash(handoff.ReviewerID))
	fmt.Fprintf(stdout, "Decision reason: %s\n", valueOrDash(handoff.DecisionReason))
}

func printNamedWorkerState(stdout io.Writer, project workerdomain.Project, state workerdomain.WorkerState) {
	fmt.Fprintf(stdout, "Project: %s (%s)\n", project.Name, project.ID)
	fmt.Fprintf(stdout, "Worker: %s\n", state.WorkerID)
	fmt.Fprintf(stdout, "Lifecycle: %s\n", state.Lifecycle)
	fmt.Fprintf(stdout, "Revision: %d\n", state.Revision)
	fmt.Fprintf(stdout, "Active task: %s\n", valueOrDash(state.ActiveTaskID))
	fmt.Fprintf(stdout, "Active run: %s\n", valueOrDash(state.ActiveRunID))
	if state.RuntimeSession == nil {
		fmt.Fprintln(stdout, "Runtime session: -")
	} else {
		fmt.Fprintf(stdout, "Runtime session: %s/%s\n", state.RuntimeSession.Runtime, state.RuntimeSession.SessionID)
	}
	fmt.Fprintf(stdout, "Last completed task: %s\n", valueOrDash(state.LastCompletedTaskID))
	fmt.Fprintf(stdout, "Failure reason: %s\n", valueOrDash(state.FailureReason))
	fmt.Fprintf(stdout, "Block reason: %s\n", valueOrDash(state.BlockReason))
	fmt.Fprintf(stdout, "Created: %s\n", formatTime(&state.CreatedAt))
	fmt.Fprintf(stdout, "Updated: %s\n", formatTime(&state.UpdatedAt))
	fmt.Fprintf(stdout, "Lifecycle changed: %s\n", formatTime(&state.LifecycleChangedAt))
}

func printStatus(stdout io.Writer, summary pgruntime.StatusSummary) {
	worker := summary.Worker
	fmt.Fprintf(stdout, "Worker: %s\n", worker.ID)
	fmt.Fprintf(stdout, "Status: %s\n", formatStatus(worker.Status))
	fmt.Fprintf(stdout, "Task: %s\n", worker.TaskPath)
	fmt.Fprintf(stdout, "Repository: %s\n", worker.RepositoryPath)
	fmt.Fprintf(stdout, "Engine: %s\n", worker.Engine)
	fmt.Fprintf(stdout, "Terminal Adapter: %s\n", valueOrDash(worker.TerminalAdapter))
	fmt.Fprintf(stdout, "Created: %s\n", formatTime(worker.CreatedAt))
	fmt.Fprintf(stdout, "Started: %s\n", formatTime(worker.StartTime))
	fmt.Fprintf(stdout, "Finished: %s\n", formatTime(worker.FinishTime))
	fmt.Fprintf(stdout, "Last Seen: %s\n", formatTime(worker.LastSeenAt))
	fmt.Fprintf(stdout, "Elapsed: %s\n", formatElapsed(worker))
	fmt.Fprintf(stdout, "Timeout: %s\n", timeoutValue(worker.Metadata))
	fmt.Fprintf(stdout, "Labels: %s\n", labelsValue(worker.Metadata))
	fmt.Fprintf(stdout, "Working Directory: %s\n", workingDirectoryValue(worker))
	fmt.Fprintf(stdout, "Log: %s\n", worker.LogPath)
	fmt.Fprintf(stdout, "Events: %s\n", summary.EventPath)
	if summary.LatestEvent.Found {
		event := summary.LatestEvent.Event
		fmt.Fprintf(stdout, "Latest Event: %s %s %s\n", event.Timestamp.Format(time.RFC3339), event.Type, event.Message)
	} else {
		fmt.Fprintln(stdout, "Latest Event: -")
	}
	if worker.ExitCode != nil {
		fmt.Fprintf(stdout, "Exit Code: %d\n", *worker.ExitCode)
	} else {
		fmt.Fprintln(stdout, "Exit Code: -")
	}
	if worker.Status == state.StatusFailed && worker.ExitCode != nil {
		fmt.Fprintf(stdout, "Error: exit code %d\n", *worker.ExitCode)
	} else {
		fmt.Fprintln(stdout, "Error: -")
	}
}

func printCapabilities(stdout io.Writer, caps engine.Capabilities) {
	for _, item := range capabilityValues(caps) {
		fmt.Fprintf(stdout, "  %-30s %t\n", item.Name, item.Value)
	}
}

func activeCapabilityNames(caps engine.Capabilities) []string {
	names := []string{}
	for _, item := range capabilityValues(caps) {
		if item.Value {
			names = append(names, item.Name)
		}
	}
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}

func printValidationPlan(stdout io.Writer, plan worker.ValidationPlan) {
	fmt.Fprintf(stdout, "Valid: %t\n", plan.Valid)
	if plan.Engine != "" {
		fmt.Fprintf(stdout, "Engine: %s\n", plan.Engine)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(stdout, "Warning: %s\n", warning)
	}
	for _, validationError := range plan.Errors {
		fmt.Fprintf(stdout, "Error: %s\n", validationError)
	}
	if wd, ok := plan.ExecutionPlan["working_directory"].(string); ok && wd != "" {
		fmt.Fprintf(stdout, "Working directory: %s\n", wd)
	}
	if timeout, ok := plan.ExecutionPlan["timeout"].(string); ok && timeout != "" {
		fmt.Fprintf(stdout, "Timeout: %s\n", timeout)
	}
	if commandPreview, ok := plan.ExecutionPlan["command_preview"].(string); ok && commandPreview != "" {
		fmt.Fprintf(stdout, "Command: %s\n", commandPreview)
	}
	if starts, ok := plan.ExecutionPlan["worker_would_start"].(bool); ok {
		fmt.Fprintf(stdout, "Worker launch: %t\n", starts)
	}
}

func printDefaults(stdout io.Writer, report config.DefaultsReport) {
	cfg := report.Config
	fmt.Fprintf(stdout, "Home: %s\n", report.HomeDir)
	fmt.Fprintf(stdout, "Default template: %s\n", report.TemplatePath)
	if report.TemplateExists {
		fmt.Fprintln(stdout, "Default template status: present")
	} else {
		fmt.Fprintln(stdout, "Default template status: missing")
		fmt.Fprintln(stdout, "Create it on first interactive run, or add this example:")
		fmt.Fprintln(stdout)
		fmt.Fprint(stdout, config.DefaultTemplateExample())
	}
	fmt.Fprintf(stdout, "User config: %s (%s)\n", report.UserConfigPath, presentLabel(report.UserConfigExists))
	if report.RepositoryConfigPath != "" {
		fmt.Fprintf(stdout, "Repository config: %s (%s)\n", report.RepositoryConfigPath, presentLabel(report.RepositoryConfigExists))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Current defaults:")
	fmt.Fprintf(stdout, "  engine.default: %s\n", cfg.Engine)
	fmt.Fprintf(stdout, "  terminal.adapter: %s\n", cfg.TerminalAdapter)
	fmt.Fprintf(stdout, "  terminal.mode: %s\n", cfg.TerminalMode)
	fmt.Fprintf(stdout, "  terminal.close_on_finish: %t\n", cfg.TerminalCloseOnFinish)
	fmt.Fprintf(stdout, "  terminal.close_on_failure: %t\n", cfg.TerminalCloseOnFailure)
	fmt.Fprintf(stdout, "  worker.heartbeat_interval: %s\n", cfg.WorkerHeartbeatInterval)
	fmt.Fprintf(stdout, "  worker.timeout: %s\n", valueOrDash(durationString(cfg.WorkerTimeout)))
	fmt.Fprintf(stdout, "  engine.codex.sandbox: %s\n", cfg.CodexSandbox)
	fmt.Fprintf(stdout, "  engine.codex.approval: %s\n", cfg.CodexApproval)
	fmt.Fprintf(stdout, "  run.engine: %s\n", valueOrDash(cfg.RunEngine))
	fmt.Fprintf(stdout, "  run_folder.repo: %s\n", cfg.RunFolderRepo)
	fmt.Fprintf(stdout, "  run_folder.template: %s\n", cfg.RunFolderTemplate)
	fmt.Fprintf(stdout, "  run_folder.engine: %s\n", valueOrDash(cfg.RunFolderEngine))
	fmt.Fprintf(stdout, "  run_folder.resume: %t\n", cfg.RunFolderResume)
	fmt.Fprintf(stdout, "  run_folder.fresh: %t\n", cfg.RunFolderFresh)
	fmt.Fprintf(stdout, "  run_folder.restart: %t\n", cfg.RunFolderRestart)
	fmt.Fprintf(stdout, "  run_folder.no_resume: %t\n", cfg.RunFolderNoResume)
	fmt.Fprintf(stdout, "  run_folder.checkpoint: %t\n", cfg.RunFolderCheckpoint)
	fmt.Fprintf(stdout, "  run_folder.commit_each: %t\n", cfg.RunFolderCommitEach)
	fmt.Fprintf(stdout, "  run_folder.require_clean_git: %t\n", cfg.RunFolderRequireCleanGit)
	fmt.Fprintf(stdout, "  run_folder.include_specification: %t\n", cfg.RunFolderIncludeSpecification)
	fmt.Fprintf(stdout, "  run_folder.detach: %t\n", cfg.RunFolderDetach)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Override order: CLI flags > environment variables > repository .ai/config.yaml > ~/.promptgrinder/config.yaml > default template > built-in defaults.")
}

func printRunFolderProgress(stdout io.Writer, event pgruntime.RunFolderProgressEvent) {
	switch event.Type {
	case "run.started":
		fmt.Fprintf(stdout, "Sequence %s started: %d prompt(s)\n", event.SequenceID, event.Total)
	case "prompt.started":
		fmt.Fprintf(stdout, "[%d/%d] Working on %s (%s)\n", event.Completed+1, event.Total, event.PromptName, event.PromptType)
	case "prompt.skipped":
		fmt.Fprintf(stdout, "[%d/%d] Skipped %s\n", event.Completed, event.Total, event.PromptName)
	case "prompt.succeeded":
		fmt.Fprintf(stdout, "[%d/%d] Completed %s", event.Completed, event.Total, event.PromptName)
		if event.WorkerID != "" {
			fmt.Fprintf(stdout, " via %s", event.WorkerID)
		}
		fmt.Fprintln(stdout)
	case "prompt.failed":
		fmt.Fprintf(stdout, "[%d/%d] Failed %s", event.Completed+1, event.Total, event.PromptName)
		if event.WorkerID != "" {
			fmt.Fprintf(stdout, " via %s", event.WorkerID)
		}
		if event.LogPath != "" {
			fmt.Fprintf(stdout, "\nLog: %s", event.LogPath)
		}
		fmt.Fprintln(stdout)
	case "run.completed":
		fmt.Fprintf(stdout, "Sequence %s completed: %d/%d prompt(s)\n", event.SequenceID, event.Completed, event.Total)
	}
}

func printSequence(stdout io.Writer, sequence pgruntime.SequenceState) {
	progress := sequence.Progress()
	fmt.Fprintf(stdout, "Sequence: %s\n", sequence.SequenceID)
	fmt.Fprintf(stdout, "Status: %s\n", sequence.Status)
	fmt.Fprintf(stdout, "Folder: %s\n", sequence.Folder)
	fmt.Fprintf(stdout, "Created (UTC): %s\n", formatTime(sequence.CreatedAt))
	fmt.Fprintf(stdout, "Started (UTC): %s\n", formatTime(sequence.StartedAt))
	fmt.Fprintf(stdout, "Updated (UTC): %s\n", formatTime(sequence.UpdatedAt))
	fmt.Fprintf(stdout, "Finished (UTC): %s\n", formatTime(sequence.FinishedAt))
	if sequence.EventPath != "" {
		fmt.Fprintf(stdout, "Events: %s\n", sequence.EventPath)
	}
	if sequence.Supervisor != nil {
		fmt.Fprintf(stdout, "Supervisor: %s (%s, PID %d)\n", sequence.Supervisor.ID, sequence.Supervisor.Status, sequence.Supervisor.PID)
		if sequence.Supervisor.LogPath != "" {
			fmt.Fprintf(stdout, "Supervisor log: %s\n", sequence.Supervisor.LogPath)
		}
	}
	fmt.Fprintf(stdout, "Progress: %d/%d done, %d failed, %d interrupted, %d pending\n", progress.Succeeded, progress.Total, progress.Failed, progress.Interrupted, progress.Pending)
	if progress.Current != "" {
		label := "Running"
		if sequence.Status == "failed" {
			label = "Failed"
		} else if sequence.Status == "interrupted" {
			label = "Interrupted"
		}
		fmt.Fprintf(stdout, "%s: %s\n", label, progress.Current)
	}
	if progress.Next != "" {
		fmt.Fprintf(stdout, "Next: %s\n", progress.Next)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "%-4s %-44s %-12s %-28s LOG\n", "#", "PROMPT", "STATUS", "WORKER")
	for i, item := range sequence.Items {
		label := sequencePromptLabel(item)
		fmt.Fprintf(stdout, "%-4d %-44s %-12s %-28s %s\n", i+1, truncate(label, 44), item.Status, valueOrDash(item.WorkerID), valueOrDash(item.LogPath))
		if item.CompletionStatus != "" || item.NextPromptSafe != nil || item.CompletionReason != "" {
			fmt.Fprintf(stdout, "     Completion: STATUS=%s NEXT_PROMPT_SAFE=%s", valueOrDash(item.CompletionStatus), sequenceBool(item.NextPromptSafe))
			if item.ExitCode != nil {
				fmt.Fprintf(stdout, " engine_exit_code=%d", *item.ExitCode)
			}
			if item.CompletionReason != "" {
				fmt.Fprintf(stdout, " reason=%s", item.CompletionReason)
			}
			fmt.Fprintln(stdout)
		}
	}
}

func sequenceBool(value *bool) string {
	if value == nil {
		return "-"
	}
	if *value {
		return "yes"
	}
	return "no"
}

func printWorkers(stdout io.Writer, service Service, workers []state.Worker, includeEvents bool) error {
	if includeEvents {
		fmt.Fprintf(stdout, "%-28s %-16s %-10s %-8s %-28s TASK\n", "ID", "STATUS", "ELAPSED", "ENGINE", "LATEST EVENT")
	} else {
		fmt.Fprintf(stdout, "%-28s %-16s %-10s %-8s TASK\n", "ID", "STATUS", "ELAPSED", "ENGINE")
	}
	for _, worker := range workers {
		if includeEvents {
			latest := "-"
			summary, err := service.LatestEvent(worker.ID)
			if err != nil {
				return err
			}
			if summary.Found {
				latest = summary.Event.Type + ": " + summary.Event.Message
			}
			fmt.Fprintf(stdout, "%-28s %-16s %-10s %-8s %-28s %s\n", worker.ID, formatStatus(worker.Status), formatElapsed(worker), worker.Engine, truncate(latest, 28), worker.TaskPath)
		} else {
			fmt.Fprintf(stdout, "%-28s %-16s %-10s %-8s %s\n", worker.ID, formatStatus(worker.Status), formatElapsed(worker), worker.Engine, worker.TaskPath)
		}
	}
	return nil
}

func displayPaths(paths []string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	return strings.Join(paths, ", ")
}

func sequencePromptLabel(item runfolder.SequenceItem) string {
	if heading := firstMarkdownHeading(item.PromptPath); heading != "" {
		return heading
	}
	return item.PromptName
}

func firstMarkdownHeading(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 1 {
		return value[:max]
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func startDetachedRunFolder(stdout io.Writer, service Service, homeDir, folder string, options pgruntime.RunFolderOptions) error {
	sequenceID, err := service.SequenceID(folder, options)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "run-folder-"+time.Now().UTC().Format("20060102150405")+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	args := []string{"__run-folder-supervisor", folder}
	supervisorID := fmt.Sprintf("sup_%d", time.Now().UTC().UnixNano())
	args = append(args,
		fmt.Sprintf("--resume=%t", options.Resume),
		fmt.Sprintf("--fresh=%t", options.Fresh),
		fmt.Sprintf("--restart=%t", options.Restart),
		fmt.Sprintf("--no-resume=%t", options.NoResume),
		fmt.Sprintf("--checkpoint=%t", options.Checkpoint),
		fmt.Sprintf("--commit-each=%t", options.CommitEach),
		fmt.Sprintf("--require-clean-git=%t", options.RequireCleanGit),
		fmt.Sprintf("--allow-concurrent-worktree=%t", options.AllowConcurrentWorktree),
		"--repo", stringDefault(options.RepoPath, "."),
		"--template", stringDefault(options.Template, "codex"),
		fmt.Sprintf("--include-specification=%t", options.IncludeSpecification),
		"--supervisor-id", supervisorID,
		"--supervisor-log", logPath,
	)
	if options.EngineOverride != "" {
		args = append(args, "--engine", options.EngineOverride)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	if err := logFile.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Detached run-folder supervisor started\n")
	fmt.Fprintf(stdout, "Sequence: %s\n", sequenceID)
	fmt.Fprintf(stdout, "Status: promptgrinder sequence %s\n", sequenceID)
	fmt.Fprintf(stdout, "PID: %d\n", cmd.Process.Pid)
	fmt.Fprintf(stdout, "Log: %s\n", logPath)
	fmt.Fprintln(stdout, "Progress: promptgrinder sequences")
	return nil
}

func stringDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func presentLabel(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
}

func capabilityValues(caps engine.Capabilities) []struct {
	Name  string
	Value bool
} {
	values := []struct {
		Name  string
		Value bool
	}{
		{"supports_model", caps.SupportsModel},
		{"supports_profile", caps.SupportsProfile},
		{"supports_sandbox", caps.SupportsSandbox},
		{"supports_approval", caps.SupportsApproval},
		{"supports_working_directory", caps.SupportsWorkingDirectory},
		{"supports_web_search", caps.SupportsWebSearch},
		{"supports_images", caps.SupportsImages},
		{"supports_resume", caps.SupportsResume},
		{"supports_structured_output", caps.SupportsStructuredOutput},
		{"supports_token_usage", caps.SupportsTokenUsage},
		{"supports_cost_usage", caps.SupportsCostUsage},
		{"supports_headless", caps.SupportsHeadless},
		{"supports_interactive_terminal", caps.SupportsInteractiveTerminal},
		{"supports_env", caps.SupportsEnv},
	}
	return values
}

func writeJSON(stdout io.Writer, value any, compact bool) error {
	encoder := json.NewEncoder(stdout)
	if !compact {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

func writeCompactJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	return encoder.Encode(value)
}

func writeJSONError(stdout io.Writer, code, message string, compact bool) {
	_ = writeJSON(stdout, errorJSONOutput{Error: errorJSON{Code: code, Message: message}}, compact)
}

func writeJSONCommandError(stdout io.Writer, code string, err error, compact bool) error {
	writeJSONError(stdout, code, err.Error(), compact)
	return StructuredError{Err: err, Code: errorExitCode(code)}
}

func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	message := err.Error()
	if strings.Contains(message, "worker not found") {
		return "worker_not_found"
	}
	if strings.Contains(message, "invalid worker transition") {
		return "invalid_transition"
	}
	if strings.Contains(message, "unknown engine") || strings.Contains(message, "invalid engine") || strings.Contains(message, "invalid") || strings.Contains(message, "must be") || strings.Contains(message, "does not exist") {
		return "validation_error"
	}
	if strings.Contains(message, "timeout") || strings.Contains(message, "timed out") {
		return "timeout"
	}
	if strings.Contains(message, "launch failed") || strings.Contains(message, "execution failed") || strings.Contains(message, "worker launch failed") {
		return "execution_failed"
	}
	return "state_error"
}

func errorExitCode(code string) int {
	switch code {
	case "worker_not_found":
		return ExitWorkerNotFound
	case "invalid_transition":
		return ExitInvalidTransition
	case "validation_error", "invalid_filter", "invalid_duration":
		return ExitInvalidInput
	case "execution_failed":
		return ExitExecutionFailed
	case "timeout":
		return ExitTimeout
	default:
		return ExitGeneralError
	}
}

func validateEventsArgs(args []string, global bool) error {
	if global {
		if len(args) != 0 {
			return fmt.Errorf("events --global does not accept a worker id")
		}
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("events requires a worker id unless --global is set")
	}
	return nil
}

func validateEventFilter(filter state.EventFilter) error {
	if filter.Tail < 0 {
		return fmt.Errorf("invalid tail value %d: must be zero or greater", filter.Tail)
	}
	if !state.ValidEventType(filter.Type) {
		return fmt.Errorf("invalid event type %q", filter.Type)
	}
	if !state.ValidSeverity(filter.Severity) {
		return fmt.Errorf("invalid severity %q", filter.Severity)
	}
	return nil
}

func printRunFolderSummary(stdout io.Writer, folder string, summary pgruntime.RunFolderSummary) {
	fmt.Fprintln(stdout, "PromptGrinder run started")
	if summary.Resumed {
		fmt.Fprintln(stdout, "Mode: resume")
	} else {
		fmt.Fprintln(stdout, "Mode: fresh")
	}
	if summary.Sequence != nil {
		progress := summary.Sequence.Progress()
		if summary.Resumed {
			fmt.Fprintf(stdout, "Resuming sequence %s\n", progress.SequenceID)
			fmt.Fprintf(stdout, "Already completed: %d\n", progress.Succeeded)
			fmt.Fprintf(stdout, "Remaining: %d\n", progress.Pending+progress.Failed)
			if progress.Current != "" {
				fmt.Fprintf(stdout, "Next prompt: %s\n", progress.Current)
			}
		} else {
			fmt.Fprintf(stdout, "Sequence: %s\n", progress.SequenceID)
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Detected prompts:")
	for _, prompt := range summary.Prompts {
		fmt.Fprintf(stdout, "%-28s %s\n", prompt.Name, prompt.Type)
	}
	for _, warning := range summary.Warnings {
		fmt.Fprintf(stdout, "Warning: %s\n", warning)
	}
	if len(summary.Run.Completed) == 0 && summary.Run.Status == "" {
		return
	}
	completed := map[string]bool{}
	for _, name := range summary.Run.Completed {
		completed[name] = true
	}
	if summary.Sequence != nil {
		for _, item := range summary.Sequence.Items {
			if item.Status == "succeeded" || item.Status == "skipped" {
				completed[item.PromptName] = true
			}
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Execution:")
	for _, prompt := range summary.Prompts {
		switch {
		case completed[prompt.Name]:
			if prompt.Type == "specification" {
				fmt.Fprintf(stdout, "✓ %s metadata loaded\n", prompt.Name)
			} else {
				fmt.Fprintf(stdout, "✓ %s\n", prompt.Name)
			}
		case summary.Run.Current == prompt.Name && summary.Run.Status == "failed":
			fmt.Fprintf(stdout, "✗ %s failed\n", prompt.Name)
		case summary.Run.Current == prompt.Name:
			fmt.Fprintf(stdout, "▶ %s\n", prompt.Name)
		default:
			fmt.Fprintf(stdout, "• %s pending\n", prompt.Name)
		}
	}
	if summary.Run.Status == "completed" {
		fmt.Fprintln(stdout, "Run completed.")
		if summary.Sequence != nil {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "Summary:")
			if summary.Sequence.TokenUsage.Available {
				fmt.Fprintf(stdout, "Total tokens used: %d\n", summary.Sequence.TokenUsage.Total)
			} else {
				fmt.Fprintln(stdout, "Total tokens used: not reported by worker logs")
			}
			fmt.Fprintln(stdout, "Executive summary:")
			fmt.Fprintln(stdout, valueOrDash(summary.Sequence.ExecutiveSummary))
		}
	}
	_ = folder
}

func printRunFolderAggregate(stdout io.Writer, summary pgruntime.RunFolderSummary) {
	for _, warning := range summary.Warnings {
		fmt.Fprintf(stdout, "Warning: %s\n", warning)
	}
	if summary.Run.Status != "completed" || summary.Sequence == nil {
		return
	}
	fmt.Fprintln(stdout, "Summary:")
	if summary.Sequence.TokenUsage.Available {
		fmt.Fprintf(stdout, "Total tokens used: %d\n", summary.Sequence.TokenUsage.Total)
	} else {
		fmt.Fprintln(stdout, "Total tokens used: not reported by worker logs")
	}
	fmt.Fprintln(stdout, "Executive summary:")
	fmt.Fprintln(stdout, valueOrDash(summary.Sequence.ExecutiveSummary))
}

func printTerminalCandidates(stdout io.Writer, candidates []pgruntime.TerminalCandidate) error {
	if len(candidates) == 0 {
		fmt.Fprintln(stdout, "No PromptGrinder Terminal.app tabs found in worker state.")
		return nil
	}
	fmt.Fprintf(stdout, "%-4s %-28s %-14s TASK\n", "#", "WORKER", "STATUS")
	for i, candidate := range candidates {
		fmt.Fprintf(stdout, "%-4d %-28s %-14s %s\n", i+1, candidate.WorkerID, candidate.Status, candidate.TaskPath)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Close tabs with:")
	fmt.Fprintln(stdout, "promptgrinder terminals kill <number>")
	fmt.Fprintln(stdout, "promptgrinder terminals kill 2,3,5,8")
	fmt.Fprintln(stdout, "promptgrinder terminals kill --all")
	return nil
}

func terminalSelections(value string, candidates []pgruntime.TerminalCandidate) ([]string, error) {
	values := strings.Split(value, ",")
	out := []string{}
	seen := map[string]bool{}
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("empty terminal selection in %q", value)
		}
		workerID, err := terminalSelection(item, candidates)
		if err != nil {
			return nil, err
		}
		if !seen[workerID] {
			out = append(out, workerID)
			seen[workerID] = true
		}
	}
	return out, nil
}

func terminalSelection(value string, candidates []pgruntime.TerminalCandidate) (string, error) {
	if index, err := strconv.Atoi(value); err == nil {
		if index < 1 || index > len(candidates) {
			return "", fmt.Errorf("terminal number %d is out of range", index)
		}
		return candidates[index-1].WorkerID, nil
	}
	for _, candidate := range candidates {
		if candidate.WorkerID == value {
			return value, nil
		}
	}
	return "", fmt.Errorf("unknown terminal selection %q", value)
}

func printEventHeader(stdout io.Writer) {
	fmt.Fprintf(stdout, "%-20s %-8s %-24s MESSAGE\n", "TIME", "SEVERITY", "TYPE")
}

func printEvents(stdout io.Writer, events []state.Event) {
	for _, event := range events {
		fmt.Fprintf(stdout, "%-20s %-8s %-24s %s\n", event.Timestamp.Format(time.RFC3339), event.Severity, event.Type, event.Message)
	}
}

func followEvents(ctx context.Context, path string, offset int64, filter state.EventFilter, stdout, stderr io.Writer, jsonMode bool) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			nextOffset, err := printAppendedEvents(path, offset, filter, stdout, stderr, jsonMode)
			if err != nil {
				return err
			}
			offset = nextOffset
		}
	}
}

func readEventsForFollow(path string, filter state.EventFilter) (state.EventReadResult, int64, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return state.EventReadResult{}, 0, nil
	}
	if err != nil {
		return state.EventReadResult{}, 0, err
	}
	defer file.Close()
	result := state.EventReadResult{}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		var event state.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped malformed event line %d", lineNumber))
			continue
		}
		if eventMatches(event, filter) {
			result.Events = append(result.Events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, 0, err
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return result, 0, err
	}
	if filter.Tail > 0 && len(result.Events) > filter.Tail {
		result.Events = result.Events[len(result.Events)-filter.Tail:]
	}
	return result, offset, nil
}

func printAppendedEvents(path string, offset int64, filter state.EventFilter, stdout, stderr io.Writer, jsonMode bool) (int64, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return offset, nil
	}
	if err != nil {
		return offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event state.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			fmt.Fprintln(stderr, "warning: skipped malformed event line while following")
			continue
		}
		if eventMatches(event, filter) {
			if jsonMode {
				if err := writeCompactJSON(stdout, event); err != nil {
					return offset, err
				}
				flush(stdout)
			} else {
				printEvents(stdout, []state.Event{event})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return offset, err
	}
	nextOffset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return offset, err
	}
	return nextOffset, nil
}

func printEventsNDJSON(stdout io.Writer, events []state.Event) error {
	for _, event := range events {
		if err := writeCompactJSON(stdout, event); err != nil {
			return err
		}
		flush(stdout)
	}
	return nil
}

func flush(writer io.Writer) {
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
		return
	}
	if flusher, ok := writer.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}

func eventMatches(event state.Event, filter state.EventFilter) bool {
	if filter.Type != "" && event.Type != filter.Type {
		return false
	}
	if filter.Severity != "" && event.Severity != filter.Severity {
		return false
	}
	return true
}

func lifecycleCommand(use, short string, fn func(string) (state.Worker, error), service Service, stdout io.Writer, compact func() bool) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   use + " <worker-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			previousStatus := ""
			if jsonOutput {
				summary, err := service.Status(args[0])
				if err != nil {
					return writeJSONCommandError(stdout, classifyError(err), err, compact())
				}
				previousStatus = summary.Worker.Status
			}
			worker, err := fn(args[0])
			if err != nil {
				if jsonOutput {
					return writeJSONCommandError(stdout, classifyError(err), err, compact())
				}
				return err
			}
			if jsonOutput {
				return writeJSON(stdout, lifecycleJSONOutput{WorkerID: worker.ID, PreviousStatus: previousStatus, Status: worker.Status, FinishedAt: worker.FinishTime}, compact())
			}
			fmt.Fprintf(stdout, "%s %s as %s.\n", title(use), worker.ID, worker.Status)
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print machine-readable JSON")
	return command
}

func runWorkersJSON(workers []state.Worker) []runWorkerJSON {
	out := []runWorkerJSON{}
	for _, worker := range workers {
		out = append(out, runWorkerJSON{
			WorkerID:        worker.ID,
			Status:          worker.Status,
			TaskPath:        worker.TaskPath,
			Engine:          worker.Engine,
			TerminalAdapter: worker.TerminalAdapter,
		})
	}
	return out
}

func runFailuresJSON(failures []error) []runFailureJSON {
	out := []runFailureJSON{}
	for _, failure := range failures {
		if failure != nil {
			out = append(out, runFailureJSON{Message: failure.Error()})
		}
	}
	return out
}

func ParseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use values like 30m, 2h, or 24h", value)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid duration %q: must be greater than zero", value)
	}
	return duration, nil
}

func formatStatus(status string) string {
	if state.IsTerminalStatus(status) {
		return strings.ToUpper(status)
	}
	return status
}

func formatElapsed(worker state.Worker) string {
	if worker.StartTime == nil {
		return "-"
	}
	end := time.Now().UTC()
	if worker.FinishTime != nil {
		end = *worker.FinishTime
	}
	elapsed := end.Sub(*worker.StartTime)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second).String()
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printDoctor(stdout io.Writer, report firstuse.DoctorReport) {
	for _, check := range report.Checks {
		fmt.Fprintf(stdout, "%-7s %-28s %s\n", strings.ToUpper(string(check.Status)), check.ID, check.Summary)
		if check.Remediation != "" && check.Status != firstuse.Pass {
			fmt.Fprintf(stdout, "        Next: %s\n", check.Remediation)
		}
	}
	if report.OK {
		fmt.Fprintf(stdout, "\nDoctor passed: %d check(s), %d warning(s), no required failures.\n", len(report.Checks), report.Warnings)
	} else {
		fmt.Fprintf(stdout, "\nDoctor failed: %d required failure(s), %d warning(s).\n", report.Failures, report.Warnings)
	}
}

func timeoutValue(metadata map[string]any) string {
	if value, ok := metadata["timeout"].(string); ok && value != "" {
		return value
	}
	return "-"
}

func labelsValue(metadata map[string]any) string {
	labels, ok := metadata["labels"]
	if !ok || labels == nil {
		return "-"
	}
	switch typed := labels.(type) {
	case []string:
		if len(typed) == 0 {
			return "-"
		}
		return strings.Join(typed, ", ")
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		if len(out) == 0 {
			return "-"
		}
		return strings.Join(out, ", ")
	default:
		return "-"
	}
}

func workingDirectoryValue(worker state.Worker) string {
	value, ok := worker.Metadata["working_directory"].(string)
	if !ok || value == "" {
		return worker.RepositoryPath
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(worker.RepositoryPath, value))
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

type terminalStatusWriter interface {
	IsTerminal() bool
}

func shouldRenderInteractive(stdout io.Writer, jsonMode, compact bool) bool {
	if jsonMode || compact {
		return false
	}
	if terminalWriter, ok := stdout.(terminalStatusWriter); ok {
		return terminalWriter.IsTerminal()
	}
	fileWriter, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	info, err := fileWriter.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func setPlainEnvForRun(plain bool) func() {
	if !plain {
		return func() {}
	}
	previous, existed := os.LookupEnv("PROMPTGRINDER_PLAIN")
	_ = os.Setenv("PROMPTGRINDER_PLAIN", "1")
	return func() {
		if existed {
			_ = os.Setenv("PROMPTGRINDER_PLAIN", previous)
			return
		}
		_ = os.Unsetenv("PROMPTGRINDER_PLAIN")
	}
}

func namedWorkerLaunchers(runtimeService pgruntime.Service) map[string]workerlaunch.Launcher {
	return map[string]workerlaunch.Launcher{
		"codex":       workeradapter.Codex{Manager: runtimeService.Worker},
		"antigravity": workeradapter.Antigravity{},
	}
}
