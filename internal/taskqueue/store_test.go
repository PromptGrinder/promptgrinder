package taskqueue

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFIFOReorderRemoveAndRestartPersistence(t *testing.T) {
	home := t.TempDir()
	store := New(home)
	for _, id := range []string{"one", "two", "three"} {
		if _, err := store.Enqueue("project", "worker", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Reorder("project", "worker", "three", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remove("project", "worker", "two"); err != nil {
		t.Fatal(err)
	}
	queue, err := New(home).List("project", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Entries) != 2 || queue.Entries[0].TaskID != "one" || queue.Entries[1].TaskID != "three" {
		t.Fatalf("queue after restart = %#v", queue.Entries)
	}
	if _, err := store.Reorder("project", "worker", "missing", 0); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("missing reorder error = %v", err)
	}
}

func TestLeaseExclusionAndExpiryRecovery(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	store := New(home)
	store.now = func() time.Time { return now }
	if _, err := store.Enqueue("project", "worker", "one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Acquire("project", "worker", "first", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Acquire("project", "worker", "second", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("concurrent lease error = %v", err)
	}
	if _, err := store.Remove("project", "worker", "one"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("leased removal error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	entry, _, err := store.Acquire("project", "worker", "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Lease == nil || entry.Lease.Owner != "second" {
		t.Fatalf("recovered lease = %#v", entry.Lease)
	}
}

func TestConcurrentAcquireHasSingleWinner(t *testing.T) {
	home := t.TempDir()
	store := New(home)
	if _, err := store.Enqueue("project", "worker", "one"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for _, owner := range []string{"a", "b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			_, _, err := New(home).Acquire("project", "worker", owner, time.Minute)
			if err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			} else if !errors.Is(err, ErrLeaseHeld) {
				t.Errorf("acquire: %v", err)
			}
		}(owner)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("lease winners = %d, want 1", winners)
	}
}
