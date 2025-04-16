// internal/modules/product/application/dataloader.go
package application

import (
	"context"

	"tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	productCategoryLoaderKey contextKey = "productCategoryLoader"
)

type ProductProductCategoryLoader struct {
	Loader *db.TypedLoader[*domain.Category]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, ps ProductService) context.Context {
	ctx = context.WithValue(ctx, productCategoryLoaderKey, NewProductProductCategoryLoader(ps))
	return ctx
}

// NewProductProductCategoryLoader creates a new Product -> Category loader.
// Note how we capture `ps` in a closure so it calls the real service method.
func NewProductProductCategoryLoader(ps ProductService) *ProductProductCategoryLoader {
	return &ProductProductCategoryLoader{
		Loader: db.NewTypedLoader[*domain.Category](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error) {
				return ps.BatchGetCategoriesByProductIDs(ctx, productIDs)
			},
			"failed to fetch categories",
		),
	}
}

// GetProductProductCategoryLoader reads the loader from context.
func GetProductProductCategoryLoader(ctx context.Context) *ProductProductCategoryLoader {
	loader, ok := ctx.Value(productCategoryLoaderKey).(*ProductProductCategoryLoader)
	if !ok {
		return nil
	}
	return loader
}
