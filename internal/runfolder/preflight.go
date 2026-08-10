package runfolder

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"promptgrinder/internal/repository"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerpathpolicy"

	"gopkg.in/yaml.v3"
)

type PreflightResult struct {
	Folder     string
	Repository string
	Inspection FolderInspection
	SequenceID string
}

// Preflight validates everything that can fail without launching a worker or
// creating sequence state. Detached callers run this synchronously.
func Preflight(folder string, options Options) (PreflightResult, error) {
	if options.Resume && options.Fresh {
		return PreflightResult{}, fmt.Errorf("--resume and --fresh are mutually exclusive")
	}
	if options.Restart && options.NoResume {
		return PreflightResult{}, fmt.Errorf("--restart and --no-resume are mutually exclusive")
	}
	if options.Template == "" {
		options.Template = "codex"
	}
	if options.Template != "codex" {
		return PreflightResult{}, fmt.Errorf("unsupported template %q: use codex", options.Template)
	}
	absFolder, err := filepath.Abs(folder)
	if err != nil {
		return PreflightResult{}, err
	}
	repoPath := options.RepoPath
	if repoPath == "" {
		repoPath = "."
	}
	repoRoot, err := repository.DetectRoot(repoPath)
	if err != nil {
		return PreflightResult{}, err
	}
	inspection, err := Inspect(absFolder)
	if err != nil {
		return PreflightResult{}, err
	}
	if len(inspection.Invalid) > 0 {
		return PreflightResult{}, invalidPromptNamesError(inspection)
	}
	if len(inspection.Prompts) == 0 {
		return PreflightResult{}, fmt.Errorf("no Markdown prompts found in %s", absFolder)
	}
	if err := applyRolePolicies(repoRoot, inspection.Prompts); err != nil {
		return PreflightResult{}, fmt.Errorf("run-folder preflight: %w", err)
	}
	if err := validatePrompts(inspection.Prompts); err != nil {
		return PreflightResult{}, fmt.Errorf("run-folder preflight: %w", err)
	}
	sequence, err := buildSequence(absFolder, repoRoot, inspection.Prompts, options)
	if err != nil {
		return PreflightResult{}, err
	}
	if options.CommitEach || options.RequireCleanGit {
		if err := requireCleanBaseline(repoRoot); err != nil {
			return PreflightResult{}, err
		}
	}
	return PreflightResult{Folder: absFolder, Repository: repoRoot, Inspection: inspection, SequenceID: sequence.SequenceID}, nil
}

type RolePolicy struct {
	ID           string   `yaml:"id"`
	Description  string   `yaml:"description"`
	AllowedPaths []string `yaml:"allowed_paths"`
	QualityGates []string `yaml:"quality_gates"`
	Runtime      struct {
		Model        string   `yaml:"model"`
		MaxCost      string   `yaml:"max_cost"`
		Capabilities []string `yaml:"capabilities"`
	} `yaml:"runtime"`
}

func (r RolePolicy) identity() string {
	return strings.Join([]string{r.ID, r.Description, strings.Join(r.AllowedPaths, "\x00"), strings.Join(r.QualityGates, "\x00"), r.Runtime.Model, r.Runtime.MaxCost, strings.Join(r.Runtime.Capabilities, "\x00")}, "\x00")
}

func applyRolePolicies(repoRoot string, prompts []Prompt) error {
	loaded := map[string]RolePolicy{}
	for index := range prompts {
		prompt := &prompts[index]
		if prompt.Role == "" {
			continue
		}
		if role, ok := loaded[prompt.Role]; ok {
			prompt.RolePolicy = &role
			continue
		}
		path := filepath.Join(repoRoot, ".promptgrinder", "roles", prompt.Role+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s declares role %q, but %s does not exist", prompt.Name, prompt.Role, path)
			}
			return fmt.Errorf("read role %q: %w", prompt.Role, err)
		}
		var role RolePolicy
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&role); err != nil {
			return fmt.Errorf("parse role %q: %w", prompt.Role, err)
		}
		if role.ID != prompt.Role {
			return fmt.Errorf("role file %s declares id %q, expected %q", path, role.ID, prompt.Role)
		}
		if err := validateRoleAllowedPaths(role.AllowedPaths); err != nil {
			return fmt.Errorf("role file %s: %w", path, err)
		}
		role.AllowedPaths = normalizeRoleAllowedPaths(repoRoot, role.AllowedPaths)
		if err := workerpathpolicy.ValidatePatterns(workerdomain.WorkerPolicy{AllowedPaths: role.AllowedPaths}); err != nil {
			return fmt.Errorf("role file %s: %w", path, err)
		}
		loaded[prompt.Role] = role
		prompt.RolePolicy = &role
	}
	return nil
}

func validateRoleAllowedPaths(patterns []string) error {
	for _, pattern := range patterns {
		clean := path.Clean(pattern)
		if strings.Contains(pattern, "\\") || path.IsAbs(pattern) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("allowed_paths pattern %q must be repository-relative and must not escape the repository", pattern)
		}
	}
	return nil
}

func normalizeRoleAllowedPaths(repoRoot string, patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		normalized := pattern
		if !strings.ContainsAny(pattern, "*?[") {
			if info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(pattern))); err == nil && info.IsDir() {
				normalized = strings.TrimSuffix(pattern, "/") + "/**"
			}
		}
		out = append(out, normalized)
	}
	return out
}

type dirtyBaseline struct {
	Modified   []string
	Added      []string
	Deleted    []string
	Untracked  []string
	Conflicted []string
}

func requireCleanBaseline(repoRoot string) error {
	dirty, err := inspectDirtyBaseline(repoRoot)
	if err != nil {
		return err
	}
	if len(dirty.Modified)+len(dirty.Added)+len(dirty.Deleted)+len(dirty.Untracked)+len(dirty.Conflicted) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Cannot use --commit-each or --require-clean-git with an unclean baseline.\n")
	writePaths := func(label string, paths []string) {
		if len(paths) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", label)
		for _, path := range paths {
			fmt.Fprintf(&b, "  %s\n", path)
		}
	}
	writePaths("Modified", dirty.Modified)
	writePaths("Added", dirty.Added)
	writePaths("Deleted", dirty.Deleted)
	writePaths("Untracked", dirty.Untracked)
	writePaths("Conflicted", dirty.Conflicted)
	b.WriteString("\nCommit, stash, or isolate these changes, then retry.")
	return errors.New(b.String())
}

func inspectDirtyBaseline(repoRoot string) (dirtyBaseline, error) {
	out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return dirtyBaseline{}, fmt.Errorf("git status failed: %w", err)
	}
	var result dirtyBaseline
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		field := string(fields[i])
		if len(field) < 4 {
			continue
		}
		status, name := field[:2], filepath.ToSlash(field[3:])
		if legacyRunStatePath(name) {
			continue
		}
		if status[0] == 'R' || status[0] == 'C' {
			if i+1 < len(fields) {
				i++
				name = filepath.ToSlash(string(fields[i]))
			}
		}
		switch {
		case status == "??":
			result.Untracked = append(result.Untracked, name)
		case strings.Contains(status, "U"):
			result.Conflicted = append(result.Conflicted, name)
		case strings.Contains(status, "D"):
			result.Deleted = append(result.Deleted, name)
		case strings.ContainsAny(status, "ARCT"):
			result.Added = append(result.Added, name)
		default:
			result.Modified = append(result.Modified, name)
		}
	}
	for _, paths := range []*[]string{&result.Modified, &result.Added, &result.Deleted, &result.Untracked, &result.Conflicted} {
		sort.Strings(*paths)
	}
	return result, nil
}

func legacyRunStatePath(name string) bool {
	name = filepath.ToSlash(strings.TrimSpace(name))
	const rootMarker = ".promptgrinder/"
	index := strings.Index(name, rootMarker)
	if index < 0 || (index > 0 && name[index-1] != '/') {
		return false
	}
	relative := name[index+len(rootMarker):]
	return relative == "run.json" || relative == "summary.md" || strings.HasPrefix(relative, "prompts/")
}
