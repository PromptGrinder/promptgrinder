package repository

import (
	"os"
	"path/filepath"
)

func DetectRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	start := abs
	if !info.IsDir() {
		start = filepath.Dir(abs)
	}

	current := start
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return start, nil
		}
		current = parent
	}
}

// IsGitRoot reports whether path is a Git worktree root (or has Git metadata
// supplied by a worktree .git file). It is intentionally small so callers can
// distinguish an inferred task directory from a repository-backed execution.
func IsGitRoot(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}
