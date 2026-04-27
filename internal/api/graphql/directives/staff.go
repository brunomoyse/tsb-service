package directives

import (
	"context"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Staff is an alias for Admin: with the POS device principal collapsed to
// admin scope, every staff-level operation is admin-only. Kept as a separate
// directive so the schema can express intent ("any staff member") without
// pinning callers to admin Zitadel JWTs in the future.
func Staff(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
	userID := utils.GetUserID(ctx)
	if userID == "" {
		return nil, &gqlerror.Error{
			Message:    "UNAUTHENTICATED: please login",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "UNAUTHENTICATED"},
		}
	}
	if err := tokenExpired(ctx); err != nil {
		return nil, err
	}

	if !utils.GetIsAdmin(ctx) {
		return nil, &gqlerror.Error{
			Message:    "FORBIDDEN: staff role required",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}
	}

	return next(ctx)
}
