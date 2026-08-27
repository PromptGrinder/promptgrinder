package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type YamlWriter struct{}

func (YamlWriter) Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type ProjectManifestWriter struct{ YAML YamlWriter }

const parallelSliceTemplate = `---
id: <feature>-<lane>
type: implement
role: <registered-role>
depends_on: []
lane: <lowercase-kebab-lane>
priority: 1
context_mode: fresh
working_directory: .
timeout: 45m
engine:
  name: codex
  model: <approved-model>
  sandbox: workspace-write
acceptance_criteria:
  - <observable lane outcome>
allowed_paths:
  - <repository-relative-owned-path>/**
forbidden_paths:
  - <adjacent-path-owned-by-another-lane>/**
validation:
  - <focused-command>
  - git diff --check
---

# <Feature> — <lane> lane

Read the shared specification, applicable project skills, and committed
prerequisite evidence. Own only this lane's stated outcome. Do not change
another lane's paths or run Git commits when the outer train uses
--commit-each.

This template is for:

    promptgrinder run-folder <folder> --parallel-worktrees --fresh \
      --checkpoint --commit-each --require-clean-git --detach=false

Every parallel runnable slice needs a unique lane, positive priority,
context_mode: fresh, and non-empty allowed_paths. Dependencies control launch
eligibility; priority controls coordinator merge order.

STATUS: PASS|PARTIAL|BLOCKED
NEXT_PROMPT_SAFE: yes|no
`

func (w ProjectManifestWriter) Files(manifest ProjectManifest, roles []Role) ([]File, error) {
	if w.YAML == (YamlWriter{}) {
		w.YAML = YamlWriter{}
	}
	content, err := w.YAML.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal project manifest: %w", err)
	}
	files := []File{{Path: ".promptgrinder/project.yaml", Content: content}}
	for _, role := range roles {
		content, err = w.YAML.Marshal(role)
		if err != nil {
			return nil, fmt.Errorf("marshal role %s: %w", role.ID, err)
		}
		files = append(files, File{Path: filepath.ToSlash(filepath.Join(".promptgrinder", "roles", role.ID+".yaml")), Content: content})
	}
	files = append(files, File{Path: ".promptgrinder/templates/parallel-slice-template.pg", Content: []byte(parallelSliceTemplate)})
	return files, nil
}

func WritePlan(root string, plan Plan) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, f := range plan.Files {
		target, err := safeTarget(abs, f.Path)
		if err != nil {
			return err
		}
		if info, statErr := os.Lstat(target); statErr == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to overwrite existing target %q", f.Path)
			}
			existing, readErr := os.ReadFile(target)
			if readErr != nil {
				return fmt.Errorf("inspect target %q: %w", f.Path, readErr)
			}
			if !bytes.Equal(existing, f.Content) {
				return fmt.Errorf("existing discovery target %q differs from the current repository analysis; no files were changed. Reconcile the file manually, or move the existing .promptgrinder directory aside before running promptgrinder discover again", f.Path)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect target %q: %w", f.Path, statErr)
		}
	}
	for _, dir := range []string{".promptgrinder", ".promptgrinder/roles", ".promptgrinder/context", ".promptgrinder/templates"} {
		target, err := safeTarget(abs, dir)
		if err != nil {
			return err
		}
		if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
			return fmt.Errorf("required directory %q is not a directory", dir)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	for _, dir := range []string{".promptgrinder", ".promptgrinder/roles", ".promptgrinder/context", ".promptgrinder/templates"} {
		if err := os.Mkdir(filepath.Join(abs, filepath.FromSlash(dir)), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	for _, f := range plan.Files {
		target, _ := safeTarget(abs, f.Path)
		if _, err := os.Lstat(target); err == nil {
			// The preflight established that an existing file is byte-identical.
			continue
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create %q: %w", f.Path, err)
		}
		if _, err = file.Write(f.Content); err != nil {
			file.Close()
			return fmt.Errorf("write %q: %w", f.Path, err)
		}
		if err = file.Close(); err != nil {
			return fmt.Errorf("close %q: %w", f.Path, err)
		}
	}
	return nil
}
func safeTarget(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || len(clean) > 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("target escapes repository: %q", rel)
	}
	if clean != ".promptgrinder" && !strings.HasPrefix(clean, ".promptgrinder"+string(filepath.Separator)) {
		return "", fmt.Errorf("target outside .promptgrinder: %q", rel)
	}
	return filepath.Join(root, clean), nil
}
