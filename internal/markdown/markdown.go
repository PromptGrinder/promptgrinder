package markdown

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Task struct {
	Metadata       map[string]any
	Body           string
	RawFrontmatter string
}

const FrontmatterContractVersion = 1

var (
	topLevelKeys  = map[string]bool{"engine": true, "working_directory": true, "timeout": true, "labels": true, "env": true, "sandbox": true, "approval": true, "web_search": true, "images": true, "acceptance_criteria": true, "allowed_paths": true, "forbidden_paths": true, "validation": true}
	engineKeys    = map[string]bool{"name": true, "model": true, "profile": true, "sandbox": true, "approval": true, "web_search": true, "images": true}
	secretPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(sk-[a-z0-9_-]{8,}|(?:api[_ -]?key|[a-z0-9_]*(?:token|password|secret))\s*[:=]\s*\S+))`)
)

func Parse(text string) (Task, error) {
	if !strings.HasPrefix(text, "---") {
		return Task{Metadata: map[string]any{}, Body: text}, nil
	}

	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Task{Metadata: map[string]any{}, Body: text}, nil
	}

	close := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			close = i
			break
		}
	}
	if close == -1 {
		return Task{}, fmt.Errorf("invalid Markdown frontmatter: missing closing ---")
	}

	metadata, err := parseFrontmatter(strings.Join(lines[1:close], ""))
	if err != nil {
		return Task{}, err
	}
	return Task{
		Metadata:       metadata,
		Body:           strings.Join(lines[close+1:], ""),
		RawFrontmatter: strings.Join(lines[1:close], ""),
	}, nil
}

func parseFrontmatter(text string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return out, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		return nil, fmt.Errorf("invalid Markdown frontmatter: %w", err)
	}
	if err := rejectAmbiguousYAML(&document); err != nil {
		return nil, err
	}
	if err := document.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid Markdown frontmatter: %w", err)
	}
	return out, nil
}

func rejectAmbiguousYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("invalid Markdown frontmatter: YAML aliases and anchors are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return fmt.Errorf("invalid Markdown frontmatter: duplicate key %q", key)
			}
			seen[key] = true
		}
	}
	for _, child := range node.Content {
		if err := rejectAmbiguousYAML(child); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the versioned task-frontmatter contract and names source in every error.
func Validate(task Task, source string) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("task frontmatter %s: %s", source, fmt.Sprintf(format, args...))
	}
	if _, ok := task.Metadata["depends_on"]; ok {
		return fail("depends_on is not supported")
	}
	for _, key := range sortedKeys(task.Metadata) {
		if !topLevelKeys[key] {
			return fail("unknown top-level key %q", key)
		}
	}
	if raw, ok := task.Metadata["engine"].(map[string]any); ok {
		for _, key := range sortedKeys(raw) {
			if !engineKeys[key] {
				return fail("unknown nested key %q", "engine."+key)
			}
		}
	}
	for _, field := range []string{"acceptance_criteria", "validation"} {
		if raw, ok := task.Metadata[field]; ok {
			values, err := stringOrList(raw, field)
			if err != nil {
				return fail("%v", err)
			}
			for _, value := range values {
				if secretPattern.MatchString(value) {
					return fail("%s contains secret-looking data", field)
				}
			}
		}
	}
	allowedRaw, allowedPresent := task.Metadata["allowed_paths"]
	allowed, err := pathList(allowedRaw, "allowed_paths", true, allowedPresent)
	if err != nil {
		return fail("%v", err)
	}
	forbiddenRaw, forbiddenPresent := task.Metadata["forbidden_paths"]
	forbidden, err := pathList(forbiddenRaw, "forbidden_paths", false, forbiddenPresent)
	if err != nil {
		return fail("%v", err)
	}
	for _, item := range append(append([]string{}, allowed...), forbidden...) {
		if secretPattern.MatchString(item) {
			return fail("path semantics contain secret-looking data")
		}
	}
	for _, a := range allowed {
		for _, f := range forbidden {
			if a == f {
				return fail("conflicting path rule %q appears in allowed_paths and forbidden_paths", a)
			}
		}
	}
	return nil
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringOrList(raw any, field string) ([]string, error) {
	if value, ok := raw.(string); ok {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s must not be empty", field)
		}
		return []string{value}, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%s must be a string or nonempty list of strings", field)
	}
	values := make([]string, len(items))
	for i, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s must contain only nonempty strings", field)
		}
		values[i] = value
	}
	return values, nil
}

func pathList(raw any, field string, requiredNonempty, present bool) ([]string, error) {
	if !present {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of repository-relative patterns", field)
	}
	if requiredNonempty && len(items) == 0 {
		return nil, fmt.Errorf("%s must be a nonempty list of repository-relative patterns", field)
	}
	values := make([]string, len(items))
	for i, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s must contain only nonempty strings", field)
		}
		if strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) == ".." || strings.HasPrefix(path.Clean(value), "../") {
			return nil, fmt.Errorf("%s pattern %q must be repository-relative and must not escape the repository", field, value)
		}
		values[i] = value
	}
	return values, nil
}

// Render returns the exact AI instruction bytes: a deterministic semantic preamble followed by the untouched body.
func Render(task Task) []byte {
	var b bytes.Buffer
	sections := []struct{ key, heading string }{{"acceptance_criteria", "Acceptance Criteria"}, {"allowed_paths", "Allowed Paths"}, {"forbidden_paths", "Forbidden Paths"}, {"validation", "Validation"}}
	started := false
	for _, section := range sections {
		raw, ok := task.Metadata[section.key]
		if !ok {
			continue
		}
		var values []string
		if value, ok := raw.(string); ok {
			values = []string{value}
		} else {
			for _, item := range raw.([]any) {
				values = append(values, item.(string))
			}
		}
		if !started {
			fmt.Fprintf(&b, "# Task Semantics (v%d)\n", FrontmatterContractVersion)
			started = true
		}
		fmt.Fprintf(&b, "\n## %s\n\n", section.heading)
		for _, value := range values {
			fmt.Fprintf(&b, "- %s\n", value)
		}
	}
	if started {
		b.WriteByte('\n')
	}
	b.WriteString(task.Body)
	return b.Bytes()
}

// Warnings returns deterministic, non-fatal contract warnings.
func Warnings(task Task) []string {
	payload := len(Render(task))
	raw := len(task.RawFrontmatter)
	if raw >= 2048 && payload <= 256 && raw >= payload*8 {
		return []string{fmt.Sprintf("frontmatter is %d bytes while rendered task instructions are %d bytes; review oversized metadata", raw, payload)}
	}
	return []string{}
}
