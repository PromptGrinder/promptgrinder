// Package taskqueue owns the durable FIFO queue for persistent named workers.
package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"promptgrinder/internal/workerdomain"
)

var (
	ErrNotPending = errors.New("task is not pending")
	ErrNotQueued  = errors.New("task is not queued")
	ErrLeaseHeld  = errors.New("task queue lease is held")
)

type Lease struct {
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Entry struct {
	TaskID     string    `json:"task_id"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	Lease      *Lease    `json:"lease,omitempty"`
}

type Queue struct {
	Version   int       `json:"version"`
	Revision  uint64    `json:"revision"`
	ProjectID string    `json:"project_id"`
	WorkerID  string    `json:"worker_id"`
	Entries   []Entry   `json:"entries"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	home string
	now  func() time.Time
}

func New(home string) *Store { return &Store{home: home, now: time.Now} }

func (s *Store) Path(projectID, workerID string) string {
	return filepath.Join(s.home, "projects", projectID, "workers", workerID, "queue.json")
}

func (s *Store) List(projectID, workerID string) (Queue, error) {
	unlock, err := s.lock(projectID, workerID)
	if err != nil {
		return Queue{}, err
	}
	defer unlock()
	return s.load(projectID, workerID)
}

func (s *Store) Enqueue(projectID, workerID, taskID string) (Queue, error) {
	return s.update(projectID, workerID, func(q *Queue) error {
		for _, entry := range q.Entries {
			if entry.TaskID == taskID {
				return fmt.Errorf("task %q is already queued", taskID)
			}
		}
		q.Entries = append(q.Entries, Entry{TaskID: taskID, EnqueuedAt: s.now().UTC()})
		return nil
	})
}

// Reorder moves one pending entry to the zero-based position.
func (s *Store) Reorder(projectID, workerID, taskID string, position int) (Queue, error) {
	return s.update(projectID, workerID, func(q *Queue) error {
		if position < 0 || position >= len(q.Entries) {
			return fmt.Errorf("queue position %d is out of range", position)
		}
		index := -1
		for i := range q.Entries {
			if q.Entries[i].TaskID == taskID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("%w: %q", ErrNotQueued, taskID)
		}
		if q.Entries[index].Lease != nil {
			return fmt.Errorf("%w: %q", ErrNotPending, taskID)
		}
		entry := q.Entries[index]
		q.Entries = append(q.Entries[:index], q.Entries[index+1:]...)
		q.Entries = append(q.Entries, Entry{})
		copy(q.Entries[position+1:], q.Entries[position:])
		q.Entries[position] = entry
		return nil
	})
}

func (s *Store) Remove(projectID, workerID, taskID string) (Queue, error) {
	return s.update(projectID, workerID, func(q *Queue) error {
		for i, entry := range q.Entries {
			if entry.TaskID != taskID {
				continue
			}
			if entry.Lease != nil {
				return fmt.Errorf("%w: %q", ErrNotPending, taskID)
			}
			q.Entries = append(q.Entries[:i], q.Entries[i+1:]...)
			return nil
		}
		return fmt.Errorf("%w: %q", ErrNotQueued, taskID)
	})
}

// Acquire leases the FIFO head. Expired leases are safely recoverable.
func (s *Store) Acquire(projectID, workerID, owner string, ttl time.Duration) (Entry, Queue, error) {
	var selected Entry
	q, err := s.update(projectID, workerID, func(q *Queue) error {
		if len(q.Entries) == 0 {
			return fmt.Errorf("%w: queue is empty", ErrNotQueued)
		}
		now := s.now().UTC()
		head := &q.Entries[0]
		if head.Lease != nil && head.Lease.ExpiresAt.After(now) {
			return fmt.Errorf("%w by %q until %s", ErrLeaseHeld, head.Lease.Owner, head.Lease.ExpiresAt.Format(time.RFC3339Nano))
		}
		head.Lease = &Lease{Owner: owner, ExpiresAt: now.Add(ttl)}
		selected = *head
		return nil
	})
	return selected, q, err
}

func (s *Store) Commit(projectID, workerID, taskID, owner string) (Queue, error) {
	return s.update(projectID, workerID, func(q *Queue) error {
		if len(q.Entries) == 0 || q.Entries[0].TaskID != taskID {
			return fmt.Errorf("%w: %q is not the queue head", ErrNotQueued, taskID)
		}
		lease := q.Entries[0].Lease
		if lease == nil || lease.Owner != owner {
			return fmt.Errorf("%w: lease owner mismatch", ErrLeaseHeld)
		}
		q.Entries = q.Entries[1:]
		return nil
	})
}

func (s *Store) Release(projectID, workerID, taskID, owner string) (Queue, error) {
	return s.update(projectID, workerID, func(q *Queue) error {
		for i := range q.Entries {
			if q.Entries[i].TaskID == taskID {
				if q.Entries[i].Lease == nil || q.Entries[i].Lease.Owner != owner {
					return fmt.Errorf("%w: lease owner mismatch", ErrLeaseHeld)
				}
				q.Entries[i].Lease = nil
				return nil
			}
		}
		return fmt.Errorf("%w: %q", ErrNotQueued, taskID)
	})
}

func (s *Store) update(projectID, workerID string, mutate func(*Queue) error) (Queue, error) {
	unlock, err := s.lock(projectID, workerID)
	if err != nil {
		return Queue{}, err
	}
	defer unlock()
	q, err := s.load(projectID, workerID)
	if err != nil {
		return Queue{}, err
	}
	if err := mutate(&q); err != nil {
		return Queue{}, err
	}
	q.Revision++
	q.UpdatedAt = s.now().UTC()
	return q, s.write(q)
}

func (s *Store) load(projectID, workerID string) (Queue, error) {
	if err := workerdomain.ValidateSlug("project id", projectID); err != nil {
		return Queue{}, err
	}
	if err := workerdomain.ValidateSlug("worker id", workerID); err != nil {
		return Queue{}, err
	}
	path := s.Path(projectID, workerID)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Queue{Version: 1, Revision: 1, ProjectID: projectID, WorkerID: workerID, Entries: []Entry{}, UpdatedAt: s.now().UTC()}, nil
	}
	if err != nil {
		return Queue{}, err
	}
	var q Queue
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		return Queue{}, fmt.Errorf("corrupt queue %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Queue{}, fmt.Errorf("corrupt queue %s: trailing JSON", path)
	}
	if q.Version != 1 || q.ProjectID != projectID || q.WorkerID != workerID || q.Revision == 0 {
		return Queue{}, fmt.Errorf("corrupt queue %s: invalid schema or identity", path)
	}
	return q, nil
}

func (s *Store) write(q Queue) error {
	path := s.Path(q.ProjectID, q.WorkerID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".queue-*.tmp")
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
	if err := os.Rename(name, path); err != nil {
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
	dir := filepath.Dir(s.Path(projectID, workerID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".queue.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
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
