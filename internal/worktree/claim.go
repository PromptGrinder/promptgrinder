package worktree

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Claim struct {
	Worktree string    `json:"worktree"`
	Owner    string    `json:"owner"`
	PID      int       `json:"pid"`
	Token    string    `json:"token"`
	Started  time.Time `json:"started_at"`
}

type Lease struct {
	path  string
	claim Claim
}

func Acquire(homeDir, repoRoot, owner string, allowConcurrent bool) (*Lease, error) {
	if allowConcurrent {
		return &Lease{}, nil
	}
	worktree, err := canonicalPath(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	claim := Claim{Worktree: worktree, Owner: owner, PID: os.Getpid(), Token: token, Started: time.Now().UTC()}
	root := filepath.Join(homeDir, "state", "worktree-claims")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(worktree))
	path := filepath.Join(root, hex.EncodeToString(sum[:])+".json")
	for attempts := 0; attempts < 2; attempts++ {
		data, err := json.MarshalIndent(claim, "", "  ")
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.Write(append(data, '\n')); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &Lease{path: path, claim: claim}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		existing, readErr := load(path)
		if readErr != nil {
			return nil, fmt.Errorf("worktree %s has an unreadable active claim at %s: %w", worktree, path, readErr)
		}
		if processAlive(existing.PID) {
			return nil, fmt.Errorf("worktree %s is already in use by PromptGrinder owner %q (pid %d, started %s); use a separate git worktree or --allow-concurrent-worktree", worktree, existing.Owner, existing.PID, existing.Started.Format(time.RFC3339))
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale worktree claim: %w", err)
		}
	}
	return nil, fmt.Errorf("could not claim worktree %s because another PromptGrinder run started concurrently", worktree)
}

func (l *Lease) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	existing, err := load(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.Token != l.claim.Token {
		return nil
	}
	return os.Remove(l.path)
}

// TransferPID keeps a claim active after a detached launch by assigning it to
// the runtime process. The token check prevents an old lease from overwriting
// a newer owner.
func (l *Lease) TransferPID(pid int) error {
	if l == nil || l.path == "" || pid <= 0 {
		return fmt.Errorf("cannot transfer worktree claim to invalid pid %d", pid)
	}
	existing, err := load(l.path)
	if err != nil {
		return err
	}
	if existing.Token != l.claim.Token {
		return fmt.Errorf("worktree claim ownership changed before pid transfer")
	}
	l.claim.PID = pid
	data, err := json.MarshalIndent(l.claim, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(l.path), ".claim-*.tmp")
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
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, l.path); err != nil {
		return err
	}
	l.claim.PID = pid
	return nil
}

func load(path string) (Claim, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Claim{}, err
	}
	var claim Claim
	if err := json.Unmarshal(data, &claim); err != nil {
		return Claim{}, err
	}
	if claim.Worktree == "" || claim.PID <= 0 || claim.Token == "" {
		return Claim{}, fmt.Errorf("invalid claim metadata")
	}
	return claim, nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(abs), nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
