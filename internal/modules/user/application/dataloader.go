package application

import (
	"context"

	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	orderUserLoaderKey contextKey = "orderUserLoader"
)

type OrderUserLoader struct {
	Loader *db.TypedLoader[*domain.User]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, as UserService) context.Context {
	ctx = context.WithValue(ctx, orderUserLoaderKey, NewOrderUserLoader(as))

	return ctx
}

// NewOrderUserLoader creates a new Order -> User loader.
func NewOrderUserLoader(as UserService) *OrderUserLoader {
	return &OrderUserLoader{
		Loader: db.NewTypedLoader[*domain.User](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.User, error) {
				return as.BatchGetUsersByOrderIDs(ctx, productIDs)
			},
			"failed to fetch useres",
		),
	}
}

// GetOrderUserLoader reads the loader from context.
func GetOrderUserLoader(ctx context.Context) *OrderUserLoader {
	loader, ok := ctx.Value(orderUserLoaderKey).(*OrderUserLoader)
	if !ok {
		return nil
	}
	return loader
}
