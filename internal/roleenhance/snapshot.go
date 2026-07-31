package roleenhance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadCurrentState(root string) (CurrentState, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return CurrentState{}, err
	}
	projectPath := filepath.Join(abs, ".promptgrinder", "project.yaml")
	b, err := readRegular(projectPath)
	if err != nil {
		return CurrentState{}, fmt.Errorf("read .promptgrinder/project.yaml: %w", err)
	}
	var project ProjectSnapshot
	if err := yaml.Unmarshal(b, &project); err != nil {
		return CurrentState{}, fmt.Errorf("parse .promptgrinder/project.yaml: %w", err)
	}
	if strings.TrimSpace(project.Name) == "" {
		return CurrentState{}, fmt.Errorf("parse .promptgrinder/project.yaml: name is required")
	}
	project.SourcePath = ".promptgrinder/project.yaml"
	project.Raw = append([]byte(nil), b...)
	ids := append([]string(nil), project.Roles...)
	sort.Strings(ids)
	roles := make([]RoleSnapshot, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
			return CurrentState{}, fmt.Errorf("invalid role id %q", id)
		}
		if seen[id] {
			return CurrentState{}, fmt.Errorf("duplicate role id %q", id)
		}
		seen[id] = true
		rel := filepath.ToSlash(filepath.Join(".promptgrinder", "roles", id+".yaml"))
		rb, err := readRegular(filepath.Join(abs, filepath.FromSlash(rel)))
		if err != nil {
			return CurrentState{}, fmt.Errorf("read %s: %w", rel, err)
		}
		var role RoleSnapshot
		if err := yaml.Unmarshal(rb, &role); err != nil {
			return CurrentState{}, fmt.Errorf("parse %s: %w", rel, err)
		}
		if role.ID != id {
			return CurrentState{}, fmt.Errorf("parse %s: role id %q does not match project role %q", rel, role.ID, id)
		}
		role.SourcePath = rel
		role.Raw = append([]byte(nil), rb...)
		roles = append(roles, role)
	}
	return CurrentState{Project: project, Roles: roles}, nil
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	return os.ReadFile(path)
}
