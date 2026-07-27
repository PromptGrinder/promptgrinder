package markdown

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Task struct {
	Metadata map[string]any
	Body     string
}

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
		Metadata: metadata,
		Body:     strings.Join(lines[close+1:], ""),
	}, nil
}

func parseFrontmatter(text string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return out, nil
	}
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("invalid Markdown frontmatter: %w", err)
	}
	return out, nil
}
