package directives

import (
	"context"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Staff checks that the caller is authenticated and holds either the staff
// role (POS device) or the admin role (Zitadel). Admin is a superset of staff.
func Staff(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
	userID := utils.GetUserID(ctx)
	if userID == "" {
		return nil, &gqlerror.Error{
			Message:    "UNAUTHENTICATED: please login",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "UNAUTHENTICATED"},
		}
	}

	if !utils.GetIsStaff(ctx) {
		return nil, &gqlerror.Error{
			Message:    "FORBIDDEN: staff role required",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}
	}

	return next(ctx)
}
