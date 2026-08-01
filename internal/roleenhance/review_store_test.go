package roleenhance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReviewStoreCreateLoadStableAndPermissions(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with spaces 日本語")
	store, err := NewReviewStore(home, "project-one")
	if err != nil {
		t.Fatal(err)
	}
	review := reviewFixture(t, "project-one")
	created, err := store.Create(review)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID[:4] != "rev_" || created.Revision != 1 {
		t.Fatalf("created identity = %#v", created)
	}
	loaded, err := store.Load(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Items[0].ProposedValue != "新しい説明" {
		t.Fatalf("proposed value = %#v", loaded.Items[0].ProposedValue)
	}
	first, err := os.ReadFile(store.Path(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndUpdate(created.ID, 1, func(r *RoleReview) error { r.Status = ReviewStatusRefined; return nil }); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Revision = 1
	loaded.Status = ReviewStatusProposed
	loaded.UpdatedAt = created.UpdatedAt
	loaded.ID = "rev_stable"
	secondStore, _ := NewReviewStore(filepath.Join(t.TempDir(), "other"), "project-one")
	second, err := secondStore.Create(loaded)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, _ := os.ReadFile(secondStore.Path(second.ID))
	var one, two map[string]any
	if json.Unmarshal(first, &one) != nil || json.Unmarshal(secondBytes, &two) != nil {
		t.Fatal("invalid JSON")
	}
	delete(one, "id")
	delete(one, "created_at")
	delete(one, "updated_at")
	delete(two, "id")
	delete(two, "created_at")
	delete(two, "updated_at")
	if string(mustJSON(t, one)) != string(mustJSON(t, two)) {
		t.Fatalf("normalized records differ\n%s\n%s", mustJSON(t, one), mustJSON(t, two))
	}
	for _, path := range []string{store.Dir(), store.Path(created.ID)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if !info.IsDir() {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
}

func TestReviewStoreListLatestAndMissing(t *testing.T) {
	store, _ := NewReviewStore(filepath.Join(t.TempDir(), "home"), "project")
	if reviews, err := store.List(); err != nil || len(reviews) != 0 {
		t.Fatalf("empty list = %v, %v", reviews, err)
	}
	if _, err := store.Latest(); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("latest error = %v", err)
	}
	times := []time.Time{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	store.now = func() time.Time { value := times[0]; times = times[1:]; return value }
	one := reviewFixture(t, "project")
	one.ID = "rev_one"
	if _, err := store.Create(one); err != nil {
		t.Fatal(err)
	}
	two := reviewFixture(t, "project")
	two.ID = "rev_two"
	if _, err := store.Create(two); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "rev_two" {
		t.Fatalf("list order = %#v", list)
	}
	latest, err := store.Latest()
	if err != nil || latest.ID != "rev_two" {
		t.Fatalf("latest = %s, %v", latest.ID, err)
	}
}

func TestReviewStoreRejectsCorruptionSchemaTraversalAndSymlinks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	store, _ := NewReviewStore(home, "project")
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := store.ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "rev_bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("rev_bad"); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "rev_future.json"), []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("rev_future"); !errors.Is(err, ErrUnsupportedReviewSchema) {
		t.Fatalf("future schema error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "rev_old.json"), []byte(`{"id":"rev_old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("rev_old"); !errors.Is(err, ErrUnsupportedReviewSchema) {
		t.Fatalf("old schema error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.Dir(), "rev_link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("rev_link"); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestReviewStoreCompareAndUpdateIsConcurrentAndTransitionSafe(t *testing.T) {
	store, _ := NewReviewStore(filepath.Join(t.TempDir(), "home"), "project")
	review := reviewFixture(t, "project")
	review.ID = "rev_concurrent"
	created, err := store.Create(review)
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 12
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.CompareAndUpdate(created.ID, 1, func(r *RoleReview) error { r.Status = ReviewStatusRefined; return nil })
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, stale := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrStaleRevision) {
			stale++
		} else {
			t.Fatalf("update error = %v", err)
		}
	}
	if success != 1 || stale != attempts-1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
	if _, err := store.CompareAndUpdate(created.ID, 2, func(r *RoleReview) error { r.Status = ReviewStatusProposed; return nil }); err == nil {
		t.Fatal("invalid transition accepted")
	}
	loaded, _ := store.Load(created.ID)
	if loaded.Revision != 2 || loaded.Status != ReviewStatusRefined {
		t.Fatalf("failed update changed record: %#v", loaded)
	}
}

func TestReviewStoreRejectsCrossProjectAndDuplicateID(t *testing.T) {
	store, _ := NewReviewStore(filepath.Join(t.TempDir(), "home"), "project")
	review := reviewFixture(t, "other")
	if _, err := store.Create(review); err == nil {
		t.Fatal("cross-project record accepted")
	}
	review.ProjectID = "project"
	review.ID = "rev_duplicate"
	if _, err := store.Create(review); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(review); err == nil {
		t.Fatal("duplicate id accepted")
	}
}

func reviewFixture(t *testing.T, projectID string) RoleReview {
	t.Helper()
	repository, err := IdentifyRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recommendation := Recommendation{ID: "rec-1", RoleID: "backend", Operation: OperationSet, Field: "description", Value: "新しい説明", Confidence: ConfidenceHigh, Explanation: "A bounded explanation", Evidence: []Citation{{Path: "docs/設計 note.md", Fact: "role=backend"}}}
	return RoleReview{ProjectID: projectID, ProjectName: "Project 日本語", Repository: repository, Status: ReviewStatusProposed,
		Sources:                 []ReviewSource{NewReviewSource(".promptgrinder/roles/backend.yaml", []byte("id: backend\n")), NewReviewSource(".promptgrinder/project.yaml", []byte("name: project\n"))},
		OriginalRecommendations: []Recommendation{recommendation},
		Items:                   []StoredReviewItem{{ID: recommendation.ID, OriginalRecommendationID: recommendation.ID, RoleID: recommendation.RoleID, Operation: recommendation.Operation, Field: recommendation.Field, OldValue: "Old description", ProposedValue: recommendation.Value, Confidence: recommendation.Confidence, Explanation: recommendation.Explanation, Evidence: recommendation.Evidence, Safety: SafetyReplacement, Decision: ReviewDecisionPending}},
		Evidence:                PersistedEvidence{Citations: recommendation.Evidence, Facts: []EvidenceFact{{Kind: EvidenceDocumentation, Path: "docs/設計 note.md", Key: "role", Value: "backend"}}}}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
