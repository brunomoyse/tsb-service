package application

import (
	"context"

	"tsb-service/internal/modules/address/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	orderAddressLoaderKey contextKey = "orderAddressLoader"
	userAddressLoaderKey  contextKey = "userAddressLoader"
)

type OrderAddressLoader struct {
	Loader *db.TypedLoader[*domain.Address]
}

type UserAddressLoader struct {
	Loader *db.TypedLoader[*domain.Address]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, as AddressService) context.Context {
	ctx = context.WithValue(ctx, orderAddressLoaderKey, NewOrderAddressLoader(as))
	ctx = context.WithValue(ctx, userAddressLoaderKey, NewUserAddressLoader(as))

	return ctx
}

// NewOrderAddressLoader creates a new Order -> Address loader.
func NewOrderAddressLoader(as AddressService) *OrderAddressLoader {
	return &OrderAddressLoader{
		Loader: db.NewTypedLoader[*domain.Address](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Address, error) {
				return as.BatchGetAddressesByOrderIDs(ctx, productIDs)
			},
			"failed to fetch addresses",
		),
	}
}

// NewUserAddressLoader creates a new User -> Address loader.
func NewUserAddressLoader(as AddressService) *UserAddressLoader {
	return &UserAddressLoader{
		Loader: db.NewTypedLoader[*domain.Address](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Address, error) {
				return as.BatchGetAddressesByUserIDs(ctx, productIDs)
			},
			"failed to fetch addresses",
		),
	}
}

// GetOrderAddressLoader reads the loader from context.
func GetOrderAddressLoader(ctx context.Context) *OrderAddressLoader {
	loader, ok := ctx.Value(orderAddressLoaderKey).(*OrderAddressLoader)
	if !ok {
		return nil
	}
	return loader
}

// GetUserAddressLoader reads the loader from context.
func GetUserAddressLoader(ctx context.Context) *UserAddressLoader {
	loader, ok := ctx.Value(userAddressLoaderKey).(*UserAddressLoader)
	if !ok {
		return nil
	}
	return loader
}
