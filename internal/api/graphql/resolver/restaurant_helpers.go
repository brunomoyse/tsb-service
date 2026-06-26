package resolver

import (
	"context"

	"tsb-service/internal/api/auth"
	"tsb-service/pkg/utils"
)

// isReviewContextUser reports whether the authenticated caller is a store-review
// account (Google Play / App Store reviewer). Such accounts are allowed to order
// outside opening hours so a reviewer can validate checkout at any time. Returns
// false for anonymous callers. TEMPORARY (revert after launch).
//
// Lives here (a non-schema file) rather than in the generated restaurant.go so
// gqlgen regeneration doesn't move it into a dead comment block.
func (r *Resolver) isReviewContextUser(ctx context.Context) bool {
	userID := utils.GetUserID(ctx)
	if userID == "" {
		return false
	}
	user, err := r.UserService.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return false
	}
	return auth.IsReviewUser(user.Email, user.FirstName, user.LastName)
}
