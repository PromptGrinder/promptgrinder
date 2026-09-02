package ui

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"promptgrinder/internal/runfolder"
	"promptgrinder/internal/state"
)

type rendererTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realRendererTicker struct{ *time.Ticker }

func (t realRendererTicker) Chan() <-chan time.Time { return t.C }

type RunFolderRenderer struct {
	w           io.Writer
	interactive bool
	opts        Options
	now         func() time.Time
	newTicker   func(time.Duration) rendererTicker

	mu             sync.Mutex
	opMu           sync.Mutex
	items          []runfolder.ProgressPrompt
	markdownTotal  int
	ignored        []string
	details        map[string]runfolder.ProgressEvent
	sequenceID     string
	folder         string
	resumePlan     string
	parallel       bool
	featureBranch  string
	prHint         string
	activeSince    map[string]time.Time
	frame          int
	dashboardSaved bool
	started        bool
	bannerShown    bool
	closed         bool
	finished       bool
	stop           chan struct{}
	done           chan struct{}
	ticker         rendererTicker
}

func NewRunFolderRenderer(w io.Writer, interactive bool, opts Options) *RunFolderRenderer {
	plain := opts.Plain || os.Getenv("NO_COLOR") != ""
	opts.Plain = plain
	return &RunFolderRenderer{
		w: w, interactive: interactive && !plain, opts: opts,
		now: time.Now, newTicker: func(d time.Duration) rendererTicker { return realRendererTicker{time.NewTicker(d)} },
		details:     make(map[string]runfolder.ProgressEvent),
		activeSince: make(map[string]time.Time),
	}
}

func (r *RunFolderRenderer) Update(event runfolder.ProgressEvent) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if !r.parallel && (event.Type == "run.started" || event.Type == "prompt.started" || event.Type == "prompt.recovering" || event.Type == "prompt.waiting-to-merge" || event.Type == "prompt.skipped" || event.Type == "prompt.succeeded" || event.Type == "prompt.failed" || event.Type == "prompt.gate-blocked" || event.Type == "run.completed") {
		r.stopTicker()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}

	switch event.Type {
	case "run.started":
		r.sequenceID, r.folder = event.SequenceID, event.Folder
		r.resumePlan = event.ResumePlan
		r.parallel, r.featureBranch = event.ParallelWorktrees, event.FeatureBranch
		r.items = append([]runfolder.ProgressPrompt(nil), event.Inventory...)
		for _, item := range r.items {
			if item.WorkerID == "" && item.Duration == 0 && item.LogPath == "" {
				continue
			}
			r.details[item.Name] = runfolder.ProgressEvent{
				PromptName: item.Name, PromptType: item.Type, Status: item.Status,
				Lane: item.Lane, Priority: item.Priority, Worktree: item.Worktree,
				IntegrationState: item.IntegrationState, IntegrationSHA: item.IntegrationSHA,
				WorkerID: item.WorkerID, Scope: item.Scope, Engine: item.Engine, Model: item.Model,
				LogPath: item.LogPath, Duration: item.Duration,
			}
		}
		r.markdownTotal = event.MarkdownTotal
		r.ignored = append([]string(nil), event.Ignored...)
		r.started = true
		if r.interactive {
			r.renderDashboardLocked()
		} else {
			r.renderPlainStartLocked()
		}
	case "prompt.started":
		status := "active"
		if r.parallel && event.Status != "" {
			status = event.Status
		}
		r.setStatusLocked(event.PromptName, status, event.PromptType)
		r.applyParallelDetailLocked(event)
		r.activeSince[event.PromptName] = r.now()
		r.frame = 0
		if r.interactive {
			r.renderDashboardLocked()
			r.startTickerLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "prompt.waiting-to-merge":
		delete(r.activeSince, event.PromptName)
		r.setStatusLocked(event.PromptName, "waiting-to-merge", event.PromptType)
		r.applyParallelDetailLocked(event)
		r.details[event.PromptName] = event
		if r.interactive {
			r.renderDashboardLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "prompt.recovering":
		r.setStatusLocked(event.PromptName, "active", event.PromptType)
		r.applyParallelDetailLocked(event)
		r.activeSince[event.PromptName] = r.now()
		r.frame = 0
		if r.interactive {
			r.renderDashboardLocked()
			r.startTickerLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "prompt.skipped", "prompt.succeeded", "prompt.failed", "prompt.gate-blocked":
		delete(r.activeSince, event.PromptName)
		r.setStatusLocked(event.PromptName, event.Status, event.PromptType)
		r.applyParallelDetailLocked(event)
		r.details[event.PromptName] = event
		if r.interactive {
			r.renderDashboardLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "run.completed":
		r.activeSince = make(map[string]time.Time)
		r.prHint = event.PRHint
		if r.interactive {
			r.renderDashboardLocked()
		} else if r.prHint != "" {
			fmt.Fprintln(r.w, "Next action: "+r.prHint)
		}
	}
}

// Finish stops any live animation before appending the stable sequence result
// and, after failure, a shell-safe resume command.
func (r *RunFolderRenderer) Finish(success bool) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.stopTicker()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.finished {
		return
	}
	r.finished = true
	if r.hasProductBlockedLocked() {
		fmt.Fprintln(r.w, "Result: product-blocked")
		r.renderParallelSubwayLocked(false)
		return
	}
	if success {
		fmt.Fprintln(r.w, "Result: succeeded")
		r.renderParallelSubwayLocked(true)
		return
	}
	failed := r.latestFailureLocked()
	label, summary := "failed", ""
	if failed.CompletionStatus == "BLOCKED" {
		label = "BLOCKED"
	} else if failed.FailureReport != nil && failed.FailureReport.Category == "cancellation" {
		label = "cancelled"
	}
	if failed.FailureReport != nil {
		summary = failed.FailureReport.Summary
	}
	if summary == "" {
		summary = failed.Reason
	}
	if summary != "" {
		fmt.Fprintf(r.w, "Result: %s — %s\n", label, summary)
	} else {
		fmt.Fprintf(r.w, "Result: %s\n", label)
	}
	r.renderParallelSubwayLocked(false)
	if r.folder != "" {
		command := "Resume: promptgrinder run-folder " + shellQuote(r.folder)
		if r.parallel {
			command += " --parallel-worktrees --checkpoint --commit-each --require-clean-git"
		}
		fmt.Fprintln(r.w, command+" --resume")
	}
}

// renderParallelSubwayLocked renders only after the train has stopped. It is
// an ASCII overview of lane integration, not a substitute for git history.
func (r *RunFolderRenderer) renderParallelSubwayLocked(featureFastForwarded bool) {
	if !r.parallel {
		return
	}
	integrated := make([]runfolder.ProgressPrompt, 0, len(r.items))
	for _, item := range r.items {
		if item.IntegrationState == "integrated" {
			integrated = append(integrated, item)
		}
	}
	if len(integrated) == 0 && len(r.items) == 0 {
		return
	}
	fmt.Fprintln(r.w, "Git subway:")
	if len(integrated) > 0 {
		target := "integration (retained)"
		if featureFastForwarded && r.featureBranch != "" {
			target = r.featureBranch
		}
		mergeSHA := shortSHA(integrated[len(integrated)-1].IntegrationSHA)
		for index, item := range integrated {
			connector := "---+"
			switch {
			case len(integrated) == 1:
				connector = "----->"
			case index == 0:
				connector = "---\\"
			case index == len(integrated)-1:
				connector = "---/"
			}
			line := fmt.Sprintf("  %-28s %s%s", parallelLaneLabel(item), r.subwayMarker(item.Status), connector)
			if len(integrated) == 1 || index == (len(integrated)-1)/2 {
				line += " " + target
				if mergeSHA != "" {
					line += "@" + mergeSHA
				}
			}
			fmt.Fprintln(r.w, line)
		}
	}
	for _, item := range r.items {
		if item.Type == runfolder.TypeSpecification || item.IntegrationState == "integrated" {
			continue
		}
		state := parallelStatusLabel(item, r.items)
		if state == "" {
			state = "in progress"
		}
		fmt.Fprintf(r.w, "  %-28s %s %s\n", parallelLaneLabel(item), r.subwayMarker(item.Status), state)
	}
}

func (r *RunFolderRenderer) subwayMarker(status string) string {
	marker, colorStatus := ".", "pending"
	switch status {
	case "succeeded", "completed", "integrated":
		marker, colorStatus = "o", "succeeded"
	case "waiting-to-merge":
		marker, colorStatus = "o", "waiting-to-merge"
	case "failed", "gate-blocked":
		marker, colorStatus = "x", "failed"
	case "active", "running", "working", "recovering":
		marker, colorStatus = "*", "active"
	}
	if r.opts.Plain {
		return marker
	}
	return colorizeStatusIcon(marker, colorStatus, themeColor(r.opts.Theme))
}

func (r *RunFolderRenderer) latestFailureLocked() runfolder.ProgressEvent {
	for i := len(r.items) - 1; i >= 0; i-- {
		if r.items[i].Status == "failed" {
			return r.details[r.items[i].Name]
		}
	}
	return runfolder.ProgressEvent{}
}

func (r *RunFolderRenderer) hasProductBlockedLocked() bool {
	for _, item := range r.items {
		if item.Status == "gate-blocked" {
			return true
		}
	}
	return false
}

func (r *RunFolderRenderer) Close() {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.stopTicker()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
}

func (r *RunFolderRenderer) startTickerLocked() {
	if r.stop != nil {
		return
	}
	ticker := r.newTicker(100 * time.Millisecond)
	stop, done := make(chan struct{}), make(chan struct{})
	r.stop, r.done = stop, done
	r.ticker = ticker
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.Chan():
				r.mu.Lock()
				if r.stop == stop && !r.closed {
					r.frame++
					r.renderDashboardLocked()
				}
				r.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
}

func (r *RunFolderRenderer) stopTicker() {
	r.mu.Lock()
	if r.stop == nil {
		r.mu.Unlock()
		return
	}
	stop, done, ticker := r.stop, r.done, r.ticker
	r.stop, r.done, r.ticker = nil, nil, nil
	close(stop)
	if ticker != nil {
		ticker.Stop()
	}
	r.mu.Unlock()
	<-done
}

func (r *RunFolderRenderer) setStatusLocked(name, status string, typ runfolder.PromptType) {
	for i := range r.items {
		if r.items[i].Name == name {
			r.items[i].Status = status
			return
		}
	}
	r.items = append(r.items, runfolder.ProgressPrompt{Name: name, Type: typ, Status: status})
}

func (r *RunFolderRenderer) applyParallelDetailLocked(event runfolder.ProgressEvent) {
	for index := range r.items {
		if r.items[index].Name != event.PromptName {
			continue
		}
		if event.Lane != "" {
			r.items[index].Lane = event.Lane
		}
		if event.Priority != 0 {
			r.items[index].Priority = event.Priority
		}
		if event.Worktree != "" {
			r.items[index].Worktree = event.Worktree
		}
		if event.IntegrationState != "" {
			r.items[index].IntegrationState = event.IntegrationState
		}
		if event.IntegrationSHA != "" {
			r.items[index].IntegrationSHA = event.IntegrationSHA
		}
		return
	}
}

func (r *RunFolderRenderer) renderPlainStartLocked() {
	if !r.bannerShown {
		Banner(r.w, Options{Theme: r.opts.Theme, Plain: true})
		r.bannerShown = true
	}
	if r.parallel {
		fmt.Fprintln(r.w, "Mode: parallel worktrees · foreground")
		if r.featureBranch != "" {
			fmt.Fprintln(r.w, "Feature branch: "+r.featureBranch)
		}
	} else {
		fmt.Fprintln(r.w, "Mode: foreground")
	}
	if r.sequenceID != "" {
		fmt.Fprintln(r.w, "Sequence: "+r.sequenceID)
		fmt.Fprintln(r.w, "Status: promptgrinder sequence "+shellQuote(r.sequenceID))
		fmt.Fprintln(r.w, "Cancel: promptgrinder sequence cancel "+shellQuote(r.sequenceID))
	}
	if r.resumePlan != "" {
		fmt.Fprintln(r.w, "Resume plan: "+r.resumePlan)
	}
	if r.parallel {
		fmt.Fprintln(r.w, parallelActivitySummary(r.items))
		if root := r.worktreeRootLocked(); root != "" {
			fmt.Fprintln(r.w, "Worktree root: "+compactWorktreeRoot(root))
		}
		fmt.Fprintln(r.w, "Lanes:")
	} else {
		fmt.Fprintln(r.w, "Prompts:")
	}
	if r.markdownTotal > 0 {
		fmt.Fprintf(r.w, "Preflight: %d of %d Markdown files included\n", len(r.items), r.markdownTotal)
	}
	if len(r.ignored) > 0 {
		fmt.Fprintln(r.w, "Ignored: "+strings.Join(r.ignored, ", "))
	}
	for i, item := range r.items {
		if r.parallel && item.Type != runfolder.TypeSpecification {
			fmt.Fprintf(r.w, "%s P%d %-30s", parallelTreePrefix(i, len(r.items)), item.Priority, parallelLaneLabel(item))
			if status := parallelStatusLabel(item, r.items); status != "" {
				fmt.Fprint(r.w, " | "+status)
			}
			detail := r.details[item.Name]
			if item.Duration > 0 {
				fmt.Fprint(r.w, " | "+formatDuration(item.Duration))
			}
			if merge := parallelMergeLabel(item, r.featureBranch); merge != "" {
				fmt.Fprint(r.w, " | "+merge)
			} else if leaf := filepath.Base(item.Worktree); item.Worktree != "" && leaf != "." {
				fmt.Fprintf(r.w, " | %s", leaf)
			}
			if item.Status == "succeeded" || item.Status == "integrated" || item.Status == "failed" || item.Status == "gate-blocked" {
				fmt.Fprint(r.w, " | "+compactParallelIdentity(detail))
				writeParallelWorkerMeta(r.w, detail)
			}
			fmt.Fprintln(r.w)
			continue
		}
		fmt.Fprintf(r.w, "[%d/%d] %s [%s] - %s\n", i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status))
	}
}

func parallelTreePrefix(index, total int) string {
	if index+1 == total {
		return "└─"
	}
	return "├─"
}

func parallelLaneLabel(item runfolder.ProgressPrompt) string {
	if item.Lane != "" {
		return item.Lane
	}
	return item.Name
}

func parallelStatusLabel(item runfolder.ProgressPrompt, items []runfolder.ProgressPrompt) string {
	if item.Status == "succeeded" || item.Status == "completed" || item.Status == "integrated" {
		// The green tick already communicates success. Keep completed lane rows
		// compact so their duration and worker identity remain visible.
		return ""
	}
	if item.Status == "active" || item.Status == "running" || item.Status == "working" || item.Status == "recovering" {
		return ""
	}
	if item.Status != "pending" || len(item.DependsOn) == 0 {
		return stateLabel(item.Status)
	}
	priorities := make(map[string]int, len(items))
	for _, candidate := range items {
		priorities[candidate.ID] = candidate.Priority
	}
	labels := make([]string, 0, len(item.DependsOn))
	for _, dependency := range item.DependsOn {
		if priority := priorities[dependency]; priority > 0 {
			labels = append(labels, fmt.Sprintf("P%d", priority))
		} else {
			labels = append(labels, dependency)
		}
	}
	return "waiting on " + strings.Join(labels, ", ")
}

func parallelMergeLabel(item runfolder.ProgressPrompt, featureBranch string) string {
	if item.IntegrationState != "integrated" || item.IntegrationSHA == "" || featureBranch == "" || item.Worktree == "" {
		return ""
	}
	return filepath.Base(item.Worktree) + " → " + featureBranch + "@" + shortSHA(item.IntegrationSHA)
}

func shortSHA(value string) string {
	if len(value) > 7 {
		return value[:7]
	}
	return value
}

func parallelActivitySummary(items []runfolder.ProgressPrompt) string {
	working, waiting, merging := 0, 0, 0
	for _, item := range items {
		switch item.Status {
		case "working", "active", "running", "recovering":
			working++
		case "waiting-to-merge":
			merging++
		case "pending":
			waiting++
		}
	}
	parts := []string{}
	if working > 0 {
		parts = append(parts, fmt.Sprintf("%d working", working))
	}
	if merging > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting-to-merge", merging))
	}
	if waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting", waiting))
	}
	if len(parts) == 0 {
		return "Lanes: idle"
	}
	return "Lanes: " + strings.Join(parts, " · ")
}

func (r *RunFolderRenderer) worktreeRootLocked() string {
	for _, item := range r.items {
		if item.Worktree != "" {
			return filepath.Dir(item.Worktree)
		}
	}
	return ""
}

func compactWorktreeRoot(root string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(root, home+string(filepath.Separator)) {
		root = "~" + strings.TrimPrefix(root, home)
	}
	parts := strings.Split(filepath.ToSlash(root), "/")
	for index, part := range parts {
		if (strings.HasPrefix(part, "seq_") || strings.HasPrefix(part, "run_")) && len(part) > 15 {
			parts[index] = part[:15] + "…"
		}
	}
	return strings.Join(parts, "/") + "/"
}

func (r *RunFolderRenderer) writePlainEventLocked(event runfolder.ProgressEvent) {
	if event.Type == "prompt.succeeded" || event.Type == "prompt.failed" || event.Type == "prompt.gate-blocked" {
		index, total := r.eventPositionLocked(event)
		if r.parallel && event.Lane != "" {
			fmt.Fprintf(r.w, "%s %s P%d %-30s", stateIcon(event.Status), parallelTreePrefix(index-1, total), event.Priority, event.Lane)
			if status := parallelStatusLabel(runfolder.ProgressPrompt{Status: event.Status}, nil); status != "" {
				fmt.Fprint(r.w, " "+status)
			}
			fmt.Fprint(r.w, " | "+compactParallelIdentity(event))
			if event.IntegrationSHA != "" && r.featureBranch != "" {
				fmt.Fprintf(r.w, " | %s → %s@%s", filepath.Base(event.Worktree), r.featureBranch, shortSHA(event.IntegrationSHA))
			} else if leaf := filepath.Base(event.Worktree); event.Worktree != "" && leaf != "." {
				fmt.Fprintf(r.w, " | %s", leaf)
			}
			writeParallelWorkerMeta(r.w, event)
			fmt.Fprintln(r.w)
			writeFailureDetails(r.w, event)
			return
		}
		fmt.Fprintf(r.w, "%s [%d/%d] %s|%s\n", stateIcon(event.Status), index, total, event.PromptName, compactRunFolderIdentity(event))
		writeFailureDetails(r.w, event)
		return
	}
	duration := ""
	if event.Type != "prompt.started" {
		duration = " in " + formatDuration(event.Duration)
	}
	fmt.Fprintf(r.w, "%s [%s] - %s%s", event.PromptName, event.PromptType, stateLabel(event.Status), duration)
	if event.WorkerID != "" {
		fmt.Fprintf(r.w, " (worker: %s)", event.WorkerID)
	}
	if event.LogPath != "" {
		fmt.Fprintf(r.w, " (log: %s)", event.LogPath)
	}
	fmt.Fprintln(r.w)
	writeFailureDetails(r.w, event)
}

func (r *RunFolderRenderer) eventPositionLocked(event runfolder.ProgressEvent) (int, int) {
	total := event.Total
	if total == 0 {
		total = len(r.items)
	}
	for index, item := range r.items {
		if item.Name == event.PromptName {
			return index + 1, total
		}
	}
	index := event.Completed
	if event.Type == "prompt.failed" {
		index++
	}
	return index, total
}

func (r *RunFolderRenderer) renderDashboardLocked() {
	if r.dashboardSaved {
		// Restore the dashboard origin and erase everything below it. This
		// remains correct when long rows occupy multiple terminal lines.
		fmt.Fprint(r.w, "\033[u\033[J")
	} else {
		if !r.bannerShown {
			Banner(r.w, r.opts)
			r.bannerShown = true
		}
		fmt.Fprint(r.w, "\033[s")
		r.dashboardSaved = true
	}
	mode := "Mode: foreground"
	if r.parallel {
		mode = "Mode: parallel worktrees · foreground"
	}
	lines := []string{mode}
	if r.parallel && r.featureBranch != "" {
		lines = append(lines, "Feature branch: "+r.featureBranch)
	}
	if r.sequenceID != "" {
		lines = append(lines, "Sequence: "+r.sequenceID)
		lines = append(lines, "Status: promptgrinder sequence "+shellQuote(r.sequenceID))
		lines = append(lines, "Cancel: promptgrinder sequence cancel "+shellQuote(r.sequenceID))
	}
	if r.resumePlan != "" {
		lines = append(lines, "Resume plan: "+r.resumePlan)
	}
	if r.prHint != "" {
		lines = append(lines, "Next action: "+r.prHint)
	}
	if r.parallel {
		lines = append(lines, parallelActivitySummary(r.items))
		if root := r.worktreeRootLocked(); root != "" {
			lines = append(lines, "Worktree root: "+compactWorktreeRoot(root))
		}
		lines = append(lines, "Lanes:")
	} else {
		lines = append(lines, "Prompts:")
	}
	if r.markdownTotal > 0 {
		lines = append(lines, fmt.Sprintf("Preflight: %d of %d Markdown files included", len(r.items), r.markdownTotal))
	}
	if len(r.ignored) > 0 {
		lines = append(lines, "Ignored: "+strings.Join(r.ignored, ", "))
	}
	for i, item := range r.items {
		icon := colorizeStatusIcon(stateIcon(item.Status), item.Status, themeColor(r.opts.Theme))
		duration, detail := "", r.details[item.Name]
		if started, active := r.activeSince[item.Name]; active {
			frame := r.frame
			if r.parallel {
				frame += i
			}
			icon = colorizeStatusIcon(spinnerFrames[frame%len(spinnerFrames)], "active", themeColor(r.opts.Theme))
			duration = " " + formatDuration(r.now().Sub(started))
		} else if item.Status == "succeeded" || item.Status == "integrated" || item.Status == "failed" || item.Status == "skipped" || item.Status == "gate-blocked" {
			duration = " " + formatDuration(detail.Duration)
		}
		line := fmt.Sprintf("%s [%d/%d] %s [%s] - %s%s", icon, i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status), duration)
		if r.parallel && item.Type != runfolder.TypeSpecification {
			line = fmt.Sprintf("%s %s P%d %-30s", icon, parallelTreePrefix(i, len(r.items)), item.Priority, parallelLaneLabel(item))
			if status := parallelStatusLabel(item, r.items); status != "" {
				line += " | " + status
			}
			if duration != "" {
				line += " | " + strings.TrimSpace(duration)
			}
			if merge := parallelMergeLabel(item, r.featureBranch); merge != "" {
				line += " | " + merge
			} else if leaf := filepath.Base(item.Worktree); item.Worktree != "" && leaf != "." {
				line += " | " + leaf
			}
		}
		if (item.Status == "succeeded" || item.Status == "integrated" || item.Status == "failed" || item.Status == "gate-blocked") && !r.parallel {
			line = fmt.Sprintf("%s [%d/%d] %s|%s", icon, i+1, len(r.items), item.Name, compactRunFolderIdentity(detail))
		} else if r.parallel && (item.Status == "succeeded" || item.Status == "integrated" || item.Status == "failed" || item.Status == "gate-blocked") {
			line += " | " + compactParallelIdentity(detail)
			if detail.WorkerID != "" {
				line += " | worker " + detail.WorkerID
			}
			if detail.Terminal != "" {
				line += " | terminal " + detail.Terminal
			}
		}
		if (item.Status == "failed" || item.Status == "gate-blocked") && detail.LogPath != "" {
			line += " (log: " + terminalFileLink(detail.LogPath) + ")"
		}
		lines = append(lines, line)
		if item.Status == "failed" || item.Status == "gate-blocked" {
			lines = append(lines, failureDetailLines(detail)...)
		}
	}
	for _, line := range lines {
		fmt.Fprint(r.w, "\033[2K"+line+"\n")
	}
}

func writeParallelWorkerMeta(w io.Writer, event runfolder.ProgressEvent) {
	if event.WorkerID != "" {
		fmt.Fprint(w, " | worker "+event.WorkerID)
	}
	if event.Terminal != "" {
		fmt.Fprint(w, " | terminal "+event.Terminal)
	}
}

func compactRunFolderIdentity(event runfolder.ProgressEvent) string {
	scope, engine, model := event.Scope, event.Engine, event.Model
	if scope == "" {
		scope = "unscoped"
	}
	if engine == "" {
		engine = "unknown-engine"
	}
	if model == "" {
		model = "default"
	}
	return scope + "|" + engine + "/" + model + "|" + formatDuration(event.Duration)
}

func compactParallelIdentity(event runfolder.ProgressEvent) string {
	scope, engine, model := event.Scope, event.Engine, event.Model
	if scope == "" {
		scope = "unscoped"
	}
	if engine == "" {
		engine = "unknown-engine"
	}
	if model == "" {
		model = "default"
	}
	return scope + " | " + engine + "/" + model
}

func terminalFileLink(path string) string {
	if path == "" {
		return ""
	}
	if hasTerminalControl(path) {
		return "worker log"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	target := (&url.URL{Scheme: "file", Path: absolute}).String()
	return "\033]8;;" + target + "\033\\" + absolute + "\033]8;;\033\\"
}

func hasTerminalControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func writeFailureDetails(w io.Writer, event runfolder.ProgressEvent) {
	for _, line := range failureDetailLines(event) {
		fmt.Fprintln(w, line)
	}
}

func failureDetailLines(event runfolder.ProgressEvent) []string {
	if event.Status != "failed" && event.Status != "gate-blocked" && event.Type != "prompt.failed" && event.Type != "prompt.recovering" && event.Type != "prompt.gate-blocked" {
		return nil
	}
	lines := make([]string, 0, 12)
	if event.RecoveryArtifact != "" {
		lines = append(lines, "  Retained artifact: "+event.RecoveryArtifact)
	}
	if event.CompletionStatus != "" || event.NextPromptSafe != nil {
		status, safe := "-", "-"
		if event.CompletionStatus != "" {
			status = event.CompletionStatus
		}
		if event.NextPromptSafe != nil {
			if *event.NextPromptSafe {
				safe = "yes"
			} else {
				safe = "no"
			}
		}
		lines = append(lines, fmt.Sprintf("  Completion: STATUS=%s NEXT_PROMPT_SAFE=%s", status, safe))
	}
	if report := event.FailureReport; report != nil {
		if report.Category != "" {
			lines = append(lines, "  Failure type: "+failureCategoryLabel(report.Category))
		}
		if report.Summary != "" {
			lines = append(lines, "  Failure summary: "+report.Summary)
		}
		lines = append(lines, renderFailureEvidence(report)...)
		if report.NextAction != "" {
			lines = append(lines, "  Next action: "+report.NextAction)
		}
		if failureReportTruncated(report) && event.LogPath != "" {
			lines = append(lines, "  See worker log for remaining details: "+event.LogPath)
		}
		return lines
	}
	if event.Reason != "" {
		lines = append(lines, "  Reason: "+event.Reason)
	}
	return lines
}

func failureCategoryLabel(category string) string {
	switch category {
	case "product-test":
		return "product/test failure"
	case "environment-capability":
		return "environment/capability block"
	case "path-policy":
		return "path-policy violation"
	case "worker-crash":
		return "worker/tool crash"
	case "cancellation":
		return "cancellation"
	default:
		return category
	}
}

func renderFailureEvidence(report *state.FailureReport) []string {
	lines := make([]string, 0, 10)
	if len(report.FeatureEvidence) > 0 {
		lines = append(lines, "  Feature evidence:")
		for _, item := range report.FeatureEvidence[:min(len(report.FeatureEvidence), 4)] {
			lines = append(lines, "    - "+item)
		}
	}
	if len(report.BlockingChecks) > 0 {
		lines = append(lines, "  Blocking checks:")
		for _, check := range report.BlockingChecks[:min(len(report.BlockingChecks), 4)] {
			lines = append(lines, "    - "+check.Summary)
			for _, detail := range check.Details[:min(len(check.Details), 2)] {
				lines = append(lines, "      - "+detail)
			}
		}
	}
	if report.EvidenceReport != "" {
		lines = append(lines, "  Evidence report: "+report.EvidenceReport)
	}
	return lines
}

func failureReportTruncated(report *state.FailureReport) bool {
	if len(report.FeatureEvidence) > 4 || len(report.BlockingChecks) > 4 {
		return true
	}
	for _, check := range report.BlockingChecks {
		if len(check.Details) > 2 {
			return true
		}
	}
	return false
}

func stateIcon(status string) string {
	switch status {
	case "active", "running", "recovering", "working":
		return "▶"
	case "integrating":
		return "↻"
	case "waiting-to-merge":
		return "◌"
	case "skipped":
		return "○"
	case "succeeded", "completed", "integrated":
		return "✓"
	case "failed":
		return "✗"
	case "gate-blocked":
		return "■"
	default:
		return "○"
	}
}
func stateLabel(status string) string {
	switch status {
	case "active", "running", "recovering":
		return "active"
	case "working":
		return "working"
	case "integrating":
		return "integrating"
	case "waiting-to-merge":
		return "waiting-to-merge"
	case "succeeded", "completed", "integrated":
		return "succeeded"
	case "failed":
		return "failed"
	case "gate-blocked":
		return "product-blocked"
	case "skipped":
		return "skipped"
	default:
		return "pending"
	}
}
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!&;|<>()[]{}*?") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
