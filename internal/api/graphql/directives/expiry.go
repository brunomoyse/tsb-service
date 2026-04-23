package directives

import (
	"context"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"tsb-service/pkg/utils"
)

// tokenExpired returns an UNAUTHENTICATED gqlerror when the caller's access
// token has passed its exp claim, or nil when the token is still valid (or
// when no expiry was recorded — e.g. public endpoints, tests).
//
// For WebSocket subscriptions the transport-level ctx already carries a
// deadline bound to the same exp (see resolver.InitFunc), so clients see
// graceful disconnects. This guard exists for HTTP queries/mutations where
// the middleware captured exp from an accepted JWT but the directive fires
// milliseconds/seconds later — and for defense-in-depth if middleware ever
// forgets to re-check expiry.
func tokenExpired(ctx context.Context) *gqlerror.Error {
	exp := utils.GetTokenExpiry(ctx)
	if exp.IsZero() || time.Now().Before(exp) {
		return nil
	}
	return &gqlerror.Error{
		Message:    "UNAUTHENTICATED: token expired",
		Path:       graphql.GetPath(ctx),
		Extensions: map[string]any{"code": "UNAUTHENTICATED"},
	}
}
