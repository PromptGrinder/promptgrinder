package scheduler

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DiscoverMarkdown(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if isMarkdown(root) && !isHidden(filepath.Base(root)) {
			abs, err := filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			return []string{abs}, nil
		}
		return nil, nil
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	files := []string{}
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if path != absRoot && isHidden(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if isMarkdown(name) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(files, func(i, j int) bool {
		left, right := filepath.Base(files[i]), filepath.Base(files[j])
		if left == right {
			return files[i] < files[j]
		}
		return left < right
	})
	return files, nil
}

func isMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}
