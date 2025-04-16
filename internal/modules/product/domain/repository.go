package domain

import (
	"context"
)

// ProductRepository defines the contract for persisting Product aggregates.
type ProductRepository interface {
	Create(ctx context.Context, product *Product) error
	Update(ctx context.Context, product *Product) error
	FindByID(ctx context.Context, id string) (*Product, error)
	FindAll(ctx context.Context) ([]*Product, error)
	FindByCategoryID(ctx context.Context, categoryID string) ([]*Product, error)
	FindAllCategories(ctx context.Context) ([]*Category, error)
	FindByIDs(ctx context.Context, productIDs []string) ([]*ProductOrderDetails, error)

	FindCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*Category, error)
}
