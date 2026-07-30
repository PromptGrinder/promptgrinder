package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireRejectsActiveClaimAndReleaseAllowsNextOwner(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	first, err := Acquire(home, repo, "first", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(home, repo, "second", false); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("err = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(home, repo, "second", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireAllowsDifferentWorktreesAndExplicitOverride(t *testing.T) {
	home := t.TempDir()
	first, err := Acquire(home, t.TempDir(), "first", false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := Acquire(home, t.TempDir(), "second", false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	override, err := Acquire(home, first.claim.Worktree, "override", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := override.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsConcurrentClaimInDisposableGitWorktree(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	command := exec.Command("git", "-C", repo, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	first, err := Acquire(home, repo, "backend-sonar/task-one", false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := Acquire(home, repo, "frontend/task-two", false); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("concurrent claim error = %v", err)
	}
}

func TestTransferPIDPreservesClaimForRuntimeProcess(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	lease, err := Acquire(home, repo, "named worker", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.TransferPID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(home, repo, "other worker", false); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("transferred claim error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireReplacesStaleClaim(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	canonical, err := canonicalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "state", "worktree-claims")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(canonical))
	path := filepath.Join(root, hex.EncodeToString(sum[:])+".json")
	stale := Claim{Worktree: canonical, Owner: "stale", PID: 99999999, Token: "stale-token", Started: time.Now().Add(-time.Hour)}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(home, repo, "replacement", false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.claim.Owner != "replacement" {
		t.Fatalf("claim = %#v", lease.claim)
	}
}
