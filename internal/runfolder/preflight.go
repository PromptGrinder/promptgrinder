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

	"promptgrinder/internal/markdown"
	"promptgrinder/internal/repository"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerpathpolicy"

	"gopkg.in/yaml.v3"
)

type PreflightResult struct {
	Folder      string
	Repository  string
	Inspection  FolderInspection
	SequenceID  string
	ResumeIndex int
	Adoption    *SequenceAdoption
}

// Preflight validates everything that can fail without launching a worker or
// creating sequence state. Detached callers run this synchronously.
func Preflight(folder string, options Options) (PreflightResult, error) {
	if options.RecoveryAttempts < 0 || options.RecoveryAttempts > 3 {
		return PreflightResult{}, fmt.Errorf("recovery attempts must be between 0 and 3")
	}
	if options.Resume && options.Fresh {
		return PreflightResult{}, fmt.Errorf("--resume and --fresh are mutually exclusive")
	}
	if options.Restart && options.NoResume {
		return PreflightResult{}, fmt.Errorf("--restart and --no-resume are mutually exclusive")
	}
	if options.ResumeSequence != "" && (options.Resume || options.Fresh || options.Restart || options.NoResume) {
		return PreflightResult{}, fmt.Errorf("--resume-sequence is mutually exclusive with --resume, --fresh, --restart, and --no-resume")
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
	result := PreflightResult{Folder: absFolder, Repository: repoRoot, Inspection: inspection}
	remaining := inspection.Prompts
	if options.ResumeSequence != "" {
		sequence, adoption, err := newSequenceStore(options.HomeDir).validateExplicitAdoption(absFolder, repoRoot, inspection.Prompts, options.ResumeSequence)
		if err != nil {
			return result, err
		}
		result.SequenceID = sequence.SequenceID
		result.ResumeIndex = len(adoption.RetainedPrompts)
		result.Adoption = adoption
		remaining = inspection.Prompts[result.ResumeIndex:]
	}
	issues := []error{}
	if err := applyRolePolicies(repoRoot, remaining); err != nil {
		issues = append(issues, err)
	}
	if err := validatePrompts(remaining); err != nil {
		issues = append(issues, err)
	}
	if options.CommitEach || options.RequireCleanGit {
		if err := requireCleanBaseline(repoRoot); err != nil {
			issues = append(issues, err)
		}
	}
	if len(issues) > 0 {
		return result, preflightIssues(issues)
	}
	if options.ResumeSequence != "" {
		if err := validateRemainingConfiguration(repoRoot, remaining, options); err != nil {
			return result, fmt.Errorf("run-folder preflight: %w", err)
		}
		return result, nil
	}
	sequence, err := buildSequence(absFolder, repoRoot, inspection.Prompts, options)
	if err != nil {
		return PreflightResult{}, err
	}
	result.SequenceID = sequence.SequenceID
	return result, nil
}

func preflightIssues(issues []error) error {
	if len(issues) == 1 {
		return fmt.Errorf("run-folder preflight: %w", issues[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run-folder preflight found %d independent issues:", len(issues))
	for _, issue := range issues {
		fmt.Fprintf(&b, "\n\n%s", issue)
	}
	return errors.New(b.String())
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

// TaskPolicyPreview describes the role and task boundaries that apply to one
// Markdown task. Both boundaries apply: role scope is an outer limit and task
// scope can only narrow it.
type TaskPolicyPreview struct {
	RoleID              string   `json:"role_id,omitempty"`
	RolePath            string   `json:"role_path,omitempty"`
	RoleAllowedPaths    []string `json:"role_allowed_paths,omitempty"`
	SliceAllowedPaths   []string `json:"slice_allowed_paths,omitempty"`
	SliceForbiddenPaths []string `json:"slice_forbidden_paths,omitempty"`
	ExpectedPaths       []string `json:"expected_paths,omitempty"`
	EffectiveRule       string   `json:"effective_rule"`
}

// InspectTaskPolicy validates and describes the same role/slice path boundary
// that run-folder enforces before workers launch.
func InspectTaskPolicy(repoRoot, taskPath string) (TaskPolicyPreview, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return TaskPolicyPreview{}, err
	}
	task, err := markdown.Parse(string(data))
	if err != nil {
		return TaskPolicyPreview{}, err
	}
	if err := markdown.Validate(task, taskPath); err != nil {
		return TaskPolicyPreview{}, err
	}
	preview := TaskPolicyPreview{
		SliceAllowedPaths:   metadataStrings(task.Metadata, "allowed_paths"),
		SliceForbiddenPaths: metadataStrings(task.Metadata, "forbidden_paths"),
		ExpectedPaths:       metadataStrings(task.Metadata, "expected_paths"),
		EffectiveRule:       "all declared role and slice path rules apply; the slice cannot widen the role boundary",
	}
	roleID, _ := task.Metadata["role"].(string)
	if roleID == "" {
		return preview, nil
	}
	prompts := []Prompt{{Path: taskPath, Name: filepath.Base(taskPath), Role: roleID}}
	if err := applyRolePolicies(repoRoot, prompts); err != nil {
		return TaskPolicyPreview{}, err
	}
	prompt := prompts[0]
	preview.RoleID = roleID
	preview.RolePath = filepath.Join(repoRoot, ".promptgrinder", "roles", roleID+".yaml")
	preview.RoleAllowedPaths = append([]string(nil), prompt.RolePolicy.AllowedPaths...)
	policy := workerdomain.WorkerPolicy{AllowedPaths: preview.SliceAllowedPaths, ForbiddenPaths: preview.SliceForbiddenPaths}
	if err := workerpathpolicy.ValidatePatterns(policy); err != nil {
		return TaskPolicyPreview{}, err
	}
	if len(preview.ExpectedPaths) > 0 {
		violations, err := promptPolicyViolations(prompt, policy, preview.ExpectedPaths)
		if err != nil {
			return TaskPolicyPreview{}, err
		}
		if len(violations) > 0 {
			return TaskPolicyPreview{}, fmt.Errorf("expected_paths are not permitted by the effective role/slice path policy: %s", formatViolations(violations))
		}
	}
	return preview, nil
}

func (r RolePolicy) identity() string {
	return strings.Join([]string{r.ID, r.Description, strings.Join(r.AllowedPaths, "\x00"), strings.Join(r.QualityGates, "\x00"), r.Runtime.Model, r.Runtime.MaxCost, strings.Join(r.Runtime.Capabilities, "\x00")}, "\x00")
}

func applyRolePolicies(repoRoot string, prompts []Prompt) error {
	loaded := map[string]RolePolicy{}
	registered, registryPresent, err := projectRoleRegistry(repoRoot)
	if err != nil {
		return err
	}
	issues := []error{}
	for index := range prompts {
		prompt := &prompts[index]
		if prompt.Role == "" {
			continue
		}
		if registryPresent {
			if _, ok := registered[prompt.Role]; !ok {
				issues = append(issues, fmt.Errorf("%s declares role %q, but it is not registered in %s; add it under roles or select a registered role", prompt.Name, prompt.Role, filepath.Join(repoRoot, ".promptgrinder", "project.yaml")))
				continue
			}
		}
		if role, ok := loaded[prompt.Role]; ok {
			prompt.RolePolicy = &role
			continue
		}
		path := filepath.Join(repoRoot, ".promptgrinder", "roles", prompt.Role+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				issues = append(issues, fmt.Errorf("%s declares role %q, but %s does not exist", prompt.Name, prompt.Role, path))
				continue
			}
			issues = append(issues, fmt.Errorf("read role %q: %w", prompt.Role, err))
			continue
		}
		var role RolePolicy
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&role); err != nil {
			issues = append(issues, fmt.Errorf("parse role %q: %w", prompt.Role, err))
			continue
		}
		if role.ID != prompt.Role {
			issues = append(issues, fmt.Errorf("role file %s declares id %q, expected %q", path, role.ID, prompt.Role))
			continue
		}
		if err := validateRoleAllowedPaths(role.AllowedPaths); err != nil {
			issues = append(issues, fmt.Errorf("role file %s: %w", path, err))
			continue
		}
		role.AllowedPaths = normalizeRoleAllowedPaths(repoRoot, role.AllowedPaths)
		if err := workerpathpolicy.ValidatePatterns(workerdomain.WorkerPolicy{AllowedPaths: role.AllowedPaths}); err != nil {
			issues = append(issues, fmt.Errorf("role file %s: %w", path, err))
			continue
		}
		loaded[prompt.Role] = role
		prompt.RolePolicy = &role
	}
	return joinIssues(issues)
}

type projectRoleManifest struct {
	Roles []string `yaml:"roles"`
}

func projectRoleRegistry(repoRoot string) (map[string]struct{}, bool, error) {
	path := filepath.Join(repoRoot, ".promptgrinder", "project.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read project role registry %s: %w", path, err)
	}
	var manifest projectRoleManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, false, fmt.Errorf("parse project role registry %s: %w", path, err)
	}
	registered := make(map[string]struct{}, len(manifest.Roles))
	for _, role := range manifest.Roles {
		if role = strings.TrimSpace(role); role != "" {
			registered[role] = struct{}{}
		}
	}
	return registered, true, nil
}

func joinIssues(issues []error) error {
	if len(issues) == 0 {
		return nil
	}
	if len(issues) == 1 {
		return issues[0]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d role configuration issues:", len(issues))
	for _, issue := range issues {
		fmt.Fprintf(&b, "\n- %s", issue)
	}
	return errors.New(b.String())
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
