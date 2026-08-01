package roleenhance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
)

// RoleReviewRefiner deterministically converts validated advisor output into
// exact, persisted review items. It performs no semantic rewriting.
type RoleReviewRefiner struct{}

// Refine returns stable review items. Prior decisions survive only when all
// persisted item content, including its exact before/after values, is equal.
func (RoleReviewRefiner) Refine(current CurrentState, recommendations []Recommendation, prior ...[]StoredReviewItem) ([]StoredReviewItem, error) {
	if len(prior) > 1 {
		return nil, fmt.Errorf("at most one prior refined result is supported")
	}
	var previous []StoredReviewItem
	if len(prior) == 1 {
		previous = prior[0]
	}
	roles := roleIndex(current)
	normalized := make([]Recommendation, 0, len(recommendations))
	seen := map[string]bool{}
	for _, input := range recommendations {
		r, err := normalizeRecommendation(input)
		if err != nil {
			return nil, err
		}
		if _, ok := roles[r.RoleID]; !ok {
			return nil, fmt.Errorf("recommendation %q references unknown role %q", r.ID, r.RoleID)
		}
		key := recommendationContentKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, r)
	}
	sort.Slice(normalized, func(i, j int) bool { return recommendationLess(normalized[i], normalized[j]) })

	groups := map[string][]Recommendation{}
	var keys []string
	for _, r := range normalized {
		key := r.RoleID + "\x00" + r.Field
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], r)
	}
	sort.Strings(keys)
	var result []StoredReviewItem
	for _, key := range keys {
		group := groups[key]
		old := fieldValue(roles[group[0].RoleID], group[0].Field)
		items, err := refineField(old, group)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	for i := range result {
		result[i].ID = stableItemID(result[i])
		result[i].Decision = ReviewDecisionPending
		for _, old := range previous {
			if sameRefinedContent(result[i], old) {
				result[i].Decision, result[i].DecisionAt = old.Decision, old.DecisionAt
				break
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// RefineReview persists the immutable original recommendations alongside the
// newly refined result in an existing review value.
func (r RoleReviewRefiner) RefineReview(current CurrentState, review *RoleReview, recommendations []Recommendation) error {
	if review == nil {
		return fmt.Errorf("role review is required")
	}
	original := append([]Recommendation(nil), recommendations...)
	items, err := r.Refine(current, original, review.Items)
	if err != nil {
		return err
	}
	review.OriginalRecommendations = original
	review.Items = items
	decided, pending := 0, 0
	for _, item := range items {
		if item.Decision == ReviewDecisionPending {
			pending++
		} else {
			decided++
		}
	}
	switch {
	case decided == 0:
		review.Status = ReviewStatusRefined
	case pending > 0:
		review.Status = ReviewStatusPartiallyDecided
	case review.Status != ReviewStatusApplied && review.Status != ReviewStatusRejected:
		return fmt.Errorf("fully decided review has no terminal status")
	}
	return nil
}

func refineField(old any, group []Recommendation) ([]StoredReviewItem, error) {
	sets, appends, removes := partitionRecommendations(group)
	contradictorySets := len(sets) > 1 && !allSameValue(sets)
	conflict := contradictorySets || (len(sets) > 0 && (len(appends) > 0 || len(removes) > 0)) || valuesOverlap(appends, removes)
	if conflict {
		const reason = "overlapping recommendations require explicit resolution"
		items := make([]StoredReviewItem, 0, len(group))
		for _, rec := range group {
			proposed, err := proposedValue(old, rec)
			if err != nil {
				return nil, err
			}
			items = append(items, storedItem(old, proposed, []Recommendation{rec}, SafetyConflict, reason))
		}
		return items, nil
	}
	if len(sets) > 0 {
		rec := sets[0]
		proposed, err := proposedValue(old, rec)
		if err != nil {
			return nil, err
		}
		if reflect.DeepEqual(old, proposed) {
			return nil, nil
		}
		safety := SafetyReplacement
		if emptyValue(old) {
			safety = SafetyAddition
		}
		return []StoredReviewItem{storedItem(old, proposed, sets, safety, "")}, nil
	}
	var items []StoredReviewItem
	if len(appends) > 0 {
		combined := consolidateValues(appends)
		proposed, err := proposedValue(old, combined)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(old, proposed) {
			items = append(items, storedItem(old, proposed, appends, SafetyAddition, ""))
		}
	}
	// Each removal remains an independently approvable destructive action.
	for _, rec := range splitValues(removes) {
		proposed, err := proposedValue(old, rec)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(old, proposed) {
			items = append(items, storedItem(old, proposed, []Recommendation{rec}, SafetyRemoval, ""))
		}
	}
	return items, nil
}

func normalizeRecommendation(r Recommendation) (Recommendation, error) {
	values, ok := stringValues(r.Value)
	if !ok || len(values) == 0 {
		return Recommendation{}, fmt.Errorf("recommendation %q has invalid value", r.ID)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		switch r.Field {
		case "allowed_paths":
			value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
			value = path.Clean(value)
		case "quality_gates":
			value = normalizeCommand(value)
		default:
			value = strings.TrimSpace(value)
		}
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return Recommendation{}, fmt.Errorf("recommendation %q contains an empty value", r.ID)
	}
	if scalarField(r.Field) {
		if len(out) != 1 {
			return Recommendation{}, fmt.Errorf("recommendation %q requires one scalar value", r.ID)
		}
		r.Value = out[0]
	} else {
		r.Value = out
	}
	r.Evidence = normalizedCitations(r.Evidence)
	return r, nil
}

func normalizeCommand(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for i := 1; i < len(lines); i++ {
		if strings.HasSuffix(strings.TrimSpace(lines[i-1]), "\\") {
			lines[i] = strings.TrimLeft(lines[i], " \t")
		}
	}
	return strings.Join(lines, "\n")
}

func storedItem(old, proposed any, recs []Recommendation, safety SafetyClassification, conflict string) StoredReviewItem {
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	ids := make([]string, len(recs))
	var evidence []Citation
	var explanations []string
	confidence := ConfidenceHigh
	for i, rec := range recs {
		ids[i] = rec.ID
		evidence = append(evidence, rec.Evidence...)
		if len(explanations) == 0 || explanations[len(explanations)-1] != rec.Explanation {
			explanations = append(explanations, rec.Explanation)
		}
		if confidenceRank(rec.Confidence) < confidenceRank(confidence) {
			confidence = rec.Confidence
		}
	}
	evidence = normalizedCitations(evidence)
	return StoredReviewItem{OriginalRecommendationID: ids[0], OriginalRecommendationIDs: ids, RoleID: recs[0].RoleID,
		Operation: recs[0].Operation, Field: recs[0].Field, OldValue: old, ProposedValue: proposed,
		Confidence: confidence, Explanation: strings.Join(explanations, "\n"), Evidence: evidence, Safety: safety, Conflict: conflict}
}

func stableItemID(item StoredReviewItem) string {
	copy := item
	copy.ID, copy.OriginalRecommendationID, copy.OriginalRecommendationIDs = "", "", nil
	copy.Decision, copy.DecisionAt = "", nil
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return "item_" + hex.EncodeToString(sum[:12])
}

func sameRefinedContent(a, b StoredReviewItem) bool {
	a.OriginalRecommendationID, b.OriginalRecommendationID = "", ""
	a.OriginalRecommendationIDs, b.OriginalRecommendationIDs = nil, nil
	a.Decision, a.DecisionAt = "", nil
	b.Decision, b.DecisionAt = "", nil
	return reflect.DeepEqual(a, b)
}

func recommendationContentKey(r Recommendation) string {
	b, _ := json.Marshal(struct {
		Role, Field string
		Operation   Operation
		Value       any
		Confidence  Confidence
		Explanation string
		Evidence    []Citation
	}{r.RoleID, r.Field, r.Operation, r.Value, r.Confidence, r.Explanation, r.Evidence})
	return string(b)
}
func recommendationLess(a, b Recommendation) bool {
	if a.RoleID != b.RoleID {
		return a.RoleID < b.RoleID
	}
	if a.Field != b.Field {
		return a.Field < b.Field
	}
	if a.Operation != b.Operation {
		return a.Operation < b.Operation
	}
	ka, kb := recommendationContentKey(a), recommendationContentKey(b)
	if ka != kb {
		return ka < kb
	}
	return a.ID < b.ID
}
func scalarField(field string) bool {
	return field == "name" || field == "description" || field == "runtime.preferred"
}
func emptyValue(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	}
	return false
}
func partitionRecommendations(in []Recommendation) (sets, appends, removes []Recommendation) {
	for _, r := range in {
		switch r.Operation {
		case OperationSet:
			sets = append(sets, r)
		case OperationAppend:
			appends = append(appends, r)
		case OperationRemove:
			removes = append(removes, r)
		}
	}
	return
}
func allSameValue(in []Recommendation) bool {
	for i := 1; i < len(in); i++ {
		if !reflect.DeepEqual(in[0].Value, in[i].Value) {
			return false
		}
	}
	return true
}
func valuesOverlap(a, b []Recommendation) bool {
	values := map[string]bool{}
	for _, r := range a {
		items, _ := stringValues(r.Value)
		for _, item := range items {
			values[item] = true
		}
	}
	for _, r := range b {
		items, _ := stringValues(r.Value)
		for _, item := range items {
			if values[item] {
				return true
			}
		}
	}
	return false
}
func consolidateValues(in []Recommendation) Recommendation {
	out := in[0]
	var values []any
	seen := map[string]bool{}
	for _, r := range in {
		vals, _ := stringValues(r.Value)
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				values = append(values, v)
			}
		}
	}
	out.Value = values
	return out
}
func splitValues(in []Recommendation) []Recommendation {
	var out []Recommendation
	for _, r := range in {
		vals, _ := stringValues(r.Value)
		for _, value := range vals {
			copy := r
			copy.Value = []any{value}
			out = append(out, copy)
		}
	}
	return out
}
func normalizedCitations(in []Citation) []Citation {
	seen := map[string]bool{}
	out := make([]Citation, 0, len(in))
	for _, c := range in {
		c.Path = strings.ReplaceAll(c.Path, "\\", "/")
		c.Path = path.Clean(c.Path)
		key := c.Path + "\x00" + c.Fact
		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Fact < out[j].Fact
	})
	return out
}
func confidenceRank(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	default:
		return 1
	}
}
