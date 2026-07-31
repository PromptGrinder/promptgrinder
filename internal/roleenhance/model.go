// Package roleenhance contains the runtime-neutral domain used to review and
// safely apply role enhancement recommendations.
package roleenhance

import (
	"fmt"
	"sort"
	"strings"
)

type ProjectSnapshot struct {
	Name        string   `yaml:"name" json:"name"`
	Languages   []string `yaml:"languages" json:"languages"`
	Frameworks  []string `yaml:"frameworks" json:"frameworks"`
	Roles       []string `yaml:"roles" json:"roles"`
	GeneratedBy string   `yaml:"generated_by" json:"generated_by"`
	SourcePath  string   `yaml:"-" json:"source_path"`
	Raw         []byte   `yaml:"-" json:"-"`
}

type RuntimeSnapshot struct {
	Preferred string `yaml:"preferred" json:"preferred"`
}
type StatusSnapshot struct {
	Generated bool `yaml:"generated" json:"generated"`
}

type RoleSnapshot struct {
	ID           string          `yaml:"id" json:"id"`
	Name         string          `yaml:"name" json:"name"`
	Description  string          `yaml:"description" json:"description"`
	Technology   []string        `yaml:"technology" json:"technology"`
	AllowedPaths []string        `yaml:"allowed_paths" json:"allowed_paths"`
	Runtime      RuntimeSnapshot `yaml:"runtime" json:"runtime"`
	QualityGates []string        `yaml:"quality_gates" json:"quality_gates"`
	Status       StatusSnapshot  `yaml:"status" json:"status"`
	SourcePath   string          `yaml:"-" json:"source_path"`
	Raw          []byte          `yaml:"-" json:"-"`
}

type CurrentState struct {
	Project ProjectSnapshot `json:"project"`
	Roles   []RoleSnapshot  `json:"roles"`
}

type EvidenceKind string

const (
	EvidenceRepository    EvidenceKind = "repository"
	EvidenceDocumentation EvidenceKind = "documentation"
	EvidenceSkill         EvidenceKind = "skill"
)

// EvidenceSource contains bounded text; facts are deliberately represented
// separately so an advisor cannot confuse an inferred fact with quoted input.
type EvidenceSource struct {
	Kind      EvidenceKind `json:"kind"`
	Path      string       `json:"path"`
	Excerpt   string       `json:"excerpt,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
}
type EvidenceFact struct {
	Kind  EvidenceKind `json:"kind"`
	Path  string       `json:"path"`
	Key   string       `json:"key"`
	Value string       `json:"value"`
}
type Evidence struct {
	Sources    []EvidenceSource `json:"sources"`
	Facts      []EvidenceFact   `json:"facts"`
	TotalBytes int              `json:"total_bytes"`
}

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

type Operation string

const (
	OperationSet    Operation = "set"
	OperationAppend Operation = "append"
	OperationRemove Operation = "remove"
)

type Citation struct {
	Path string `json:"path" yaml:"path"`
	Fact string `json:"fact,omitempty" yaml:"fact,omitempty"`
}
type Recommendation struct {
	ID          string     `json:"id" yaml:"id"`
	RoleID      string     `json:"role_id" yaml:"role_id"`
	Operation   Operation  `json:"operation" yaml:"operation"`
	Field       string     `json:"field" yaml:"field"`
	Value       any        `json:"value" yaml:"value"`
	Confidence  Confidence `json:"confidence" yaml:"confidence"`
	Explanation string     `json:"explanation" yaml:"explanation"`
	Evidence    []Citation `json:"evidence" yaml:"evidence"`
}
type ReviewItem struct {
	Recommendation Recommendation `json:"recommendation"`
	OldValue       any            `json:"old_value"`
	Approved       bool           `json:"approved"`
}
type ReviewPlan struct {
	Items []ReviewItem `json:"items"`
}
type ApprovalMode string

const (
	ApprovalApplyAll      ApprovalMode = "apply_all"
	ApprovalApplySelected ApprovalMode = "apply_selected"
	ApprovalRejectAll     ApprovalMode = "reject_all"
)

type ApprovalSelection struct {
	Mode              ApprovalMode `json:"mode"`
	RecommendationIDs []string     `json:"recommendation_ids,omitempty"`
}
type MergeChange struct {
	RecommendationID string `json:"recommendation_id"`
	RoleID           string `json:"role_id"`
	Field            string `json:"field"`
	OldValue         any    `json:"old_value"`
	NewValue         any    `json:"new_value"`
}
type MergeConflict struct {
	RecommendationID string `json:"recommendation_id"`
	Reason           string `json:"reason"`
}
type MergeResult struct {
	Applied   []MergeChange   `json:"applied"`
	Rejected  []string        `json:"rejected"`
	Conflicts []MergeConflict `json:"conflicts"`
	Files     []string        `json:"files"`
}

// StableReviewPlan makes advisor ordering irrelevant without changing a
// recommendation's visible content.
func StableReviewPlan(items []ReviewItem) (ReviewPlan, error) {
	out := append([]ReviewItem(nil), items...)
	seen := map[string]bool{}
	for _, item := range out {
		r := item.Recommendation
		if r.ID == "" || r.RoleID == "" || r.Field == "" {
			return ReviewPlan{}, fmt.Errorf("recommendation id, role id, and field are required")
		}
		if seen[r.ID] {
			return ReviewPlan{}, fmt.Errorf("duplicate recommendation id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Operation != OperationSet && r.Operation != OperationAppend && r.Operation != OperationRemove {
			return ReviewPlan{}, fmt.Errorf("invalid operation %q", r.Operation)
		}
		if r.Confidence != ConfidenceLow && r.Confidence != ConfidenceMedium && r.Confidence != ConfidenceHigh {
			return ReviewPlan{}, fmt.Errorf("invalid confidence %q", r.Confidence)
		}
		if r.Explanation == "" || len(r.Evidence) == 0 {
			return ReviewPlan{}, fmt.Errorf("recommendation %q requires explanation and evidence", r.ID)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Recommendation, out[j].Recommendation
		if a.RoleID != b.RoleID {
			return a.RoleID < b.RoleID
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		return a.ID < b.ID
	})
	return ReviewPlan{Items: out}, nil
}

// ValidateRecommendationEvidence rejects citations that the collectors did
// not expose. Callers should perform this before review or merge planning.
func ValidateRecommendationEvidence(recommendations []Recommendation, evidence Evidence) error {
	paths := map[string]bool{}
	facts := map[string]bool{}
	for _, source := range evidence.Sources {
		paths[source.Path] = true
	}
	for _, fact := range evidence.Facts {
		paths[fact.Path] = true
		facts[fact.Path+"\x00"+fact.Key+"\x00"+fact.Value] = true
	}
	for _, recommendation := range recommendations {
		for _, citation := range recommendation.Evidence {
			if !paths[citation.Path] {
				return fmt.Errorf("recommendation %q cites uncollected path %q", recommendation.ID, citation.Path)
			}
			if citation.Fact != "" {
				matched := false
				for key := range facts {
					parts := strings.Split(key, "\x00")
					if parts[0] == citation.Path && (citation.Fact == parts[1] || citation.Fact == parts[2] || citation.Fact == parts[1]+"="+parts[2]) {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("recommendation %q cites uncollected fact %q at %q", recommendation.ID, citation.Fact, citation.Path)
				}
			}
		}
	}
	return nil
}
