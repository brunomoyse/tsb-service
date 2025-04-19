package application

import (
	"context"

	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	userOrderLoaderKey contextKey = "userOrderLoader"
	orderItemLoaderKey contextKey = "orderItemLoader"
)

type UserOrderLoader struct {
	Loader *db.TypedLoader[*domain.Order]
}

type OrderItemLoader struct {
	Loader *db.TypedLoader[*domain.OrderProductRaw]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, os OrderService) context.Context {
	ctx = context.WithValue(ctx, userOrderLoaderKey, NewUserOrderLoader(os))
	ctx = context.WithValue(ctx, orderItemLoaderKey, NewOrderItemLoader(os))

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

// NewOrderItemLoader creates a new Order -> OrderProduct loader.
func NewOrderItemLoader(os OrderService) *OrderItemLoader {
	return &OrderItemLoader{
		Loader: db.NewTypedLoader[*domain.OrderProductRaw](
			func(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error) {
				return os.BatchGetOrderProductsByOrderIDs(ctx, orderIDs)
			},
			"failed to fetch order items",
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

// GetOrderItemLoader reads the loader from context.
func GetOrderItemLoader(ctx context.Context) *OrderItemLoader {
	loader, ok := ctx.Value(orderItemLoaderKey).(*OrderItemLoader)
	if !ok {
		return nil
	}
	return loader
}
