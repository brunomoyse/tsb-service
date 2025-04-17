package application

import (
	"context"

	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	userOrderLoaderKey contextKey = "userOrderLoader"
)

type UserOrderLoader struct {
	Loader *db.TypedLoader[*domain.Order]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, os OrderService) context.Context {
	ctx = context.WithValue(ctx, userOrderLoaderKey, NewUserOrderLoader(os))

	return ctx
}

// NewUserOrderLoader creates a new User -> Order loader.
func NewUserOrderLoader(os OrderService) *UserOrderLoader {
	return &UserOrderLoader{
		Loader: db.NewTypedLoader[*domain.Order](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Order, error) {
				return os.BatchGetOrdersByUserIDs(ctx, productIDs)
			},
			"failed to fetch addresses",
		),
	}
}

// GetUserOrderLoader reads the loader from context.
func GetUserOrderLoader(ctx context.Context) *UserOrderLoader {
	loader, ok := ctx.Value(userOrderLoaderKey).(*UserOrderLoader)
	if !ok {
		return nil
	}
	return loader
}
