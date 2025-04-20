// internal/api/graphql/directives/auth.go
package directives

import (
	"context"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Admin checks for a "user" value in ctx and verifies if the user is an admin.
func Admin(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	// 1) Must be authenticated
	userID := utils.GetUserID(ctx)
	if userID == "" {
		err := &gqlerror.Error{
			Message:    "UNAUTHENTICATED: please login",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]interface{}{"code": "UNAUTHENTICATED"},
		}
		return nil, err
	}

	// 2) Must be admin
	if !utils.GetIsAdmin(ctx) {
		err := &gqlerror.Error{
			Message:    "FORBIDDEN: you are not an admin",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]interface{}{"code": "FORBIDDEN"},
		}
		return nil, err
	}

	// 3) All good → continue to the resolver
	return next(ctx)
}
