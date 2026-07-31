// Package workerpathpolicy attributes Git changes to a named-worker launch and
// enforces its repository path policy without modifying the working tree.
package workerpathpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"promptgrinder/internal/workerdomain"
)

type Snapshot struct {
	Version   int               `json:"version"`
	Head      string            `json:"head"`
	Entries   map[string]string `json:"entries"`
	CreatedAt time.Time         `json:"created_at"`
}

type Violation struct {
	Path   string `json:"path"`
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason"`
}

type Event struct {
	SchemaVersion int         `json:"schema_version"`
	Timestamp     time.Time   `json:"timestamp"`
	Type          string      `json:"type"`
	Severity      string      `json:"severity"`
	ProjectID     string      `json:"project_id"`
	WorkerID      string      `json:"worker_id"`
	TaskID        string      `json:"task_id"`
	RunID         string      `json:"run_id,omitempty"`
	Checkpoint    string      `json:"checkpoint"`
	Violations    []Violation `json:"violations"`
	Message       string      `json:"message"`
}

func Validate(policy workerdomain.WorkerPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	for _, group := range [][]string{policy.AllowedPaths, policy.ForbiddenPaths} {
		for _, pattern := range group {
			if _, err := match(pattern, "validation/path"); err != nil {
				return fmt.Errorf("invalid path policy pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

func Capture(repo string) (Snapshot, error) {
	head, err := gitBytes(repo, "rev-parse", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot Git HEAD: %w", err)
	}
	raw, err := gitBytes(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot Git status: %w", err)
	}
	paths, err := statusPaths(raw)
	if err != nil {
		return Snapshot{}, err
	}
	entries := make(map[string]string, len(paths))
	for _, name := range paths {
		entries[name], err = identity(repo, name)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{Version: 1, Head: strings.TrimSpace(string(head)), Entries: entries, CreatedAt: time.Now().UTC()}, nil
}

// AttributedChanges excludes unchanged dirty baseline paths, but includes any
// further edits to them and changes committed after the snapshot.
func AttributedChanges(repo string, before Snapshot) ([]string, error) {
	after, err := Capture(repo)
	if err != nil {
		return nil, err
	}
	changed := map[string]struct{}{}
	for name, current := range after.Entries {
		if previous, existed := before.Entries[name]; !existed || previous != current {
			changed[name] = struct{}{}
		}
	}
	for name := range before.Entries {
		if _, exists := after.Entries[name]; !exists {
			changed[name] = struct{}{}
		}
	}
	if after.Head != before.Head {
		raw, diffErr := gitBytes(repo, "diff", "--name-status", "-z", "--find-renames", before.Head, after.Head)
		if diffErr != nil {
			return nil, fmt.Errorf("attribute committed Git changes: %w", diffErr)
		}
		for _, name := range nameStatusPaths(raw) {
			changed[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(changed))
	for name := range changed {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func Violations(policy workerdomain.WorkerPolicy, paths []string) ([]Violation, error) {
	var violations []Violation
	for _, name := range paths {
		for _, pattern := range policy.ForbiddenPaths {
			ok, err := match(pattern, name)
			if err != nil {
				return nil, err
			}
			if ok {
				violations = append(violations, Violation{Path: name, Rule: pattern, Reason: "forbidden path"})
				goto next
			}
		}
		if len(policy.AllowedPaths) > 0 {
			allowed := false
			for _, pattern := range policy.AllowedPaths {
				ok, err := match(pattern, name)
				if err != nil {
					return nil, err
				}
				allowed = allowed || ok
			}
			if !allowed {
				violations = append(violations, Violation{Path: name, Reason: "outside allowed paths"})
			}
		}
	next:
	}
	return violations, nil
}

func SaveSnapshot(home, projectID, workerID string, snapshot Snapshot) error {
	dir := filepath.Join(home, "projects", projectID, "workers", workerID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".path-policy-snapshot-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "path-policy-snapshot.json"))
}

func LoadSnapshot(home, projectID, workerID string) (Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(home, "projects", projectID, "workers", workerID, "path-policy-snapshot.json"))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != 1 || snapshot.Head == "" || snapshot.Entries == nil {
		return Snapshot{}, fmt.Errorf("invalid path-policy snapshot")
	}
	return snapshot, nil
}

func AppendViolationEvent(home string, event Event) error {
	event.SchemaVersion = 1
	event.Timestamp = time.Now().UTC()
	event.Type = "worker.path_policy_violated"
	event.Severity = "error"
	dir := filepath.Join(home, "projects", event.ProjectID, "workers", event.WorkerID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func statusPaths(raw []byte) ([]string, error) {
	fields := strings.Split(string(raw), "\x00")
	var result []string
	for i := 0; i < len(fields) && fields[i] != ""; i++ {
		field := fields[i]
		if len(field) < 4 {
			return nil, fmt.Errorf("parse Git status entry %q", field)
		}
		code, name := field[:2], filepath.ToSlash(field[3:])
		result = append(result, name)
		if code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C' {
			i++
			if i < len(fields) && fields[i] != "" {
				result = append(result, filepath.ToSlash(fields[i]))
			}
		}
	}
	return result, nil
}

func nameStatusPaths(raw []byte) []string {
	fields := strings.Split(string(raw), "\x00")
	var result []string
	for i := 0; i < len(fields) && fields[i] != ""; {
		status := fields[i]
		i++
		count := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			count = 2
		}
		for j := 0; j < count && i < len(fields) && fields[i] != ""; j, i = j+1, i+1 {
			result = append(result, filepath.ToSlash(fields[i]))
		}
	}
	return result
}

func identity(repo, name string) (string, error) {
	full := filepath.Join(repo, filepath.FromSlash(name))
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return "deleted", nil
	}
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%v:%o:", info.Mode().Type(), info.Mode().Perm())
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(target))
	} else if info.Mode().IsRegular() {
		data, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func match(pattern, name string) (bool, error) {
	pattern = path.Clean(pattern)
	name = path.Clean(name)
	var walk func([]string, []string) (bool, error)
	walk = func(patternParts, nameParts []string) (bool, error) {
		if len(patternParts) == 0 {
			return len(nameParts) == 0, nil
		}
		if patternParts[0] == "**" {
			if ok, err := walk(patternParts[1:], nameParts); ok || err != nil {
				return ok, err
			}
			if len(nameParts) > 0 {
				return walk(patternParts, nameParts[1:])
			}
			return false, nil
		}
		if len(nameParts) == 0 {
			return false, nil
		}
		ok, err := path.Match(patternParts[0], nameParts[0])
		if err != nil || !ok {
			return false, err
		}
		return walk(patternParts[1:], nameParts[1:])
	}
	return walk(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func gitBytes(repo string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	data, err := command.Output()
	if err != nil {
		return nil, err
	}
	return data, nil
}
