package directives

import (
	"context"
	"tsb-service/pkg/utils"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Staff admits admins and POS devices. It guards the shop-floor surface the
// POS handheld needs (orders, order updates, payment status, coupons); every
// other privileged operation stays @admin, which POS devices cannot pass.
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

	if !utils.GetIsStaff(ctx) {
		return nil, &gqlerror.Error{
			Message:    "FORBIDDEN: staff role required",
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}
	}

	return next(ctx)
}
