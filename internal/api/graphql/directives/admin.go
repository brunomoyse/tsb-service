// internal/api/graphql/directives/auth.go
package directives

import (
	"context"
	"fmt"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
)

// Admin checks for a "user" value in ctx and verifies if the user is an admin.
func Admin(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	userID := utils.GetUserID(ctx)
	if userID == "" {
		return nil, fmt.Errorf("UNAUTHENTICATED: please login")
	}

	isAdmin := utils.GetIsAdmin(ctx)
	if !isAdmin {
		return nil, fmt.Errorf("UNAUTHORIZED: you are not an admin")
	}

	return next(ctx)
}
