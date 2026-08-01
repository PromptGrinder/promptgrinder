package roleenhance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingAdvisor struct {
	calls int
	plan  ReviewPlan
	err   error
}

func (a *countingAdvisor) Recommend(context.Context, CurrentState, Evidence) (ReviewPlan, error) {
	a.calls++
	return a.plan, a.err
}

func lifecycleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".git", filepath.Join(".promptgrinder", "roles")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(".promptgrinder", "project.yaml"):          "name: test\nroles: [backend]\ngenerated_by: promptgrinder discover\n",
		filepath.Join(".promptgrinder", "roles", "backend.yaml"): "id: backend\nname: Backend\ndescription: old\ntechnology: [Go, Legacy]\nallowed_paths: [internal]\nruntime: {preferred: local}\nquality_gates: []\n",
		"README.md": "The backend owns APIs.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func lifecyclePlan() ReviewPlan {
	return ReviewPlan{Items: []ReviewItem{
		{Recommendation: Recommendation{ID: "desc", RoleID: "backend", Operation: OperationSet, Field: "description", Value: "Own APIs", Confidence: ConfidenceHigh, Explanation: "documented", Evidence: []Citation{{Path: "README.md"}}}},
		{Recommendation: Recommendation{ID: "legacy", RoleID: "backend", Operation: OperationRemove, Field: "technology", Value: "Legacy", Confidence: ConfidenceHigh, Explanation: "obsolete", Evidence: []Citation{{Path: "README.md"}}}},
	}}
}

func TestReviewLifecycleCallsAdvisorOnceAndFollowupsUseNoAI(t *testing.T) {
	root, home := lifecycleRepo(t), filepath.Join(t.TempDir(), "home")
	advisor := &countingAdvisor{plan: lifecyclePlan()}
	lifecycle := ReviewLifecycle{Home: home, Advisor: advisor}
	review, err := lifecycle.Enhance(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if advisor.calls != 1 || review.ID == "" || review.Status != ReviewStatusRefined {
		t.Fatalf("calls=%d review=%+v", advisor.calls, review)
	}
	rolePath := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	before, _ := os.ReadFile(rolePath)
	lifecycle.Advisor = &countingAdvisor{err: errors.New("must not run")}
	if list, err := lifecycle.List(root); err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if _, err := lifecycle.Load(root, "latest"); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Refine(root, review.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(rolePath)
	if string(after) != string(before) {
		t.Fatal("review/refine wrote role YAML")
	}
	loaded, _ := lifecycle.Load(root, review.ID)
	var descriptionID, removalID string
	for _, item := range loaded.Items {
		if item.Safety == SafetyRemoval {
			removalID = item.ID
		} else {
			descriptionID = item.ID
		}
	}
	applied, result, err := lifecycle.Apply(root, review.ID, ApplySelected, []string{descriptionID})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != ReviewStatusPartiallyDecided || len(result.Applied) != 1 {
		t.Fatalf("review=%+v result=%+v", applied, result)
	}
	contents, _ := os.ReadFile(rolePath)
	if !strings.Contains(string(contents), "description: Own APIs") || !strings.Contains(string(contents), "Legacy") {
		t.Fatalf("role=%s", contents)
	}
	if _, _, err := lifecycle.Apply(root, review.ID, ApplySelected, []string{removalID}); err != nil {
		t.Fatal(err)
	}
	contents, _ = os.ReadFile(rolePath)
	if strings.Contains(string(contents), "Legacy") {
		t.Fatalf("removal not applied: %s", contents)
	}
	final, repeated, err := lifecycle.Apply(root, review.ID, ApplySelected, []string{removalID})
	if err != nil || final.Status != ReviewStatusApplied || len(repeated.Files) != 0 {
		t.Fatalf("final=%+v repeat=%+v err=%v", final, repeated, err)
	}
}

func TestReviewLifecycleSafeExcludesRemovalAndStaleWritesNothing(t *testing.T) {
	root := lifecycleRepo(t)
	lifecycle := ReviewLifecycle{Home: filepath.Join(t.TempDir(), "home"), Advisor: &countingAdvisor{plan: lifecyclePlan()}}
	review, err := lifecycle.Enhance(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(rolePath)
	stale, _, err := lifecycle.Apply(root, review.ID, ApplySafe, nil)
	if err == nil || stale.Status != ReviewStatusStale {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	after, _ := os.ReadFile(rolePath)
	if string(after) != string(before) {
		t.Fatal("stale apply wrote role YAML")
	}
	loaded, loadErr := lifecycle.Load(root, review.ID)
	if loadErr != nil || loaded.Status != ReviewStatusStale {
		t.Fatalf("loaded=%+v err=%v", loaded, loadErr)
	}
}

func TestReviewLifecycleDecisionsAndEditsPersistWithoutAdvisorOrYAML(t *testing.T) {
	root := lifecycleRepo(t)
	advisor := &countingAdvisor{plan: lifecyclePlan()}
	lifecycle := ReviewLifecycle{Home: filepath.Join(t.TempDir(), "home"), Advisor: advisor}
	review, err := lifecycle.Enhance(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	before, _ := os.ReadFile(rolePath)
	var descriptionID, removalID string
	for _, item := range review.Items {
		if item.Safety == SafetyRemoval {
			removalID = item.ID
		} else {
			descriptionID = item.ID
		}
	}
	lifecycle.Advisor = &countingAdvisor{err: errors.New("advisor must not run")}
	review, err = lifecycle.EditValue(root, review.ID, descriptionID, "  Own stable APIs  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := findStoredItem(review, descriptionID).ProposedValue; got != "Own stable APIs" || len(review.Edits) != 1 {
		t.Fatalf("edited review = %+v", review)
	}
	review, err = lifecycle.Decide(root, review.ID, descriptionID, ReviewDecisionApproved)
	if err != nil {
		t.Fatal(err)
	}
	if review.Status != ReviewStatusPartiallyDecided {
		t.Fatalf("partial decision status = %s", review.Status)
	}
	review, err = lifecycle.Decide(root, review.ID, removalID, ReviewDecisionRejected)
	if err != nil || review.Status != ReviewStatusDecided {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	refined, err := lifecycle.Refine(root, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if findStoredItem(refined, descriptionID).ProposedValue != "Own stable APIs" {
		t.Fatalf("refine lost edit: %+v", refined)
	}
	after, _ := os.ReadFile(rolePath)
	if string(before) != string(after) {
		t.Fatal("decision or edit wrote role YAML")
	}
	if advisor.calls != 1 {
		t.Fatalf("advisor calls = %d", advisor.calls)
	}
}

func TestReviewLifecycleRejectsInvalidStructuredEdit(t *testing.T) {
	root := lifecycleRepo(t)
	lifecycle := ReviewLifecycle{Home: filepath.Join(t.TempDir(), "home"), Advisor: &countingAdvisor{plan: lifecyclePlan()}}
	review, err := lifecycle.Enhance(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var descriptionID string
	for _, item := range review.Items {
		if item.Field == "description" {
			descriptionID = item.ID
		}
	}
	if _, err := lifecycle.EditValue(root, review.ID, descriptionID, []string{"one", "two"}); err == nil || !strings.Contains(err.Error(), "requires one scalar") {
		t.Fatalf("invalid edit error = %v", err)
	}
}

func findStoredItem(review RoleReview, id string) StoredReviewItem {
	for _, item := range review.Items {
		if item.ID == id {
			return item
		}
	}
	return StoredReviewItem{}
}
