package roleenhance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

var (
	ErrReviewNotFound = errors.New("role review not found")
	ErrStaleRevision  = errors.New("stale role review revision")
)

type ReviewStore struct {
	home      string
	projectID string
	now       func() time.Time
}

func NewReviewStore(home, projectID string) (ReviewStore, error) {
	if home == "" || !filepath.IsAbs(home) {
		return ReviewStore{}, fmt.Errorf("review store home must be absolute")
	}
	if !validComponent(projectID) {
		return ReviewStore{}, fmt.Errorf("invalid project id %q", projectID)
	}
	return ReviewStore{home: filepath.Clean(home), projectID: projectID, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s ReviewStore) Dir() string {
	return filepath.Join(s.home, "projects", s.projectID, "role-reviews")
}
func (s ReviewStore) Path(id string) string {
	if !validReviewID(id) {
		return ""
	}
	return filepath.Join(s.Dir(), id+".json")
}

func (s ReviewStore) Create(review RoleReview) (RoleReview, error) {
	unlock, err := s.lock()
	if err != nil {
		return RoleReview{}, err
	}
	defer unlock()
	if review.ProjectID != "" && review.ProjectID != s.projectID {
		return RoleReview{}, fmt.Errorf("cross-project role review")
	}
	if review.ID == "" {
		review.ID, err = newReviewID()
		if err != nil {
			return RoleReview{}, err
		}
	}
	if !validReviewID(review.ID) {
		return RoleReview{}, fmt.Errorf("invalid review id %q", review.ID)
	}
	if _, err := os.Lstat(s.Path(review.ID)); err == nil {
		return RoleReview{}, fmt.Errorf("duplicate review id %q", review.ID)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RoleReview{}, err
	}
	now := s.now().UTC()
	review.SchemaVersion, review.Revision, review.ProjectID = ReviewSchemaVersion, 1, s.projectID
	if review.Status == "" {
		review.Status = ReviewStatusProposed
	}
	if review.CreatedAt.IsZero() {
		review.CreatedAt = now
	}
	review.CreatedAt = review.CreatedAt.UTC()
	review.UpdatedAt = now
	review.normalize()
	if err := review.Validate(); err != nil {
		return RoleReview{}, err
	}
	if err := s.write(review); err != nil {
		return RoleReview{}, err
	}
	return review, nil
}

func (s ReviewStore) Load(id string) (RoleReview, error) {
	if !validReviewID(id) {
		return RoleReview{}, fmt.Errorf("invalid review id %q", id)
	}
	return s.loadPath(s.Path(id), id)
}

func (s ReviewStore) List() ([]RoleReview, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		return nil, err
	}
	reviews := make([]RoleReview, 0)
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("invalid role review store entry %q", entry.Name())
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		if seen[id] {
			return nil, fmt.Errorf("duplicate review id %q", id)
		}
		seen[id] = true
		review, err := s.Load(id)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", entry.Name(), err)
		}
		reviews = append(reviews, review)
	}
	sort.Slice(reviews, func(i, j int) bool {
		if !reviews[i].CreatedAt.Equal(reviews[j].CreatedAt) {
			return reviews[i].CreatedAt.After(reviews[j].CreatedAt)
		}
		return reviews[i].ID > reviews[j].ID
	})
	return reviews, nil
}

func (s ReviewStore) Latest() (RoleReview, error) {
	reviews, err := s.List()
	if err != nil {
		return RoleReview{}, err
	}
	if len(reviews) == 0 {
		return RoleReview{}, ErrReviewNotFound
	}
	return reviews[0], nil
}

func (s ReviewStore) CompareAndUpdate(id string, expectedRevision uint64, update func(*RoleReview) error) (RoleReview, error) {
	if update == nil {
		return RoleReview{}, fmt.Errorf("review update is required")
	}
	unlock, err := s.lock()
	if err != nil {
		return RoleReview{}, err
	}
	defer unlock()
	current, err := s.loadPath(s.Path(id), id)
	if err != nil {
		return RoleReview{}, err
	}
	if current.Revision != expectedRevision {
		return RoleReview{}, fmt.Errorf("%w: have %d, expected %d", ErrStaleRevision, current.Revision, expectedRevision)
	}
	next := current
	if err := update(&next); err != nil {
		return RoleReview{}, err
	}
	if !sameImmutableIdentity(current, next) {
		return RoleReview{}, fmt.Errorf("review identity fields are immutable")
	}
	if !validTransition(current.Status, next.Status) {
		return RoleReview{}, fmt.Errorf("invalid review status transition %q to %q", current.Status, next.Status)
	}
	next.Revision++
	next.UpdatedAt = s.now().UTC()
	next.normalize()
	if err := next.Validate(); err != nil {
		return RoleReview{}, err
	}
	if err := s.write(next); err != nil {
		return RoleReview{}, err
	}
	return next, nil
}

func (s ReviewStore) loadPath(path, expectedID string) (RoleReview, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return RoleReview{}, ErrReviewNotFound
	}
	if err != nil {
		return RoleReview{}, err
	}
	if !info.Mode().IsRegular() {
		return RoleReview{}, fmt.Errorf("role review is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return RoleReview{}, fmt.Errorf("role review permissions are not restrictive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RoleReview{}, err
	}
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return RoleReview{}, fmt.Errorf("malformed role review: %w", err)
	}
	if envelope.SchemaVersion != ReviewSchemaVersion {
		return RoleReview{}, fmt.Errorf("%w: %d", ErrUnsupportedReviewSchema, envelope.SchemaVersion)
	}
	var review RoleReview
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&review); err != nil {
		return RoleReview{}, fmt.Errorf("malformed role review: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RoleReview{}, fmt.Errorf("malformed role review: trailing content")
	}
	if review.ID != expectedID || review.ProjectID != s.projectID {
		return RoleReview{}, fmt.Errorf("cross-project or mismatched role review identity")
	}
	if err := review.Validate(); err != nil {
		return RoleReview{}, err
	}
	return review, nil
}

func (s ReviewStore) ensure() error {
	parts := []string{s.home, filepath.Join(s.home, "projects"), filepath.Join(s.home, "projects", s.projectID), s.Dir()}
	for _, dir := range parts {
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(dir)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe role review directory %q", dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s ReviewStore) lock() (func(), error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir(), ".lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe role review lock")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("unsafe role review lock")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}

func (s ReviewStore) write(review RoleReview) error {
	data, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir(), ".review-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, s.Path(review.ID)); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir())
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return err
	}
	ok = true
	return nil
}

func newReviewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rev_" + hex.EncodeToString(b[:]), nil
}
