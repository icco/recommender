package recommend

import "context"

// tasteProfile summarizes the user's taste from stored signals. Phase 2 fills
// this in; until then it returns an empty profile.
func (r *Recommender) tasteProfile(ctx context.Context) (string, error) {
	return "", nil
}
