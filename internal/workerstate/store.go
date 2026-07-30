// Package workerstate persists PromptGrinder-authoritative named-worker state.
// It is deliberately independent of anonymous execution-run storage.
package workerstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"promptgrinder/internal/workerdomain"
)

var (
	ErrNotFound         = errors.New("named-worker state not found")
	ErrRevisionConflict = errors.New("named-worker state revision conflict")
	ErrUnsafeReset      = errors.New("named-worker state cannot be safely reset")
)

type Store struct {
	home string
	now  func() time.Time
}

func New(home string) *Store {
	return &Store{home: home, now: time.Now}
}

func (s *Store) Path(projectID, workerID string) string {
	return filepath.Join(s.home, "projects", projectID, "workers", workerID, "state.json")
}

func (s *Store) Load(projectID, workerID string) (workerdomain.WorkerState, error) {
	if err := validateKeys(projectID, workerID); err != nil {
		return workerdomain.WorkerState{}, err
	}
	path := s.Path(projectID, workerID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workerdomain.WorkerState{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return workerdomain.WorkerState{}, fmt.Errorf("read named-worker state %s: %w", path, err)
	}
	var state workerdomain.WorkerState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return workerdomain.WorkerState{}, fmt.Errorf("corrupt named-worker state %s: %w", path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return workerdomain.WorkerState{}, fmt.Errorf("corrupt named-worker state %s: %w", path, err)
	}
	if state.ProjectID != projectID || state.WorkerID != workerID {
		return workerdomain.WorkerState{}, fmt.Errorf("corrupt named-worker state %s: identity does not match its path", path)
	}
	if err := state.Validate(); err != nil {
		return workerdomain.WorkerState{}, fmt.Errorf("corrupt named-worker state %s: %w", path, err)
	}
	return state, nil
}

// Ensure creates idle state or reconciles its effective policy. Reconciliation
// is intentionally conservative: allow rules can only be retained, while
// forbidden rules are accumulated.
func (s *Store) Ensure(def workerdomain.WorkerDefinition) (workerdomain.WorkerState, error) {
	current, err := s.Load(def.ProjectID, def.ID)
	if errors.Is(err, ErrNotFound) {
		now := s.now().UTC()
		initial := workerdomain.WorkerState{
			Version: workerdomain.SchemaVersion, Revision: 1,
			ProjectID: def.ProjectID, WorkerID: def.ID,
			Lifecycle: workerdomain.LifecycleIdle,
			CreatedAt: now, UpdatedAt: now, LifecycleChangedAt: now,
			EffectivePolicy: clonePolicy(def.Policy),
		}
		if err := s.create(initial); err != nil {
			if errors.Is(err, os.ErrExist) {
				return s.Ensure(def)
			}
			return workerdomain.WorkerState{}, err
		}
		return initial, nil
	}
	if err != nil {
		return workerdomain.WorkerState{}, err
	}
	reconciled := reconcilePolicy(current.EffectivePolicy, def.Policy)
	if policiesEqual(current.EffectivePolicy, reconciled) {
		return current, nil
	}
	current.EffectivePolicy = reconciled
	return s.Save(current, current.Revision)
}

func (s *Store) Save(state workerdomain.WorkerState, expectedRevision uint64) (workerdomain.WorkerState, error) {
	unlock, err := s.lock(state.ProjectID, state.WorkerID)
	if err != nil {
		return workerdomain.WorkerState{}, err
	}
	defer unlock()
	current, err := s.Load(state.ProjectID, state.WorkerID)
	if err != nil {
		return workerdomain.WorkerState{}, err
	}
	if current.Revision != expectedRevision {
		return workerdomain.WorkerState{}, fmt.Errorf("%w: expected %d, found %d", ErrRevisionConflict, expectedRevision, current.Revision)
	}
	state.Version = workerdomain.SchemaVersion
	state.Revision = current.Revision + 1
	state.CreatedAt = current.CreatedAt
	state.UpdatedAt = s.now().UTC()
	if err := state.Validate(); err != nil {
		return workerdomain.WorkerState{}, err
	}
	if err := s.writeAtomic(state, false); err != nil {
		return workerdomain.WorkerState{}, err
	}
	return state, nil
}

func (s *Store) Transition(state workerdomain.WorkerState, to workerdomain.Lifecycle, reason string) (workerdomain.WorkerState, error) {
	if err := workerdomain.ValidateTransition(state.Lifecycle, to); err != nil {
		return workerdomain.WorkerState{}, err
	}
	state.Lifecycle = to
	state.LifecycleChangedAt = s.now().UTC()
	state.FailureReason = ""
	state.BlockReason = ""
	if to == workerdomain.LifecycleFailed {
		state.FailureReason = reason
	}
	if to == workerdomain.LifecycleBlocked {
		state.BlockReason = reason
	}
	return s.Save(state, state.Revision)
}

func (s *Store) Reset(state workerdomain.WorkerState) (workerdomain.WorkerState, error) {
	if state.Lifecycle == workerdomain.LifecycleIdle {
		return state, nil
	}
	if state.Lifecycle != workerdomain.LifecycleFailed && state.Lifecycle != workerdomain.LifecycleAwaitingReview {
		return workerdomain.WorkerState{}, fmt.Errorf("%w from %q", ErrUnsafeReset, state.Lifecycle)
	}
	state.ActiveTaskID = ""
	state.ActiveRunID = ""
	state.RuntimeSession = nil
	return s.Transition(state, workerdomain.LifecycleIdle, "")
}

func (s *Store) create(state workerdomain.WorkerState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	return s.writeAtomic(state, true)
}

func (s *Store) writeAtomic(state workerdomain.WorkerState, exclusive bool) error {
	path := s.Path(state.ProjectID, state.WorkerID)
	dir := filepath.Dir(path)
	if err := s.secureDirectories(state.ProjectID, state.WorkerID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary named-worker state: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if exclusive {
		linkErr := os.Link(tempPath, path)
		if linkErr != nil {
			return linkErr
		}
		if err := os.Remove(tempPath); err != nil {
			return err
		}
	} else if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirHandle.Close()
	return dirHandle.Sync()
}

func (s *Store) secureDirectories(projectID, workerID string) error {
	dirs := []string{
		s.home,
		filepath.Join(s.home, "projects"),
		filepath.Join(s.home, "projects", projectID),
		filepath.Join(s.home, "projects", projectID, "workers"),
		filepath.Join(s.home, "projects", projectID, "workers", workerID),
	}
	for index, dir := range dirs {
		var err error
		if index == 0 {
			err = os.MkdirAll(dir, 0o700)
		} else {
			err = os.Mkdir(dir, 0o700)
		}
		if err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create named-worker state directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure named-worker state directory %s: %w", dir, err)
		}
	}
	return nil
}

func (s *Store) lock(projectID, workerID string) (func(), error) {
	if err := s.secureDirectories(projectID, workerID); err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(s.Path(projectID, workerID)), ".state.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open named-worker state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("secure named-worker state lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock named-worker state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func validateKeys(projectID, workerID string) error {
	if err := workerdomain.ValidateSlug("project id", projectID); err != nil {
		return err
	}
	return workerdomain.ValidateSlug("worker id", workerID)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func clonePolicy(policy workerdomain.WorkerPolicy) workerdomain.WorkerPolicy {
	policy.AllowedPaths = slices.Clone(policy.AllowedPaths)
	policy.ForbiddenPaths = slices.Clone(policy.ForbiddenPaths)
	return policy
}

func reconcilePolicy(old, current workerdomain.WorkerPolicy) workerdomain.WorkerPolicy {
	result := clonePolicy(current)
	result.AllowedPaths = intersection(old.AllowedPaths, current.AllowedPaths)
	result.ForbiddenPaths = union(old.ForbiddenPaths, current.ForbiddenPaths)
	// Working-location constraints are also permission-relevant. Preserve the
	// existing values when a definition changes rather than silently moving.
	result.BranchPrefix = old.BranchPrefix
	result.DefaultWorktree = old.DefaultWorktree
	return result
}

func intersection(a, b []string) []string {
	var result []string
	for _, value := range a {
		if slices.Contains(b, value) && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func union(a, b []string) []string {
	result := slices.Clone(a)
	for _, value := range b {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func policiesEqual(a, b workerdomain.WorkerPolicy) bool {
	return a.BranchPrefix == b.BranchPrefix &&
		a.DefaultWorktree == b.DefaultWorktree &&
		slices.Equal(a.AllowedPaths, b.AllowedPaths) &&
		slices.Equal(a.ForbiddenPaths, b.ForbiddenPaths)
}
