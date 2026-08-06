package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pgruntime "promptgrinder/internal/runtime"
)

const Version = "dev"

var spinnerFrames = []string{"|", "/", "-", "\\"}

type Theme string

const (
	ThemeDefault Theme = "default"
	ThemeMinimal Theme = "minimal"
	ThemeLoud    Theme = "loud"
)

type Options struct {
	Theme Theme
	Plain bool
}

type LaunchHeader struct {
	WorkerID       string
	TaskPath       string
	RepositoryPath string
	StartedAt      *time.Time
	LogPath        string
}

type SharedProgressRenderer struct {
	w           io.Writer
	animated    bool
	color       string
	mu          sync.Mutex
	current     pgruntime.SharedRunProgress
	frame       int
	spinnerStop chan struct{}
	spinnerDone chan struct{}
}

func NewSharedProgressRenderer(w io.Writer, interactive bool, opts Options) *SharedProgressRenderer {
	return &SharedProgressRenderer{w: w, animated: interactive && !opts.Plain, color: themeColor(opts.Theme)}
}

func (r *SharedProgressRenderer) Update(progress pgruntime.SharedRunProgress) {
	r.stopSpinner()
	if progress.Status == pgruntime.SharedRunStarted {
		if !r.animated {
			fmt.Fprintf(r.w, "[%d/%d] %s - In progress\n", progress.Index, progress.Total, filepath.Base(progress.TaskPath))
			return
		}
		r.mu.Lock()
		r.current = progress
		r.frame = 0
		r.spinnerStop = make(chan struct{})
		r.spinnerDone = make(chan struct{})
		stop := r.spinnerStop
		done := r.spinnerDone
		r.renderSpinnerLocked()
		r.mu.Unlock()
		go r.spin(stop, done)
		return
	}

	name := filepath.Base(progress.TaskPath)
	label := compactProgressLabel(progress)
	if r.animated {
		prefix := colorizeStatusIcon("✓", "succeeded", r.color)
		if progress.Status == pgruntime.SharedRunFailed {
			prefix = colorizeStatusIcon("✗", "failed", r.color)
		}
		fmt.Fprintf(r.w, "\r\033[2K%s [%d/%d] %s|%s\n", prefix, progress.Index, progress.Total, name, label)
		return
	}
	prefix := "✓"
	if progress.Status == pgruntime.SharedRunFailed {
		prefix = "✗"
	}
	fmt.Fprintf(r.w, "%s [%d/%d] %s|%s\n", prefix, progress.Index, progress.Total, name, label)
}

func colorizeStatusIcon(icon, status, successColor string) string {
	color := ""
	switch status {
	case "active", "running", "succeeded", "completed":
		color = successColor
	case "failed":
		color = "\033[31m"
	}
	if color == "" {
		return icon
	}
	return color + icon + "\033[0m"
}

func compactProgressLabel(progress pgruntime.SharedRunProgress) string {
	scope, engine, model := progress.Scope, progress.Engine, progress.Model
	if scope == "" {
		scope = "unscoped"
	}
	if engine == "" {
		engine = "unknown-engine"
	}
	if model == "" {
		model = "default"
	}
	return scope + "|" + engine + "/" + model + "|" + formatDuration(progress.Duration)
}

func formatDuration(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	totalSeconds := int64(duration.Round(time.Second) / time.Second)
	hours := totalSeconds / 3600
	minutes := totalSeconds % 3600 / 60
	seconds := totalSeconds % 60
	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}

func (r *SharedProgressRenderer) Close() {
	r.stopSpinner()
}

func (r *SharedProgressRenderer) spin(stop <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer close(done)
	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			r.frame++
			r.renderSpinnerLocked()
			r.mu.Unlock()
		case <-stop:
			return
		}
	}
}

func (r *SharedProgressRenderer) renderSpinnerLocked() {
	p := r.current
	fmt.Fprintf(r.w, "\r\033[2K%s [%d/%d] %s - In progress", spinnerFrames[r.frame%len(spinnerFrames)], p.Index, p.Total, filepath.Base(p.TaskPath))
}

func (r *SharedProgressRenderer) stopSpinner() {
	r.mu.Lock()
	stop := r.spinnerStop
	done := r.spinnerDone
	r.spinnerStop = nil
	r.spinnerDone = nil
	r.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}

func NormalizeTheme(value string) (Theme, error) {
	switch Theme(value) {
	case "", ThemeDefault:
		return ThemeDefault, nil
	case ThemeMinimal:
		return ThemeMinimal, nil
	case ThemeLoud:
		return ThemeLoud, nil
	default:
		return "", fmt.Errorf("unsupported theme %q: use default, minimal, or loud", value)
	}
}

func PlainFromEnv() bool {
	return os.Getenv("NO_COLOR") != "" || os.Getenv("PROMPTGRINDER_PLAIN") != ""
}

func Banner(w io.Writer, opts Options) {
	lines := bannerLines(opts.Theme)
	if opts.Plain {
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
		return
	}
	color := themeColor(opts.Theme)
	for _, line := range lines {
		fmt.Fprintf(w, "%s%s\033[0m\n", color, line)
	}
}

func WorkerLaunchHeader(w io.Writer, header LaunchHeader, opts Options) {
	rows := []string{
		"PromptGrinder " + Version,
		"Prompt: " + filepath.Base(header.TaskPath),
		"Worker: " + header.WorkerID,
		"Repository: " + header.RepositoryPath,
		"Started: " + formatTime(header.StartedAt),
		"Log: " + header.LogPath,
	}
	if opts.Plain {
		for _, row := range rows {
			fmt.Fprintln(w, row)
		}
		return
	}
	writeBox(w, rows, themeColor(opts.Theme))
}

func bannerLines(theme Theme) []string {
	switch theme {
	case ThemeMinimal:
		return []string{"PromptGrinder"}
	case ThemeLoud:
		return []string{
			"██████╗ ██████╗  ██████╗ ███╗   ███╗██████╗ ████████╗ ██████╗ ██████╗ ██╗███╗   ██╗██████╗ ███████╗██████╗",
			"PromptGrinder",
		}
	default:
		return []string{
			"  ____                            _    ____      _           _           ",
			" |  _ \\ _ __ ___  _ __ ___  _ __ | |_ / ___|_ __(_)_ __   __| | ___ _ __ ",
			" | |_) | '__/ _ \\| '_ ` _ \\| '_ \\| __| |  _| '__| | '_ \\ / _` |/ _ \\ '__|",
			" |  __/| | | (_) | | | | | | |_) | |_| |_| | |  | | | | | (_| |  __/ |   ",
			" |_|   |_|  \\___/|_| |_| |_| .__/ \\__|\\____|_|  |_|_| |_|\\__,_|\\___|_|   ",
			"                           |_|                                           ",
		}
	}
}

func themeColor(theme Theme) string {
	switch theme {
	case ThemeMinimal:
		return "\033[36m"
	case ThemeLoud:
		return "\033[95m"
	default:
		return "\033[32m"
	}
}

func writeBox(w io.Writer, rows []string, color string) {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	top := "┌" + strings.Repeat("─", width+2) + "┐"
	bottom := "└" + strings.Repeat("─", width+2) + "┘"
	fmt.Fprintf(w, "%s%s\033[0m\n", color, top)
	for _, row := range rows {
		fmt.Fprintf(w, "%s│\033[0m %-*s %s│\033[0m\n", color, width, row, color)
	}
	fmt.Fprintf(w, "%s%s\033[0m\n", color, bottom)
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
