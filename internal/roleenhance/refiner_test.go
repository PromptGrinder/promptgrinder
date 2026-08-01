package roleenhance

import (
	"reflect"
	"testing"
	"time"
)

func TestRoleReviewRefinerNormalizesAndCalculatesExactValues(t *testing.T) {
	current := refinerState()
	tests := []struct {
		name           string
		recommendation Recommendation
		wantOld        any
		wantNew        any
		wantSafety     SafetyClassification
	}{
		{"scalar", rec("name", OperationSet, []any{"API"}), "Backend", "API", SafetyReplacement},
		{"list representation", rec("technology", OperationAppend, "Rust"), []string{"Go"}, []string{"Go", "Rust"}, SafetyAddition},
		{"generated path cleanup", rec("allowed_paths", OperationAppend, `.\\internal\\api\\..\\service`), []string{"internal", "cmd"}, []string{"internal", "cmd", "internal/service"}, SafetyAddition},
		{"command whitespace", rec("quality_gates", OperationAppend, "  go vet ./... \\\r\n    -flag  "), []string{"go test ./..."}, []string{"go test ./...", "go vet ./... \\\n-flag"}, SafetyAddition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (RoleReviewRefiner{}).Refine(current, []Recommendation{tt.recommendation}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0].OldValue, tt.wantOld) || !reflect.DeepEqual(got[0].ProposedValue, tt.wantNew) || got[0].Safety != tt.wantSafety {
				t.Fatalf("items = %#v", got)
			}
		})
	}
}

func TestRoleReviewRefinerDeduplicatesDropsNoOpsAndSplitsRemovals(t *testing.T) {
	appendRec := rec("technology", OperationAppend, []any{"Go", "Rust", "Rust"})
	duplicate := appendRec
	duplicate.ID = "other-model-id"
	removeRec := rec("allowed_paths", OperationRemove, []any{"missing", "internal", "cmd"})
	items, err := (RoleReviewRefiner{}).Refine(refinerState(), []Recommendation{appendRec, duplicate, removeRec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items: %#v", len(items), items)
	}
	var additions, removals int
	for _, item := range items {
		switch item.Safety {
		case SafetyAddition:
			additions++
			if !reflect.DeepEqual(item.ProposedValue, []string{"Go", "Rust"}) {
				t.Fatalf("append result = %#v", item.ProposedValue)
			}
		case SafetyRemoval:
			removals++
			if item.Decision != ReviewDecisionPending {
				t.Fatalf("removal was implicitly approved: %#v", item)
			}
		}
	}
	if additions != 1 || removals != 2 {
		t.Fatalf("additions=%d removals=%d", additions, removals)
	}
}

func TestRoleReviewRefinerConflictsAreVisible(t *testing.T) {
	tests := [][]Recommendation{
		{rec("description", OperationSet, "one"), withID(rec("description", OperationSet, "two"), "two")},
		{rec("technology", OperationSet, []any{"Rust"}), withID(rec("technology", OperationRemove, "Go"), "remove")},
		{rec("technology", OperationAppend, "Go"), withID(rec("technology", OperationRemove, "Go"), "remove")},
	}
	for _, recommendations := range tests {
		items, err := (RoleReviewRefiner{}).Refine(refinerState(), recommendations, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 {
			t.Fatalf("items = %#v", items)
		}
		for _, item := range items {
			if item.Safety != SafetyConflict || item.Conflict == "" || item.Decision != ReviewDecisionPending {
				t.Fatalf("conflict not explicit: %#v", item)
			}
		}
	}
}

func TestRoleReviewRefinerIsOrderIndependentAndPreservesOnlyExactDecisions(t *testing.T) {
	a := rec("technology", OperationAppend, "Rust")
	b := withID(rec("quality_gates", OperationAppend, "go vet ./..."), "gate")
	refiner := RoleReviewRefiner{}
	first, err := refiner.Refine(refinerState(), []Recommendation{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := refiner.Refine(refinerState(), []Recommendation{b, a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ordering changed result:\n%#v\n%#v", first, second)
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first[0].Decision, first[0].DecisionAt = ReviewDecisionApproved, &stamp
	preserved, err := refiner.Refine(refinerState(), []Recommendation{a, b}, first)
	if err != nil {
		t.Fatal(err)
	}
	if preserved[0].Decision != ReviewDecisionApproved || preserved[0].DecisionAt == nil {
		t.Fatalf("exact decision lost: %#v", preserved[0])
	}
	changed := a
	changed.Value = "Java"
	revised, err := refiner.Refine(refinerState(), []Recommendation{changed, b}, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range revised {
		if item.Field == "technology" && item.Decision != ReviewDecisionPending {
			t.Fatalf("changed decision preserved: %#v", item)
		}
	}
}

func TestRoleReviewRefinerPersistsOriginalAndRefinedResults(t *testing.T) {
	recommendations := []Recommendation{rec("technology", OperationAppend, []any{"Go", "Rust"})}
	review := RoleReview{}
	if err := (RoleReviewRefiner{}).RefineReview(refinerState(), &review, recommendations); err != nil {
		t.Fatal(err)
	}
	if review.Status != ReviewStatusRefined || !reflect.DeepEqual(review.OriginalRecommendations, recommendations) || len(review.Items) != 1 {
		t.Fatalf("review = %#v", review)
	}
}

func TestRoleReviewRefinerKeepsReviewDecisionState(t *testing.T) {
	recommendations := []Recommendation{rec("technology", OperationAppend, "Rust"), withID(rec("quality_gates", OperationAppend, "go vet ./..."), "gate")}
	refiner := RoleReviewRefiner{}
	items, err := refiner.Refine(refinerState(), recommendations)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	items[0].Decision, items[0].DecisionAt = ReviewDecisionApproved, &stamp
	review := RoleReview{Status: ReviewStatusPartiallyDecided, Items: items}
	if err := refiner.RefineReview(refinerState(), &review, recommendations); err != nil {
		t.Fatal(err)
	}
	if review.Status != ReviewStatusPartiallyDecided || review.Items[0].Decision != ReviewDecisionApproved {
		t.Fatalf("decision state = %#v", review)
	}
}

func refinerState() CurrentState {
	return CurrentState{Roles: []RoleSnapshot{{ID: "backend", Name: "Backend", Description: "old", Technology: []string{"Go"}, AllowedPaths: []string{"internal", "cmd"}, QualityGates: []string{"go test ./..."}, Runtime: RuntimeSnapshot{Preferred: "local"}}}}
}

func rec(field string, operation Operation, value any) Recommendation {
	return Recommendation{ID: "rec", RoleID: "backend", Field: field, Operation: operation, Value: value, Confidence: ConfidenceHigh, Explanation: "validated explanation", Evidence: []Citation{{Path: `docs\\guide.md`}}}
}

func withID(r Recommendation, id string) Recommendation { r.ID = id; return r }
