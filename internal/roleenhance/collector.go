package roleenhance

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxFiles      = 128
	defaultMaxFileBytes  = 32 * 1024
	defaultMaxTotalBytes = 256 * 1024
)

type CollectorLimits struct {
	MaxFiles      int
	MaxFileBytes  int
	MaxTotalBytes int
}

func (l CollectorLimits) normalized() CollectorLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = defaultMaxFiles
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = defaultMaxFileBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = defaultMaxTotalBytes
	}
	return l
}

type RepositoryContextCollector struct{ Limits CollectorLimits }
type DocumentationCollector struct{ Limits CollectorLimits }
type SkillCollector struct{ Limits CollectorLimits }

func (c RepositoryContextCollector) Collect(root string) (Evidence, error) {
	paths, dirs, err := repositoryCandidates(root)
	if err != nil {
		return Evidence{}, err
	}
	facts := make([]EvidenceFact, 0, len(paths)+len(dirs))
	for _, d := range dirs {
		facts = append(facts, EvidenceFact{Kind: EvidenceRepository, Path: d, Key: "repository_directory", Value: d})
	}
	for _, p := range paths {
		facts = append(facts, EvidenceFact{Kind: EvidenceRepository, Path: p, Key: "repository_input", Value: classifyRepositoryFile(p)})
	}
	return collectFiles(root, EvidenceRepository, paths, facts, c.Limits)
}
func (c DocumentationCollector) Collect(root string) (Evidence, error) {
	paths, err := walkSelected(root, documentationPath)
	if err != nil {
		return Evidence{}, err
	}
	facts := make([]EvidenceFact, 0, len(paths))
	for _, p := range paths {
		facts = append(facts, EvidenceFact{Kind: EvidenceDocumentation, Path: p, Key: "documentation", Value: "present"})
	}
	return collectFiles(root, EvidenceDocumentation, paths, facts, c.Limits)
}
func (c SkillCollector) Collect(root string) (Evidence, error) {
	paths, err := walkSelected(root, skillPath)
	if err != nil {
		return Evidence{}, err
	}
	facts := make([]EvidenceFact, 0, len(paths))
	for _, p := range paths {
		facts = append(facts, EvidenceFact{Kind: EvidenceSkill, Path: p, Key: "skill_input", Value: "present"})
	}
	return collectFiles(root, EvidenceSkill, paths, facts, c.Limits)
}

func repositoryCandidates(root string) ([]string, []string, error) {
	abs, err := validRoot(root)
	if err != nil {
		return nil, nil, err
	}
	var paths, dirs []string
	err = filepath.WalkDir(abs, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			if excludedDir(rel, e.Name()) {
				return filepath.SkipDir
			}
			if !strings.HasPrefix(rel, ".") && strings.Count(rel, "/") < 2 {
				dirs = append(dirs, rel)
			}
			return nil
		}
		if !e.Type().IsRegular() || secretPath(rel) || !repositoryPath(rel) {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan repository: %w", err)
	}
	sort.Strings(paths)
	sort.Strings(dirs)
	return paths, dirs, nil
}
func walkSelected(root string, selectPath func(string) bool) ([]string, error) {
	abs, err := validRoot(root)
	if err != nil {
		return nil, err
	}
	var paths []string
	err = filepath.WalkDir(abs, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			if excludedDir(rel, e.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if e.Type().IsRegular() && selectPath(rel) && !secretPath(rel) {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan repository: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}
func validRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	i, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !i.IsDir() {
		return "", fmt.Errorf("repository root %q is not a directory", root)
	}
	return abs, nil
}

func collectFiles(root string, kind EvidenceKind, paths []string, facts []EvidenceFact, limits CollectorLimits) (Evidence, error) {
	limits = limits.normalized()
	abs, _ := filepath.Abs(root)
	sort.Slice(facts, func(i, j int) bool {
		a, b := facts[i], facts[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Value < b.Value
	})
	if len(facts) > limits.MaxFiles {
		facts = facts[:limits.MaxFiles]
	}
	out := Evidence{Facts: facts}
	for _, rel := range paths {
		if len(out.Sources) >= limits.MaxFiles || out.TotalBytes >= limits.MaxTotalBytes {
			break
		}
		path := filepath.Join(abs, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil {
			return Evidence{}, fmt.Errorf("inspect %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		capBytes := limits.MaxFileBytes
		if remaining := limits.MaxTotalBytes - out.TotalBytes; remaining < capBytes {
			capBytes = remaining
		}
		if capBytes <= 0 {
			break
		}
		b, more, err := readBounded(path, capBytes)
		if err != nil {
			return Evidence{}, fmt.Errorf("read %s: %w", rel, err)
		}
		if binary(b) {
			continue
		}
		text := redactSecretValues(string(b))
		if text == "" {
			continue
		}
		out.Sources = append(out.Sources, EvidenceSource{Kind: kind, Path: rel, Excerpt: text, Truncated: more})
		out.TotalBytes += len(text)
	}
	return out, nil
}
func readBounded(path string, n int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b := make([]byte, n+1)
	read, err := f.Read(b)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return b[:min(read, n)], read > n, nil
}
func binary(b []byte) bool { return bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) }

func excludedDir(rel, name string) bool {
	lower := strings.ToLower(name)
	if secretPath(name) {
		return true
	}
	if rel == ".promptgrinder/context" || rel == ".promptgrinder/skills" || rel == "skills" || rel == ".ai" || rel == "docs" || rel == ".github" {
		return false
	}
	switch lower {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", "coverage", ".cache", "tmp", "temp", "bin", "obj", "runs", "state":
		return true
	}
	return false
}
func repositoryPath(p string) bool {
	l := strings.ToLower(p)
	base := strings.ToLower(filepath.Base(l))
	if strings.HasPrefix(l, ".github/workflows/") {
		return strings.HasSuffix(l, ".yml") || strings.HasSuffix(l, ".yaml")
	}
	if strings.Count(l, "/") == 0 {
		switch base {
		case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "makefile", "justfile", "cargo.toml", "pyproject.toml", "requirements.txt", "dockerfile", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
			return true
		}
	}
	return strings.HasSuffix(l, "/pom.xml") || strings.HasSuffix(l, "/build.gradle") || strings.HasSuffix(l, "/build.gradle.kts") || strings.HasSuffix(l, "/package.json")
}
func documentationPath(p string) bool {
	l := strings.ToLower(p)
	base := strings.ToLower(filepath.Base(l))
	if strings.HasPrefix(l, "docs/") {
		return textExtension(l)
	}
	if strings.Contains(l, "/") {
		return false
	}
	for _, prefix := range []string{"readme", "contributing", "contribution", "architecture", "testing", "coding", "development", "security"} {
		if base == prefix || strings.HasPrefix(base, prefix+".") {
			return textExtension(l)
		}
	}
	return false
}
func skillPath(p string) bool {
	l := strings.ToLower(p)
	return (strings.HasPrefix(l, ".promptgrinder/context/") || strings.HasPrefix(l, ".promptgrinder/skills/") || strings.HasPrefix(l, "skills/") || strings.HasPrefix(l, ".ai/")) && textExtension(l)
}
func textExtension(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case "", ".md", ".txt", ".yaml", ".yml", ".json", ".toml", ".ini", ".cfg", ".conf":
		return true
	}
	return false
}
func secretPath(p string) bool {
	l := strings.ToLower(filepath.Base(p))
	if l == ".env" || strings.HasPrefix(l, ".env.") || strings.HasSuffix(l, ".pem") || strings.HasSuffix(l, ".key") || strings.HasSuffix(l, ".p12") || strings.HasSuffix(l, ".pfx") || strings.Contains(l, "credential") || strings.Contains(l, "secret") || strings.Contains(l, "token") {
		return true
	}
	return false
}
func redactSecretValues(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		lower := strings.ToLower(line)
		sensitive := false
		for _, key := range []string{"password", "passwd", "api_key", "apikey", "access_token", "auth_token", "client_secret", "private_key"} {
			if strings.Contains(lower, key) {
				sensitive = true
				break
			}
		}
		if !sensitive {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
func classifyRepositoryFile(p string) string {
	if strings.HasPrefix(strings.ToLower(p), ".github/workflows/") {
		return "ci"
	}
	return "build_configuration"
}
