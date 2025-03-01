package domain

import "context"

// ProductRepository defines the contract for persisting Product aggregates.
type ProductRepository interface {
	Save(ctx context.Context, product *Product) error
	FindByID(ctx context.Context, id string) (*Product, error)
	FindAll(ctx context.Context) ([]*Product, error)
	FindByCategoryID(ctx context.Context, categoryID string) ([]*Product, error)
	FindAllCategories(ctx context.Context) ([]*Category, error)
}
