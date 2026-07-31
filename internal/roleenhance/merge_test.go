package roleenhance

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mergeFixture(t *testing.T) (string, CurrentState, ReviewPlan) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".promptgrinder", "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := "name: sample\nlanguages: [Go]\nroles: [backend]\ngenerated_by: promptgrinder discover\n"
	role := "id: backend\nname: Backend\ndescription: old\ntechnology: [Go]\nallowed_paths: [internal]\nruntime:\n  preferred: local\nquality_gates: [go test ./...]\ncustom: keep-me\n"
	if err := os.WriteFile(filepath.Join(root, ".promptgrinder", "project.yaml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend.yaml"), []byte(role), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := LoadCurrentState(root)
	if err != nil {
		t.Fatal(err)
	}
	recs := []Recommendation{
		{ID: "desc", RoleID: "backend", Operation: OperationSet, Field: "description", Value: "Own APIs", Confidence: ConfidenceHigh, Explanation: "docs", Evidence: []Citation{{Path: "README.md"}}},
		{ID: "gate", RoleID: "backend", Operation: OperationAppend, Field: "quality_gates", Value: "go vet ./...", Confidence: ConfidenceMedium, Explanation: "CI", Evidence: []Citation{{Path: "README.md"}}},
	}
	plan, err := (RoleDiffGenerator{}).Generate(current, recs)
	if err != nil {
		t.Fatal(err)
	}
	return root, current, plan
}

func TestRoleMergeApplySelectedPreservesCustomYAML(t *testing.T) {
	root, current, plan := mergeFixture(t)
	result, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplySelected, RecommendationIDs: []string{"desc"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || !reflect.DeepEqual(result.Rejected, []string{"gate"}) {
		t.Fatalf("result=%#v", result)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "roles", "backend.yaml"))
	text := string(b)
	for _, want := range []string{"description: Own APIs", "custom: keep-me", "quality_gates:", "go test ./..."} {
		if !strings.Contains(text, want) {
			t.Fatalf("role missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "go vet") {
		t.Fatalf("unapproved change applied:\n%s", text)
	}
}

func TestRoleMergeRejectAndStaleConflictDoNotWrite(t *testing.T) {
	root, current, plan := mergeFixture(t)
	path := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	before, _ := os.ReadFile(path)
	if _, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalRejectAll}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reject changed file")
	}
	changed := append(append([]byte(nil), before...), []byte("# user edit\n")...)
	if err := os.WriteFile(path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplyAll})
	if err == nil || len(result.Conflicts) == 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	after, _ = os.ReadFile(path)
	if !reflect.DeepEqual(changed, after) {
		t.Fatal("conflict changed file")
	}
}

func TestRoleMergeValidatesWholeSelectionBeforeWriting(t *testing.T) {
	root, current, plan := mergeFixture(t)
	path := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	before, _ := os.ReadFile(path)
	plan.Items[1].Recommendation.Value = []any{"invalid", "append"}
	if _, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplyAll}); err == nil {
		t.Fatal("expected invalid plan")
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("validation failure changed file")
	}
}

func TestRemovalRequiresIndividualApproval(t *testing.T) {
	root, current, plan := mergeFixture(t)
	plan.Items = []ReviewItem{{Recommendation: Recommendation{ID: "remove", RoleID: "backend", Operation: OperationRemove, Field: "technology", Value: "Go", Confidence: ConfidenceHigh, Explanation: "obsolete", Evidence: []Citation{{Path: "go.mod"}}}, OldValue: []string{"Go"}}}
	if _, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplyAll}); err == nil {
		t.Fatal("apply-all removal should fail")
	}
	if _, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplySelected, RecommendationIDs: []string{"remove"}}); err != nil {
		t.Fatal(err)
	}
}
