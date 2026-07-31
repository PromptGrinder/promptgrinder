package ui

import (
	"fmt"
	"io"
	"os"
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

	mu          sync.Mutex
	opMu        sync.Mutex
	items       []runfolder.ProgressPrompt
	details     map[string]runfolder.ProgressEvent
	sequenceID  string
	folder      string
	active      string
	activeSince time.Time
	frame       int
	lines       int
	started     bool
	bannerShown bool
	closed      bool
	stop        chan struct{}
	done        chan struct{}
	ticker      rendererTicker
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
	if event.Type == "run.started" || event.Type == "prompt.started" || event.Type == "prompt.skipped" || event.Type == "prompt.succeeded" || event.Type == "prompt.failed" || event.Type == "run.completed" {
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
		} else {
			fmt.Fprintln(r.w, "Result: succeeded")
		}
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
	}
	fmt.Fprintln(r.w, "Prompts:")
	for i, item := range r.items {
		fmt.Fprintf(r.w, "[%d/%d] %s [%s] - %s\n", i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status))
	}
}

func (r *RunFolderRenderer) writePlainEventLocked(event runfolder.ProgressEvent) {
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
	if event.Type == "prompt.failed" && r.folder != "" {
		fmt.Fprintln(r.w, "Resume: promptgrinder run-folder "+shellQuote(r.folder)+" --resume")
	}
}

func (r *RunFolderRenderer) renderDashboardLocked() {
	if r.lines > 0 {
		fmt.Fprintf(r.w, "\033[%dA", r.lines)
	} else if !r.bannerShown {
		Banner(r.w, r.opts)
		r.bannerShown = true
	}
	lines := []string{"Mode: foreground"}
	if r.sequenceID != "" {
		lines = append(lines, "Sequence: "+r.sequenceID)
	}
	lines = append(lines, "Prompts:")
	for i, item := range r.items {
		icon := stateIcon(item.Status)
		duration, detail := "", r.details[item.Name]
		if item.Name == r.active {
			icon = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[r.frame%10]
			duration = " " + formatDuration(r.now().Sub(r.activeSince))
		} else if item.Status == "succeeded" || item.Status == "failed" || item.Status == "skipped" {
			duration = " " + formatDuration(detail.Duration)
		}
		line := fmt.Sprintf("%s [%d/%d] %s [%s] - %s%s", icon, i+1, len(r.items), item.Name, item.Type, stateLabel(item.Status), duration)
		if detail.WorkerID != "" {
			line += " (worker: " + detail.WorkerID + ")"
		}
		if detail.LogPath != "" {
			line += " (log: " + detail.LogPath + ")"
		}
		lines = append(lines, line)
	}
	for _, line := range lines {
		fmt.Fprint(r.w, "\033[2K"+line+"\n")
	}
	r.lines = len(lines)
}

func stateIcon(status string) string {
	switch status {
	case "active", "running":
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
	case "active", "running":
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
