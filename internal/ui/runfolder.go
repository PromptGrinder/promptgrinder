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
	active         string
	activeSince    time.Time
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
		details: make(map[string]runfolder.ProgressEvent),
	}
}

func (r *RunFolderRenderer) Update(event runfolder.ProgressEvent) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if event.Type == "run.started" || event.Type == "prompt.started" || event.Type == "prompt.recovering" || event.Type == "prompt.skipped" || event.Type == "prompt.succeeded" || event.Type == "prompt.failed" || event.Type == "run.completed" {
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
		r.items = append([]runfolder.ProgressPrompt(nil), event.Inventory...)
		r.markdownTotal = event.MarkdownTotal
		r.ignored = append([]string(nil), event.Ignored...)
		r.started = true
		if r.interactive {
			r.renderDashboardLocked()
		} else {
			r.renderPlainStartLocked()
		}
	case "prompt.started":
		r.setStatusLocked(event.PromptName, "active", event.PromptType)
		r.active, r.activeSince, r.frame = event.PromptName, r.now(), 0
		if r.interactive {
			r.renderDashboardLocked()
			r.startTickerLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "prompt.recovering":
		r.setStatusLocked(event.PromptName, "active", event.PromptType)
		r.active, r.activeSince, r.frame = event.PromptName, r.now(), 0
		if r.interactive {
			r.renderDashboardLocked()
			r.startTickerLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "prompt.skipped", "prompt.succeeded", "prompt.failed":
		r.active = ""
		r.setStatusLocked(event.PromptName, event.Status, event.PromptType)
		r.details[event.PromptName] = event
		if r.interactive {
			r.renderDashboardLocked()
		} else {
			r.writePlainEventLocked(event)
		}
	case "run.completed":
		r.active = ""
		if r.interactive {
			r.renderDashboardLocked()
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
	if success {
		fmt.Fprintln(r.w, "Result: succeeded")
		return
	}
	fmt.Fprintln(r.w, "Result: failed")
	if r.folder != "" {
		fmt.Fprintln(r.w, "Resume: promptgrinder run-folder "+shellQuote(r.folder)+" --resume")
	}
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

func (r *RunFolderRenderer) renderPlainStartLocked() {
	if !r.bannerShown {
		Banner(r.w, Options{Theme: r.opts.Theme, Plain: true})
		r.bannerShown = true
	}
	fmt.Fprintln(r.w, "Mode: foreground")
	if r.sequenceID != "" {
		fmt.Fprintln(r.w, "Sequence: "+r.sequenceID)
		fmt.Fprintln(r.w, "Status: promptgrinder sequence "+shellQuote(r.sequenceID))
	}
	fmt.Fprintln(r.w, "Prompts:")
	if r.markdownTotal > 0 {
		fmt.Fprintf(r.w, "Preflight: %d of %d Markdown files included\n", len(r.items), r.markdownTotal)
	}
	if len(r.ignored) > 0 {
		fmt.Fprintln(r.w, "Ignored: "+strings.Join(r.ignored, ", "))
	}
	for i, item := range r.items {
		fmt.Fprintf(r.w, "[%d/%d] %s [%s] - %s\n", i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status))
	}
}

func (r *RunFolderRenderer) writePlainEventLocked(event runfolder.ProgressEvent) {
	if event.Type == "prompt.succeeded" || event.Type == "prompt.failed" {
		index, total := r.eventPositionLocked(event)
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
	lines := []string{"Mode: foreground"}
	if r.sequenceID != "" {
		lines = append(lines, "Sequence: "+r.sequenceID)
		lines = append(lines, "Status: promptgrinder sequence "+shellQuote(r.sequenceID))
	}
	lines = append(lines, "Prompts:")
	if r.markdownTotal > 0 {
		lines = append(lines, fmt.Sprintf("Preflight: %d of %d Markdown files included", len(r.items), r.markdownTotal))
	}
	if len(r.ignored) > 0 {
		lines = append(lines, "Ignored: "+strings.Join(r.ignored, ", "))
	}
	for i, item := range r.items {
		icon := colorizeStatusIcon(stateIcon(item.Status), item.Status, themeColor(r.opts.Theme))
		duration, detail := "", r.details[item.Name]
		if item.Name == r.active {
			icon = colorizeStatusIcon(spinnerFrames[r.frame%len(spinnerFrames)], "active", themeColor(r.opts.Theme))
			duration = " " + formatDuration(r.now().Sub(r.activeSince))
		} else if item.Status == "succeeded" || item.Status == "failed" || item.Status == "skipped" {
			duration = " " + formatDuration(detail.Duration)
		}
		line := fmt.Sprintf("%s [%d/%d] %s [%s] - %s%s", icon, i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status), duration)
		if item.Status == "succeeded" || item.Status == "failed" {
			line = fmt.Sprintf("%s [%d/%d] %s|%s", icon, i+1, len(r.items), item.Name, compactRunFolderIdentity(detail))
		}
		if item.Status == "failed" && detail.LogPath != "" {
			line += " (log: " + terminalFileLink(detail.LogPath) + ")"
		}
		lines = append(lines, line)
		if item.Status == "failed" {
			lines = append(lines, failureDetailLines(detail)...)
		}
	}
	for _, line := range lines {
		fmt.Fprint(r.w, "\033[2K"+line+"\n")
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
	if event.Status != "failed" && event.Type != "prompt.failed" && event.Type != "prompt.recovering" {
		return nil
	}
	lines := make([]string, 0, 2)
	if event.Reason != "" {
		lines = append(lines, "  Reason: "+event.Reason)
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
	return lines
}

func stateIcon(status string) string {
	switch status {
	case "active", "running", "recovering":
		return "▶"
	case "skipped":
		return "○"
	case "succeeded", "completed":
		return "✓"
	case "failed":
		return "✗"
	default:
		return "·"
	}
}
func stateLabel(status string) string {
	switch status {
	case "active", "running", "recovering":
		return "active"
	case "succeeded", "completed":
		return "succeeded"
	case "failed":
		return "failed"
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
