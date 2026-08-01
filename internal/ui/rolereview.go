package ui

import (
	"encoding/json"
	"fmt"
	"io"

	"promptgrinder/internal/roleenhance"
)

// RoleReviewItem renders the exact stored values used by role-review decisions.
func RoleReviewItem(w io.Writer, item roleenhance.StoredReviewItem, opts Options) {
	classification := string(item.Safety)
	if !opts.Plain {
		color := themeColor(opts.Theme)
		if item.Safety == roleenhance.SafetyRemoval {
			color = "\033[31m"
		}
		if item.Safety == roleenhance.SafetyReplacement || item.Safety == roleenhance.SafetyConflict {
			color = "\033[33m"
		}
		classification = color + classification + "\033[0m"
	}
	oldValue, _ := json.Marshal(item.OldValue)
	newValue, _ := json.Marshal(item.ProposedValue)
	fmt.Fprintf(w, "\n[%s] %s.%s (%s, %s)\n  Item ID: %s\n  Old: %s\n  Proposed: %s\n  Confidence: %s\n  Decision: %s\n  Reason: %s\n",
		item.OriginalRecommendationID, item.RoleID, item.Field, item.Operation, classification,
		item.ID, oldValue, newValue, item.Confidence, item.Decision, item.Explanation)
	if item.Conflict != "" {
		fmt.Fprintf(w, "  Conflict: %s\n", item.Conflict)
	}
	for _, citation := range item.Evidence {
		fmt.Fprintf(w, "  Evidence: %s", citation.Path)
		if citation.Fact != "" {
			fmt.Fprintf(w, " (%s)", citation.Fact)
		}
		fmt.Fprintln(w)
	}
}

func RoleReviewMenu(w io.Writer) {
	fmt.Fprintln(w, "\nActions:")
	fmt.Fprintln(w, "  [a] Apply safe changes")
	fmt.Fprintln(w, "  [r] Review changes one by one")
	fmt.Fprintln(w, "  [e] Edit recommendations")
	fmt.Fprintln(w, "  [x] Reject all")
	fmt.Fprintln(w, "  [s] Save for later")
}
