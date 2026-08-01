package roleenhance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const ReviewSchemaVersion = 1

var ErrUnsupportedReviewSchema = errors.New("unsupported role review schema version")

type ReviewStatus string

const (
	ReviewStatusProposed         ReviewStatus = "proposed"
	ReviewStatusRefined          ReviewStatus = "refined"
	ReviewStatusPartiallyDecided ReviewStatus = "partially_decided"
	ReviewStatusDecided          ReviewStatus = "decided"
	ReviewStatusApplied          ReviewStatus = "applied"
	ReviewStatusRejected         ReviewStatus = "rejected"
	ReviewStatusStale            ReviewStatus = "stale"
)

type ReviewDecision string

const (
	ReviewDecisionPending  ReviewDecision = "pending"
	ReviewDecisionApproved ReviewDecision = "approved"
	ReviewDecisionRejected ReviewDecision = "rejected"
)

type SafetyClassification string

const (
	SafetyAddition    SafetyClassification = "addition"
	SafetyReplacement SafetyClassification = "replacement"
	SafetyRemoval     SafetyClassification = "removal"
	SafetyConflict    SafetyClassification = "conflict"
)

// RepositoryIdentity is an opaque binding to a canonical repository. It is
// deliberately a digest so persisted/user-facing records do not expose a
// developer-specific checkout path.
type RepositoryIdentity struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

func IdentifyRepository(root string) (RepositoryIdentity, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return RepositoryIdentity{Algorithm: "sha256", Digest: hex.EncodeToString(sum[:])}, nil
}

type ReviewSource struct {
	Path      string `json:"path"`
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
}

func NewReviewSource(path string, content []byte) ReviewSource {
	sum := sha256.Sum256(content)
	return ReviewSource{Path: filepath.ToSlash(path), Algorithm: "sha256", Hash: hex.EncodeToString(sum[:])}
}

// PersistedEvidence contains citations and bounded structured facts only. It
// intentionally has no field for collected excerpts or runtime transcripts.
type PersistedEvidence struct {
	Citations []Citation     `json:"citations"`
	Facts     []EvidenceFact `json:"facts,omitempty"`
}

type StoredReviewItem struct {
	ID                        string               `json:"id"`
	OriginalRecommendationID  string               `json:"original_recommendation_id"`
	OriginalRecommendationIDs []string             `json:"original_recommendation_ids,omitempty"`
	RoleID                    string               `json:"role_id"`
	Operation                 Operation            `json:"operation"`
	Field                     string               `json:"field"`
	OldValue                  any                  `json:"old_value"`
	ProposedValue             any                  `json:"proposed_value"`
	Confidence                Confidence           `json:"confidence"`
	Explanation               string               `json:"explanation"`
	Evidence                  []Citation           `json:"evidence"`
	Safety                    SafetyClassification `json:"safety"`
	Conflict                  string               `json:"conflict,omitempty"`
	Decision                  ReviewDecision       `json:"decision"`
	DecisionAt                *time.Time           `json:"decision_at,omitempty"`
	AppliedAt                 *time.Time           `json:"applied_at,omitempty"`
}

type ReviewEdit struct {
	ItemID   string    `json:"item_id"`
	OldValue any       `json:"old_value"`
	NewValue any       `json:"new_value"`
	EditedAt time.Time `json:"edited_at"`
}

type RoleReview struct {
	SchemaVersion           int                `json:"schema_version"`
	Revision                uint64             `json:"revision"`
	ID                      string             `json:"id"`
	ProjectID               string             `json:"project_id"`
	ProjectName             string             `json:"project_name,omitempty"`
	Repository              RepositoryIdentity `json:"repository"`
	Status                  ReviewStatus       `json:"status"`
	CreatedAt               time.Time          `json:"created_at"`
	UpdatedAt               time.Time          `json:"updated_at"`
	DecidedAt               *time.Time         `json:"decided_at,omitempty"`
	AppliedAt               *time.Time         `json:"applied_at,omitempty"`
	Sources                 []ReviewSource     `json:"sources"`
	OriginalRecommendations []Recommendation   `json:"original_recommendations"`
	Items                   []StoredReviewItem `json:"items"`
	Edits                   []ReviewEdit       `json:"edits,omitempty"`
	Evidence                PersistedEvidence  `json:"evidence"`
}

func (r *RoleReview) normalize() {
	sort.Slice(r.Sources, func(i, j int) bool { return r.Sources[i].Path < r.Sources[j].Path })
	sort.Slice(r.OriginalRecommendations, func(i, j int) bool { return r.OriginalRecommendations[i].ID < r.OriginalRecommendations[j].ID })
	sort.Slice(r.Items, func(i, j int) bool { return r.Items[i].ID < r.Items[j].ID })
	sort.Slice(r.Evidence.Citations, func(i, j int) bool {
		if r.Evidence.Citations[i].Path != r.Evidence.Citations[j].Path {
			return r.Evidence.Citations[i].Path < r.Evidence.Citations[j].Path
		}
		return r.Evidence.Citations[i].Fact < r.Evidence.Citations[j].Fact
	})
	sort.Slice(r.Evidence.Facts, func(i, j int) bool {
		a, b := r.Evidence.Facts[i], r.Evidence.Facts[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		return a.Kind < b.Kind
	})
}

func (r RoleReview) Validate() error {
	if r.SchemaVersion != ReviewSchemaVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedReviewSchema, r.SchemaVersion)
	}
	if r.Revision == 0 || !validReviewID(r.ID) || !validComponent(r.ProjectID) {
		return fmt.Errorf("invalid review identity or revision")
	}
	if r.Repository.Algorithm != "sha256" || !validSHA256(r.Repository.Digest) {
		return fmt.Errorf("invalid repository identity")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || !r.CreatedAt.Equal(r.CreatedAt.UTC()) || !r.UpdatedAt.Equal(r.UpdatedAt.UTC()) || r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("invalid review timestamps")
	}
	if !validStatus(r.Status) {
		return fmt.Errorf("invalid review status %q", r.Status)
	}
	if (r.Status == ReviewStatusApplied) != (r.AppliedAt != nil) {
		return fmt.Errorf("applied status and timestamp disagree")
	}
	for name, stamp := range map[string]*time.Time{"decision": r.DecidedAt, "application": r.AppliedAt} {
		if stamp != nil && (!stamp.Equal(stamp.UTC()) || stamp.Before(r.CreatedAt)) {
			return fmt.Errorf("invalid %s timestamp", name)
		}
	}
	if err := validateSources(r.Sources); err != nil {
		return err
	}
	original := make(map[string]Recommendation, len(r.OriginalRecommendations))
	for _, rec := range r.OriginalRecommendations {
		if rec.ID == "" || original[rec.ID].ID != "" {
			return fmt.Errorf("invalid or duplicate original recommendation %q", rec.ID)
		}
		if !jsonValue(rec.Value) {
			return fmt.Errorf("original recommendation %q contains a non-JSON value", rec.ID)
		}
		original[rec.ID] = rec
	}
	originalItems := make([]ReviewItem, 0, len(r.OriginalRecommendations))
	for _, rec := range r.OriginalRecommendations {
		originalItems = append(originalItems, ReviewItem{Recommendation: rec})
	}
	if _, err := StableReviewPlan(originalItems); err != nil {
		return fmt.Errorf("invalid original recommendations: %w", err)
	}
	seen := map[string]bool{}
	pending, decided := 0, 0
	for _, item := range r.Items {
		if item.ID == "" || seen[item.ID] || item.RoleID == "" || item.Field == "" {
			return fmt.Errorf("invalid or duplicate review item %q", item.ID)
		}
		seen[item.ID] = true
		sourceIDs := item.OriginalRecommendationIDs
		if len(sourceIDs) == 0 {
			sourceIDs = []string{item.OriginalRecommendationID}
		}
		if len(sourceIDs) == 0 || item.OriginalRecommendationID != sourceIDs[0] {
			return fmt.Errorf("review item %q has inconsistent source recommendations", item.ID)
		}
		source, ok := original[sourceIDs[0]]
		if !ok {
			return fmt.Errorf("review item %q has no original recommendation", item.ID)
		}
		for i, sourceID := range sourceIDs {
			if i > 0 && sourceIDs[i-1] >= sourceID {
				return fmt.Errorf("review item %q has unordered or duplicate source recommendations", item.ID)
			}
			source, ok = original[sourceID]
			if !ok || item.RoleID != source.RoleID || item.Field != source.Field || item.Operation != source.Operation {
				return fmt.Errorf("review item %q changes its source recommendation target", item.ID)
			}
		}
		if item.Operation != OperationSet && item.Operation != OperationAppend && item.Operation != OperationRemove {
			return fmt.Errorf("invalid operation for %q", item.ID)
		}
		if item.Decision != ReviewDecisionPending && item.Decision != ReviewDecisionApproved && item.Decision != ReviewDecisionRejected {
			return fmt.Errorf("invalid decision for %q", item.ID)
		}
		if (item.Decision == ReviewDecisionPending) != (item.DecisionAt == nil) {
			return fmt.Errorf("decision timestamp mismatch for %q", item.ID)
		}
		if item.Decision == ReviewDecisionPending {
			pending++
		} else {
			decided++
		}
		if item.Safety != SafetyAddition && item.Safety != SafetyReplacement && item.Safety != SafetyRemoval && item.Safety != SafetyConflict {
			return fmt.Errorf("invalid safety classification for %q", item.ID)
		}
		if item.Operation == OperationRemove && item.Safety != SafetyRemoval && item.Safety != SafetyConflict {
			return fmt.Errorf("removal %q must have removal safety", item.ID)
		}
		if (item.Safety == SafetyConflict) != (item.Conflict != "") {
			return fmt.Errorf("conflict classification mismatch for %q", item.ID)
		}
		if item.DecisionAt != nil && !item.DecisionAt.Equal(item.DecisionAt.UTC()) {
			return fmt.Errorf("decision timestamp for %q is not UTC", item.ID)
		}
		if item.AppliedAt != nil && (item.Decision != ReviewDecisionApproved || !item.AppliedAt.Equal(item.AppliedAt.UTC())) {
			return fmt.Errorf("application timestamp mismatch for %q", item.ID)
		}
		if item.Confidence != ConfidenceLow && item.Confidence != ConfidenceMedium && item.Confidence != ConfidenceHigh {
			return fmt.Errorf("invalid confidence for %q", item.ID)
		}
		if strings.TrimSpace(item.Explanation) == "" || len(item.Evidence) == 0 {
			return fmt.Errorf("review item %q requires explanation and evidence", item.ID)
		}
		if len(item.Explanation) > 8192 {
			return fmt.Errorf("review item %q explanation is too large", item.ID)
		}
		for _, citation := range item.Evidence {
			if citation.Path == "" || filepath.IsAbs(citation.Path) {
				return fmt.Errorf("invalid evidence citation for %q", item.ID)
			}
		}
		if !jsonValue(item.OldValue) || !jsonValue(item.ProposedValue) {
			return fmt.Errorf("review item %q contains a non-JSON value", item.ID)
		}
	}
	for _, edit := range r.Edits {
		if !seen[edit.ItemID] || edit.EditedAt.IsZero() || !edit.EditedAt.Equal(edit.EditedAt.UTC()) || !jsonValue(edit.OldValue) || !jsonValue(edit.NewValue) {
			return fmt.Errorf("invalid review edit for %q", edit.ItemID)
		}
	}
	if (r.Status == ReviewStatusProposed || r.Status == ReviewStatusRefined) && decided != 0 {
		return fmt.Errorf("undecided review status contains decisions")
	}
	if r.Status == ReviewStatusPartiallyDecided && (decided == 0 || pending == 0) {
		return fmt.Errorf("partially decided review must contain pending and decided items")
	}
	if r.Status == ReviewStatusDecided && (pending != 0 || decided == 0 || r.DecidedAt == nil) {
		return fmt.Errorf("decided review must contain only decided items")
	}
	if (r.Status == ReviewStatusApplied || r.Status == ReviewStatusRejected) && (pending != 0 || r.DecidedAt == nil) {
		return fmt.Errorf("terminal review has incomplete decisions")
	}
	if r.Status == ReviewStatusRejected {
		for _, item := range r.Items {
			if item.Decision != ReviewDecisionRejected {
				return fmt.Errorf("rejected review contains a non-rejected item")
			}
		}
	}
	if r.Status == ReviewStatusApplied {
		approved := false
		for _, item := range r.Items {
			approved = approved || item.Decision == ReviewDecisionApproved
		}
		if !approved {
			return fmt.Errorf("applied review contains no approved item")
		}
	}
	if len(r.Evidence.Facts) > 256 {
		return fmt.Errorf("too many persisted evidence facts")
	}
	totalFactBytes := 0
	for _, fact := range r.Evidence.Facts {
		totalFactBytes += len(fact.Path) + len(fact.Key) + len(fact.Value)
		key := strings.ToLower(strings.TrimSpace(fact.Key))
		if len(fact.Value) > 1024 || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "credential") {
			return fmt.Errorf("unsafe or oversized persisted evidence fact %q", fact.Key)
		}
	}
	if totalFactBytes > 64*1024 {
		return fmt.Errorf("persisted evidence facts exceed size limit")
	}
	return nil
}

func validStatus(s ReviewStatus) bool {
	switch s {
	case ReviewStatusProposed, ReviewStatusRefined, ReviewStatusPartiallyDecided, ReviewStatusDecided, ReviewStatusApplied, ReviewStatusRejected, ReviewStatusStale:
		return true
	}
	return false
}

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}
func validComponent(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, `/\\`)
}
func validReviewID(s string) bool {
	return strings.HasPrefix(s, "rev_") && validComponent(s) && len(s) > len("rev_")
}

func validateSources(sources []ReviewSource) error {
	seen := map[string]bool{}
	for _, source := range sources {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(source.Path)))
		if source.Path == "" || filepath.IsAbs(source.Path) || clean == ".." || strings.HasPrefix(clean, "../") || clean != source.Path || seen[source.Path] {
			return fmt.Errorf("invalid or duplicate source path %q", source.Path)
		}
		seen[source.Path] = true
		if source.Algorithm != "sha256" || !validSHA256(source.Hash) {
			return fmt.Errorf("invalid source hash for %q", source.Path)
		}
	}
	return nil
}

func jsonValue(v any) bool { _, err := json.Marshal(v); return err == nil }

func validTransition(from, to ReviewStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case ReviewStatusProposed:
		return to == ReviewStatusRefined || to == ReviewStatusPartiallyDecided || to == ReviewStatusRejected || to == ReviewStatusStale
	case ReviewStatusRefined:
		return to == ReviewStatusPartiallyDecided || to == ReviewStatusDecided || to == ReviewStatusApplied || to == ReviewStatusRejected || to == ReviewStatusStale
	case ReviewStatusPartiallyDecided:
		return to == ReviewStatusDecided || to == ReviewStatusApplied || to == ReviewStatusRejected || to == ReviewStatusStale
	case ReviewStatusDecided:
		return to == ReviewStatusRefined || to == ReviewStatusPartiallyDecided || to == ReviewStatusApplied || to == ReviewStatusRejected || to == ReviewStatusStale
	case ReviewStatusStale:
		return to == ReviewStatusRefined || to == ReviewStatusRejected
	default:
		return false
	}
}

func sameImmutableIdentity(a, b RoleReview) bool {
	return a.ID == b.ID && a.ProjectID == b.ProjectID && reflect.DeepEqual(a.Repository, b.Repository) && a.SchemaVersion == b.SchemaVersion && a.CreatedAt.Equal(b.CreatedAt)
}
