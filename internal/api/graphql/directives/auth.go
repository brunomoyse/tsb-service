// internal/api/graphql/directives/auth.go
package directives

import (
	"context"
	"fmt"

	"github.com/99designs/gqlgen/graphql"
)

// Auth checks for a "user" value in ctx
// If missing, it aborts with an error; otherwise it proceeds to the next resolver.
func Auth(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	if ctx.Value("user") == nil {
		return nil, fmt.Errorf("UNAUTHENTICATED: please login")
	}
	return next(ctx)
}
