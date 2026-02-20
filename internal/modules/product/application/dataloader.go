// internal/modules/product/application/dataloader.go
package application

import (
	"context"

	"tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	productCategoryLoaderKey  contextKey = "productCategoryLoader"
	categoryProductLoaderKey  contextKey = "categoryProductLoader"
	orderItemProductLoaderKey contextKey = "orderItemProductLoader"
	categoryTranslation       contextKey = "categoryTranslation"
	productTranslation        contextKey = "productTranslation"
	productChoiceLoaderKey    contextKey = "productChoiceLoader"
)

type ProductCategoryLoader struct {
	Loader *db.TypedLoader[*domain.Category]
}

type CategoryProduct struct {
	Loader *db.TypedLoader[*domain.Product]
}

type OrderItemProduct struct {
	Loader *db.TypedLoader[*domain.Product]
}

type CategoryTranslation struct {
	Loader *db.TypedLoader[*domain.Translation]
}

type ProductTranslation struct {
	Loader *db.TypedLoader[*domain.Translation]
}

type ProductChoiceLoader struct {
	Loader *db.TypedLoader[*domain.ProductChoice]
}

// AttachDataLoaders attaches all necessary DataLoaders for products to the context.
func AttachDataLoaders(ctx context.Context, ps ProductService) context.Context {
	ctx = context.WithValue(ctx, productCategoryLoaderKey, NewProductCategoryLoader(ps))
	ctx = context.WithValue(ctx, categoryProductLoaderKey, NewCategoryProductLoader(ps))
	ctx = context.WithValue(ctx, orderItemProductLoaderKey, NewOrderItemProductLoader(ps))
	ctx = context.WithValue(ctx, categoryTranslation, NewCategoryTranslation(ps))
	ctx = context.WithValue(ctx, productTranslation, NewProductTranslation(ps))
	ctx = context.WithValue(ctx, productChoiceLoaderKey, NewProductChoiceLoader(ps))
	return ctx
}

// NewProductCategoryLoader creates a new Product -> Category loader.
// Note how we capture `ps` in a closure so it calls the real service method.
func NewProductCategoryLoader(ps ProductService) *ProductCategoryLoader {
	return &ProductCategoryLoader{
		Loader: db.NewTypedLoader[*domain.Category](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error) {
				return ps.BatchGetCategoriesByProductIDs(ctx, productIDs)
			},
			"failed to fetch categories",
		),
	}
}

func NewCategoryProductLoader(ps ProductService) *CategoryProduct {
	return &CategoryProduct{
		Loader: db.NewTypedLoader[*domain.Product](
			func(ctx context.Context, categoryIDs []string) (map[string][]*domain.Product, error) {
				return ps.BatchGetProductsByCategory(ctx, categoryIDs)
			},
			"failed to fetch products",
		),
	}
}

func NewOrderItemProductLoader(ps ProductService) *OrderItemProduct {
	return &OrderItemProduct{
		Loader: db.NewTypedLoader[*domain.Product](
			func(ctx context.Context, orderItemIDs []string) (map[string][]*domain.Product, error) {
				return ps.BatchGetProductByIDs(ctx, orderItemIDs)
			},
			"failed to fetch products",
		),
	}
}

func NewCategoryTranslation(ps ProductService) *CategoryTranslation {
	return &CategoryTranslation{
		Loader: db.NewTypedLoader[*domain.Translation](
			func(ctx context.Context, categoryIDs []string) (map[string][]*domain.Translation, error) {
				return ps.BatchGetCategoryTranslations(ctx, categoryIDs)
			},
			"failed to fetch translations",
		),
	}
}

func NewProductTranslation(ps ProductService) *ProductTranslation {
	return &ProductTranslation{
		Loader: db.NewTypedLoader[*domain.Translation](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.Translation, error) {
				return ps.BatchGetProductTranslations(ctx, productIDs)
			},
			"failed to fetch translations",
		),
	}
}

func NewProductChoiceLoader(ps ProductService) *ProductChoiceLoader {
	return &ProductChoiceLoader{
		Loader: db.NewTypedLoader[*domain.ProductChoice](
			func(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoice, error) {
				return ps.BatchGetChoicesByProductIDs(ctx, productIDs)
			},
			"failed to fetch product choices",
		),
	}
}

// GetProductCategoryLoader reads the loader from context.
func GetProductCategoryLoader(ctx context.Context) *ProductCategoryLoader {
	loader, ok := ctx.Value(productCategoryLoaderKey).(*ProductCategoryLoader)
	if !ok {
		return nil
	}
	return loader
}

// GetCategoryProductLoader reads the loader from context.
func GetCategoryProductLoader(ctx context.Context) *CategoryProduct {
	loader, ok := ctx.Value(categoryProductLoaderKey).(*CategoryProduct)
	if !ok {
		return nil
	}
	return loader
}

// GetOrderItemProductLoader reads the loader from context.
func GetOrderItemProductLoader(ctx context.Context) *OrderItemProduct {
	loader, ok := ctx.Value(orderItemProductLoaderKey).(*OrderItemProduct)
	if !ok {
		return nil
	}
	return loader
}

// GetCategoryTranslationLoader reads the loader from context.
func GetCategoryTranslationLoader(ctx context.Context) *CategoryTranslation {
	loader, ok := ctx.Value(categoryTranslation).(*CategoryTranslation)
	if !ok {
		return nil
	}
	return loader
}

// GetProductTranslationLoader reads the loader from context.
func GetProductTranslationLoader(ctx context.Context) *ProductTranslation {
	loader, ok := ctx.Value(productTranslation).(*ProductTranslation)
	if !ok {
		return nil
	}
	return loader
}

// GetProductChoiceLoader reads the loader from context.
func GetProductChoiceLoader(ctx context.Context) *ProductChoiceLoader {
	loader, ok := ctx.Value(productChoiceLoaderKey).(*ProductChoiceLoader)
	if !ok {
		return nil
	}
	return loader
}
