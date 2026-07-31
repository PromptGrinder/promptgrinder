// Package taskstore persists immutable task snapshots and coordinates their
// assignment with PromptGrinder-owned named-worker state.
package taskstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"promptgrinder/internal/markdown"
	"promptgrinder/internal/taskqueue"
	"promptgrinder/internal/workerdomain"
	"promptgrinder/internal/workerstate"
)

var (
	ErrNotFound         = errors.New("task not found")
	ErrActiveAssignment = errors.New("worker already has an active task")
	ErrTaskExists       = errors.New("task already exists")
)

type Store struct {
	home        string
	now         func() time.Time
	workerState *workerstate.Store
}

func New(home string) *Store {
	return &Store{home: home, now: time.Now, workerState: workerstate.New(home)}
}

func (s *Store) Path(projectID, taskID string) string {
	return filepath.Join(s.home, "projects", projectID, "tasks", taskID+".json")
}

func (s *Store) Assign(root string, definition workerdomain.WorkerDefinition, source string) (workerdomain.Task, error) {
	return s.enqueue(root, definition, source, true)
}

// Enqueue always appends a task to the worker's pending FIFO.
func (s *Store) Enqueue(root string, definition workerdomain.WorkerDefinition, source string) (workerdomain.Task, error) {
	return s.enqueue(root, definition, source, false)
}

func (s *Store) enqueue(root string, definition workerdomain.WorkerDefinition, source string, assignIfIdle bool) (workerdomain.Task, error) {
	if err := definition.Validate(); err != nil {
		return workerdomain.Task{}, fmt.Errorf("invalid worker definition: %w", err)
	}
	sourceReference, content, err := readSource(root, source)
	if err != nil {
		return workerdomain.Task{}, err
	}
	parsed, err := markdown.Parse(content)
	if err != nil {
		return workerdomain.Task{}, fmt.Errorf("parse task source %s: %w", sourceReference, err)
	}
	taskID := strings.TrimSuffix(filepath.Base(sourceReference), filepath.Ext(sourceReference))
	now := s.now().UTC()
	task := workerdomain.Task{
		Version: workerdomain.SchemaVersion, ID: taskID,
		ProjectID: definition.ProjectID, WorkerID: definition.ID,
		Instructions: parsed.Body, ContentSnapshot: content,
		SourceReference: filepath.ToSlash(sourceReference),
		Status:          workerdomain.TaskStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := task.Validate(); err != nil {
		return workerdomain.Task{}, err
	}

	unlock, err := s.lock(definition.ProjectID, definition.ID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	defer unlock()
	state, err := s.workerState.Ensure(definition)
	if err != nil {
		return workerdomain.Task{}, err
	}
	if state.ProjectID != task.ProjectID || state.WorkerID != task.WorkerID {
		return workerdomain.Task{}, fmt.Errorf("task project/worker identity does not match worker state")
	}
	if assignIfIdle && state.ActiveTaskID == "" {
		task.Status = workerdomain.TaskStatusAssigned
	}
	if err := s.create(task); err != nil {
		return workerdomain.Task{}, err
	}
	if task.Status == workerdomain.TaskStatusPending {
		if _, err := taskqueue.New(s.home).Enqueue(task.ProjectID, task.WorkerID, task.ID); err != nil {
			_ = os.Remove(s.Path(task.ProjectID, task.ID))
			return workerdomain.Task{}, fmt.Errorf("enqueue task: %w", err)
		}
		return task, nil
	}
	state.ActiveTaskID = task.ID
	if _, err := s.workerState.Save(state, state.Revision); err != nil {
		if removeErr := os.Remove(s.Path(task.ProjectID, task.ID)); removeErr != nil {
			return workerdomain.Task{}, fmt.Errorf("update worker assignment: %v; rollback task snapshot: %w", err, removeErr)
		}
		return workerdomain.Task{}, fmt.Errorf("update worker assignment: %w", err)
	}
	return task, nil
}

// SetStatus atomically updates only scheduler-owned task dispatch metadata.
func (s *Store) SetStatus(projectID, taskID string, status workerdomain.TaskStatus) (workerdomain.Task, error) {
	unlock, err := s.lockTask(projectID, taskID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	defer unlock()
	task, err := s.Load(projectID, taskID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	if status != workerdomain.TaskStatusAssigned {
		return workerdomain.Task{}, fmt.Errorf("unsupported scheduler task status %q", status)
	}
	if task.Status != workerdomain.TaskStatusPending {
		return workerdomain.Task{}, fmt.Errorf("task %q is not pending", taskID)
	}
	task.Status = status
	task.UpdatedAt = s.now().UTC()
	if err := s.writeAtomic(task); err != nil {
		return workerdomain.Task{}, err
	}
	return task, nil
}

// UpdateControl atomically persists Slice 9 task control metadata.
func (s *Store) UpdateControl(projectID, taskID string, mutate func(*workerdomain.Task) error) (workerdomain.Task, error) {
	unlock, err := s.lockTask(projectID, taskID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	defer unlock()
	task, err := s.Load(projectID, taskID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	if err := mutate(&task); err != nil {
		return workerdomain.Task{}, err
	}
	task.UpdatedAt = s.now().UTC()
	if err := task.Validate(); err != nil {
		return workerdomain.Task{}, err
	}
	if err := s.writeAtomic(task); err != nil {
		return workerdomain.Task{}, err
	}
	return task, nil
}

func (s *Store) Load(projectID, taskID string) (workerdomain.Task, error) {
	if err := workerdomain.ValidateSlug("project id", projectID); err != nil {
		return workerdomain.Task{}, err
	}
	if err := workerdomain.ValidateSlug("task id", taskID); err != nil {
		return workerdomain.Task{}, err
	}
	path := s.Path(projectID, taskID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workerdomain.Task{}, fmt.Errorf("%w: %q", ErrNotFound, taskID)
		}
		return workerdomain.Task{}, fmt.Errorf("read task %s: %w", path, err)
	}
	var task workerdomain.Task
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil {
		return workerdomain.Task{}, fmt.Errorf("corrupt task %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return workerdomain.Task{}, fmt.Errorf("corrupt task %s: trailing JSON", path)
	}
	if task.ProjectID != projectID || task.ID != taskID {
		return workerdomain.Task{}, fmt.Errorf("corrupt task %s: identity does not match its path", path)
	}
	if err := task.Validate(); err != nil {
		return workerdomain.Task{}, fmt.Errorf("corrupt task %s: %w", path, err)
	}
	return task, nil
}

// SaveLaunchLocation atomically records the Git location selected for a task.
// It is intentionally narrow so task identity and its immutable content
// snapshot cannot be changed by launch setup.
func (s *Store) SaveLaunchLocation(task workerdomain.Task) (workerdomain.Task, error) {
	unlock, err := s.lockTask(task.ProjectID, task.ID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	defer unlock()
	current, err := s.Load(task.ProjectID, task.ID)
	if err != nil {
		return workerdomain.Task{}, err
	}
	current.Worktree = task.Worktree
	current.Branch = task.Branch
	current.BaseBranch = task.BaseBranch
	current.BaseRevision = task.BaseRevision
	current.LaunchSetup = task.LaunchSetup
	current.UpdatedAt = s.now().UTC()
	if err := current.Validate(); err != nil {
		return workerdomain.Task{}, err
	}
	if err := s.writeAtomic(current); err != nil {
		return workerdomain.Task{}, err
	}
	return current, nil
}

func (s *Store) lockTask(projectID, taskID string) (func(), error) {
	if err := workerdomain.ValidateSlug("task id", taskID); err != nil {
		return nil, err
	}
	return s.lock(projectID, "task-"+taskID)
}

func (s *Store) List(projectID, workerID string) ([]workerdomain.Task, error) {
	if err := workerdomain.ValidateSlug("project id", projectID); err != nil {
		return nil, err
	}
	if workerID != "" {
		if err := workerdomain.ValidateSlug("worker id", workerID); err != nil {
			return nil, err
		}
	}
	dir := filepath.Dir(s.Path(projectID, "placeholder"))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []workerdomain.Task{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	tasks := make([]workerdomain.Task, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		task, err := s.Load(projectID, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if workerID == "" || task.WorkerID == workerID {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func readSource(root, source string) (string, string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	absoluteRoot, err = filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root %s: %w", root, err)
	}
	candidate := source
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absoluteRoot, candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("read task source %s: %w", source, err)
	}
	relative, err := filepath.Rel(absoluteRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("task source %q escapes repository %s", source, absoluteRoot)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("read task source %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("task source %q is not a regular file", source)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", "", fmt.Errorf("read task source %s: %w", source, err)
	}
	return relative, string(data), nil
}

func (s *Store) create(task workerdomain.Task) error {
	dir := filepath.Dir(s.Path(task.ProjectID, task.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create task directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(dir, ".task-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary task snapshot: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(file.Name())
		return err
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	path := s.Path(task.ProjectID, task.ID)
	if err := os.Link(file.Name(), path); err != nil {
		_ = os.Remove(file.Name())
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %q", ErrTaskExists, task.ID)
		}
		return fmt.Errorf("publish task snapshot: %w", err)
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = os.Remove(path)
		return err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (s *Store) writeAtomic(task workerdomain.Task) error {
	path := s.Path(task.ProjectID, task.ID)
	dir := filepath.Dir(path)
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".task-update-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
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
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func (s *Store) lock(projectID, workerID string) (func(), error) {
	dir := filepath.Join(s.home, "projects", projectID, "workers", workerID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".assignment.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
