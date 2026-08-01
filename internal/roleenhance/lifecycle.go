package roleenhance

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

// ReviewLifecycle owns durable role-review creation, decisions, and application.
// Advisor is used only by Enhance; every other operation is deterministic.
type ReviewLifecycle struct {
	Home    string
	Advisor Advisor
	Now     func() time.Time
}

type ApplyMode string

const (
	ApplySafe     ApplyMode = "safe"
	ApplySelected ApplyMode = "selected"
)

func ProjectID(root string) (string, RepositoryIdentity, error) {
	repository, err := IdentifyRepository(root)
	if err != nil {
		return "", RepositoryIdentity{}, err
	}
	return "repo_" + repository.Digest[:20], repository, nil
}

func (s ReviewLifecycle) store(root string) (ReviewStore, string, RepositoryIdentity, error) {
	id, repository, err := ProjectID(root)
	if err != nil {
		return ReviewStore{}, "", RepositoryIdentity{}, err
	}
	store, err := NewReviewStore(s.Home, id)
	return store, id, repository, err
}

func reviewSources(current CurrentState) []ReviewSource {
	sources := []ReviewSource{NewReviewSource(current.Project.SourcePath, current.Project.Raw)}
	for _, role := range current.Roles {
		sources = append(sources, NewReviewSource(role.SourcePath, role.Raw))
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return sources
}

func collectedReviewSources(root string, current CurrentState, evidence Evidence) ([]ReviewSource, error) {
	sources := reviewSources(current)
	seen := map[string]bool{}
	for _, source := range sources {
		seen[source.Path] = true
	}
	for _, source := range evidence.Sources {
		if !seen[source.Path] {
			content, err := readRegular(filepath.Join(root, filepath.FromSlash(source.Path)))
			if err != nil {
				return nil, err
			}
			sources = append(sources, NewReviewSource(source.Path, content))
			seen[source.Path] = true
		}
	}
	for _, fact := range evidence.Facts {
		if !seen[fact.Path] {
			content, err := readRegular(filepath.Join(root, filepath.FromSlash(fact.Path)))
			if err != nil {
				return nil, err
			}
			sources = append(sources, NewReviewSource(fact.Path, content))
			seen[fact.Path] = true
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return sources, nil
}

func (s ReviewLifecycle) Enhance(ctx context.Context, root string) (RoleReview, error) {
	current, plan, evidence, err := (EnhanceService{Advisor: s.Advisor}).review(ctx, root)
	if err != nil {
		return RoleReview{}, err
	}
	store, projectID, repository, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	recommendations := make([]Recommendation, len(plan.Items))
	for i := range plan.Items {
		recommendations[i] = plan.Items[i].Recommendation
	}
	sources, err := collectedReviewSources(root, current, evidence)
	if err != nil {
		return RoleReview{}, err
	}
	review := RoleReview{ProjectID: projectID, ProjectName: current.Project.Name, Repository: repository,
		Status: ReviewStatusProposed, Sources: sources, Evidence: PersistedEvidence{Facts: evidence.Facts}}
	for _, item := range plan.Items {
		review.Evidence.Citations = append(review.Evidence.Citations, item.Recommendation.Evidence...)
	}
	if err := (RoleReviewRefiner{}).RefineReview(current, &review, recommendations); err != nil {
		return RoleReview{}, err
	}
	return store.Create(review)
}

func (s ReviewLifecycle) List(root string) ([]RoleReview, error) {
	store, _, _, err := s.store(root)
	if err != nil {
		return nil, err
	}
	return store.List()
}

func (s ReviewLifecycle) Load(root, id string) (RoleReview, error) {
	store, _, repository, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	var review RoleReview
	if id == "latest" {
		review, err = store.Latest()
	} else {
		review, err = store.Load(id)
	}
	if err == nil && !reflect.DeepEqual(review.Repository, repository) {
		err = fmt.Errorf("role review belongs to another repository")
	}
	return review, err
}

func (s ReviewLifecycle) Refine(root, id string) (RoleReview, error) {
	store, _, _, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	review, err := s.Load(root, id)
	if err != nil {
		return RoleReview{}, err
	}
	current, err := LoadCurrentState(root)
	if err != nil {
		return RoleReview{}, err
	}
	if err := verifySources(root, review.Sources); err != nil {
		return s.markStale(store, review, err)
	}
	return store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error {
		prior := append([]StoredReviewItem(nil), next.Items...)
		if err := (RoleReviewRefiner{}).RefineReview(current, next, next.OriginalRecommendations); err != nil {
			return err
		}
		for _, edit := range next.Edits {
			for i := range next.Items {
				if next.Items[i].ID == edit.ItemID {
					next.Items[i].ProposedValue = edit.NewValue
				}
			}
		}
		pending, approved := 0, 0
		for i := range next.Items {
			for _, old := range prior {
				if sameRefinedContent(next.Items[i], old) {
					next.Items[i].Decision, next.Items[i].DecisionAt = old.Decision, old.DecisionAt
					break
				}
			}
			if next.Items[i].Decision == ReviewDecisionPending {
				pending++
			}
			if next.Items[i].Decision == ReviewDecisionApproved {
				approved++
			}
		}
		decided := len(next.Items) - pending
		switch {
		case decided == 0:
			next.Status, next.DecidedAt = ReviewStatusRefined, nil
		case pending > 0:
			next.Status, next.DecidedAt = ReviewStatusPartiallyDecided, nil
		case approved == 0:
			next.Status = ReviewStatusRejected
		case next.Status != ReviewStatusApplied:
			next.Status = ReviewStatusDecided
		}
		return nil
	})
}

func (s ReviewLifecycle) Reject(root, id string) (RoleReview, error) {
	store, _, _, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	review, err := s.Load(root, id)
	if err != nil {
		return RoleReview{}, err
	}
	if review.Status == ReviewStatusRejected {
		return review, nil
	}
	if review.Status == ReviewStatusApplied {
		return RoleReview{}, fmt.Errorf("applied review cannot be rejected")
	}
	for _, item := range review.Items {
		if item.AppliedAt != nil {
			return RoleReview{}, fmt.Errorf("partially applied review cannot be rejected")
		}
	}
	stamp := s.now()
	return store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error {
		for i := range next.Items {
			next.Items[i].Decision, next.Items[i].DecisionAt = ReviewDecisionRejected, &stamp
		}
		next.Status, next.DecidedAt = ReviewStatusRejected, &stamp
		return nil
	})
}

// Decide persists an item-level decision without touching role YAML.
func (s ReviewLifecycle) Decide(root, id, itemID string, decision ReviewDecision) (RoleReview, error) {
	if decision != ReviewDecisionApproved && decision != ReviewDecisionRejected {
		return RoleReview{}, fmt.Errorf("decision must be approved or rejected")
	}
	store, _, _, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	review, err := s.Load(root, id)
	if err != nil {
		return RoleReview{}, err
	}
	if review.Status == ReviewStatusApplied || review.Status == ReviewStatusRejected || review.Status == ReviewStatusStale {
		return RoleReview{}, fmt.Errorf("review %s is %s", review.ID, review.Status)
	}
	stamp := s.now()
	return store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error {
		found, pending, approved := false, 0, 0
		for i := range next.Items {
			if next.Items[i].ID == itemID {
				found = true
				next.Items[i].Decision, next.Items[i].DecisionAt = decision, &stamp
			}
			if next.Items[i].Decision == ReviewDecisionPending {
				pending++
			}
			if next.Items[i].Decision == ReviewDecisionApproved {
				approved++
			}
		}
		if !found {
			return fmt.Errorf("unknown review item ID %q", itemID)
		}
		switch {
		case pending > 0:
			next.Status, next.DecidedAt = ReviewStatusPartiallyDecided, nil
		case approved == 0:
			next.Status, next.DecidedAt = ReviewStatusRejected, &stamp
		default:
			next.Status, next.DecidedAt = ReviewStatusDecided, &stamp
		}
		return nil
	})
}

// EditValue validates and persists an exact structured value without invoking an advisor.
func (s ReviewLifecycle) EditValue(root, id, itemID string, value any) (RoleReview, error) {
	store, _, _, err := s.store(root)
	if err != nil {
		return RoleReview{}, err
	}
	review, err := s.Load(root, id)
	if err != nil {
		return RoleReview{}, err
	}
	if review.Status == ReviewStatusApplied || review.Status == ReviewStatusRejected || review.Status == ReviewStatusStale {
		return RoleReview{}, fmt.Errorf("review %s is %s", review.ID, review.Status)
	}
	stamp := s.now()
	return store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error {
		for i := range next.Items {
			item := &next.Items[i]
			if item.ID != itemID {
				continue
			}
			normalized, err := normalizeEditedValue(item.Field, value)
			if err != nil {
				return err
			}
			if reflect.DeepEqual(item.OldValue, normalized) {
				return fmt.Errorf("edited value makes recommendation a no-op")
			}
			old := item.ProposedValue
			item.ProposedValue = normalized
			item.Decision, item.DecisionAt = ReviewDecisionPending, nil
			item.Conflict = ""
			if item.Operation == OperationRemove {
				item.Safety = SafetyRemoval
			} else if emptyValue(item.OldValue) {
				item.Safety = SafetyAddition
			} else {
				item.Safety = SafetyReplacement
			}
			next.Edits = append(next.Edits, ReviewEdit{ItemID: item.ID, OldValue: old, NewValue: normalized, EditedAt: stamp})
			decided := 0
			for _, candidate := range next.Items {
				if candidate.Decision != ReviewDecisionPending {
					decided++
				}
			}
			if decided == 0 {
				next.Status = ReviewStatusRefined
			} else {
				next.Status = ReviewStatusPartiallyDecided
			}
			next.DecidedAt = nil
			return nil
		}
		return fmt.Errorf("unknown review item ID %q", itemID)
	})
}

func normalizeEditedValue(field string, value any) (any, error) {
	rec, err := normalizeRecommendation(Recommendation{ID: "edit", RoleID: "edit", Operation: OperationSet, Field: field, Value: value})
	if err != nil {
		return nil, fmt.Errorf("invalid edited value: %w", err)
	}
	return rec.Value, nil
}

func (s ReviewLifecycle) Apply(root, id string, mode ApplyMode, selected []string) (RoleReview, MergeResult, error) {
	store, _, _, err := s.store(root)
	if err != nil {
		return RoleReview{}, MergeResult{}, err
	}
	review, err := s.Load(root, id)
	if err != nil {
		return RoleReview{}, MergeResult{}, err
	}
	if review.Status == ReviewStatusRejected || review.Status == ReviewStatusStale {
		return RoleReview{}, MergeResult{}, fmt.Errorf("review %s is %s", review.ID, review.Status)
	}
	chosen, err := chooseItems(review, mode, selected)
	if err != nil {
		return RoleReview{}, MergeResult{}, err
	}
	if err := verifySources(root, review.Sources); err != nil {
		stale, markErr := s.markStale(store, review, err)
		return stale, MergeResult{}, markErr
	}
	allAlready := true
	for itemID := range chosen {
		for _, item := range review.Items {
			if item.ID == itemID && item.AppliedAt == nil {
				allAlready = false
			}
		}
	}
	if allAlready {
		return review, MergeResult{}, nil
	}
	current, err := LoadCurrentState(root)
	if err != nil {
		return RoleReview{}, MergeResult{}, err
	}
	plan := exactPlan(review, chosen)
	planIDs := make([]string, len(plan.Items))
	for i := range plan.Items {
		planIDs[i] = plan.Items[i].Recommendation.ID
	}
	result, err := (RoleMergeService{}).Apply(root, current, plan, ApprovalSelection{Mode: ApprovalApplySelected, RecommendationIDs: planIDs})
	if err != nil {
		return RoleReview{}, result, err
	}
	stamp := s.now()
	updated, err := store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error {
		pending := 0
		for i := range next.Items {
			if chosen[next.Items[i].ID] {
				next.Items[i].Decision, next.Items[i].DecisionAt, next.Items[i].AppliedAt = ReviewDecisionApproved, &stamp, &stamp
			}
			if next.Items[i].Decision == ReviewDecisionPending {
				pending++
			}
		}
		if pending == 0 {
			next.Status, next.DecidedAt, next.AppliedAt = ReviewStatusApplied, &stamp, &stamp
		} else {
			next.Status = ReviewStatusPartiallyDecided
		}
		latest, loadErr := LoadCurrentState(root)
		if loadErr != nil {
			return loadErr
		}
		updated := map[string]ReviewSource{}
		for _, source := range next.Sources {
			updated[source.Path] = source
		}
		for _, source := range reviewSources(latest) {
			updated[source.Path] = source
		}
		next.Sources = next.Sources[:0]
		for _, source := range updated {
			next.Sources = append(next.Sources, source)
		}
		sort.Slice(next.Sources, func(i, j int) bool { return next.Sources[i].Path < next.Sources[j].Path })
		return nil
	})
	return updated, result, err
}

func chooseItems(review RoleReview, mode ApplyMode, selected []string) (map[string]bool, error) {
	chosen := map[string]bool{}
	known := map[string]StoredReviewItem{}
	for _, item := range review.Items {
		known[item.ID] = item
	}
	switch mode {
	case ApplySafe:
		if len(selected) != 0 {
			return nil, fmt.Errorf("apply safe cannot include item IDs")
		}
		for _, item := range review.Items {
			if item.Decision != ReviewDecisionRejected && item.Safety == SafetyAddition {
				chosen[item.ID] = true
			}
		}
	case ApplySelected:
		if len(selected) == 0 {
			return nil, fmt.Errorf("apply selected requires at least one item ID")
		}
		for _, requested := range selected {
			id := requested
			if _, ok := known[id]; !ok {
				matches := []string{}
				for _, item := range review.Items {
					for _, sourceID := range item.OriginalRecommendationIDs {
						if sourceID == requested {
							matches = append(matches, item.ID)
						}
					}
					if item.OriginalRecommendationID == requested && len(item.OriginalRecommendationIDs) == 0 {
						matches = append(matches, item.ID)
					}
				}
				if len(matches) != 1 {
					return nil, fmt.Errorf("unknown or non-atomic review item ID %q", requested)
				}
				id = matches[0]
			}
			if chosen[id] {
				return nil, fmt.Errorf("duplicate review item ID %q", id)
			}
			chosen[id] = true
		}
	default:
		return nil, fmt.Errorf("invalid apply mode %q", mode)
	}
	if mode == ApplySelected && len(chosen) == 0 {
		return nil, fmt.Errorf("review has no applicable items")
	}
	return chosen, nil
}

func exactPlan(review RoleReview, chosen map[string]bool) ReviewPlan {
	items := make([]ReviewItem, 0, len(chosen))
	for _, item := range review.Items {
		if chosen[item.ID] && item.AppliedAt == nil {
			value := item.ProposedValue
			if item.Operation == OperationAppend {
				value = valueDifference(item.ProposedValue, item.OldValue)
			}
			if item.Operation == OperationRemove {
				value = valueDifference(item.OldValue, item.ProposedValue)
			}
			items = append(items, ReviewItem{Recommendation: Recommendation{ID: item.ID, RoleID: item.RoleID, Operation: item.Operation, Field: item.Field, Value: value, Confidence: item.Confidence, Explanation: item.Explanation, Evidence: item.Evidence}, OldValue: item.OldValue})
		}
	}
	plan, _ := StableReviewPlan(items)
	return plan
}

func valueDifference(left, right any) []string {
	leftValues, _ := stringValues(left)
	rightValues, _ := stringValues(right)
	excluded := map[string]bool{}
	for _, value := range rightValues {
		excluded[value] = true
	}
	out := []string{}
	for _, value := range leftValues {
		if !excluded[value] {
			out = append(out, value)
		}
	}
	return out
}

func verifySources(root string, sources []ReviewSource) error {
	for _, source := range sources {
		content, err := readRegular(filepath.Join(root, filepath.FromSlash(source.Path)))
		if err != nil {
			return fmt.Errorf("stale source %s: %w", source.Path, err)
		}
		if !bytes.Equal([]byte(NewReviewSource(source.Path, content).Hash), []byte(source.Hash)) {
			return fmt.Errorf("stale source %s", source.Path)
		}
	}
	return nil
}

func (s ReviewLifecycle) markStale(store ReviewStore, review RoleReview, cause error) (RoleReview, error) {
	if review.Status != ReviewStatusStale {
		updated, err := store.CompareAndUpdate(review.ID, review.Revision, func(next *RoleReview) error { next.Status = ReviewStatusStale; return nil })
		if err != nil {
			return RoleReview{}, err
		}
		review = updated
	}
	return review, fmt.Errorf("role review is stale: %w", cause)
}

func (s ReviewLifecycle) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
