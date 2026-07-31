package roleenhance

import (
	"encoding/json"
	"fmt"
	"io"
)

type ReviewRenderer struct{}

func (ReviewRenderer) Render(w io.Writer, plan ReviewPlan) error {
	if _, err := fmt.Fprintln(w, "Role enhancement review"); err != nil {
		return err
	}
	if len(plan.Items) == 0 {
		_, err := fmt.Fprintln(w, "No recommendations.")
		return err
	}
	for _, item := range plan.Items {
		r := item.Recommendation
		fmt.Fprintf(w, "\n[%s] %s — %s (%s)\n", r.ID, r.RoleID, r.Field, r.Operation)
		fmt.Fprintf(w, "  Old: %s\n", displayValue(item.OldValue))
		fmt.Fprintf(w, "  Proposed: %s\n", displayValue(r.Value))
		fmt.Fprintf(w, "  Confidence: %s\n", r.Confidence)
		fmt.Fprintf(w, "  Reason: %s\n", r.Explanation)
		fmt.Fprintln(w, "  Evidence:")
		for _, citation := range r.Evidence {
			if citation.Fact == "" {
				fmt.Fprintf(w, "    - %s\n", citation.Path)
			} else {
				fmt.Fprintf(w, "    - %s (%s)\n", citation.Path, citation.Fact)
			}
		}
	}
	return nil
}

func displayValue(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(b)
}
