package roleenhance

// RoleDiffGenerator turns validated recommendations into a review plan while
// capturing the exact values that were observed during review.
type RoleDiffGenerator struct{}

func (RoleDiffGenerator) Generate(current CurrentState, recommendations []Recommendation) (ReviewPlan, error) {
	roles := roleIndex(current)
	items := make([]ReviewItem, len(recommendations))
	for i, recommendation := range recommendations {
		items[i] = ReviewItem{Recommendation: recommendation, OldValue: fieldValue(roles[recommendation.RoleID], recommendation.Field)}
	}
	return StableReviewPlan(items)
}
