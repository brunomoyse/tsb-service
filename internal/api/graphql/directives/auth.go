// internal/api/graphql/directives/auth.go
package directives

import (
	"context"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
)

// Auth checks for a "user" value in ctx
// If missing, it aborts with an error; otherwise it proceeds to the next resolver.
func Auth(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
	// 1) Must be authenticated
	userID := utils.GetUserID(ctx)
	if userID == "" {
		err := &gqlerror.Error{
			Message:    "UNAUTHENTICATED: please login",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "UNAUTHENTICATED"},
		}
		return nil, err
	}
	return next(ctx)
}
