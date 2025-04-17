package resolver

import (
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/modules/product/domain"
)

// Map applies fn to every element of in, returning a new slice.
func Map[T any, U any](in []T, fn func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// ToGQLProduct converts a domain.Product into the GraphQL model.Product.
func ToGQLProduct(p *domain.Product, lang string) *model.Product {
	return &model.Product{
		ID:          p.ID,
		CreatedAt:   p.CreatedAt,
		Price:       p.Price.String(),
		Code:        p.Code,
		Slug:        *p.Slug,
		PieceCount:  p.PieceCount,
		IsVisible:   p.IsVisible,
		IsAvailable: p.IsAvailable,
		IsHalal:     p.IsHalal,
		IsVegan:     p.IsVegan,
		Name:        p.GetTranslationFor(lang).Name,
		Description: p.GetTranslationFor(lang).Description,
	}
}

// ToGQLProductCategory converts a domain.Category into the GraphQL model.ProductCategory.
func ToGQLProductCategory(c *domain.Category, lang string) *model.ProductCategory {
	return &model.ProductCategory{
		ID:    c.ID,
		Name:  c.GetTranslationFor(lang).Name,
		Order: c.Order,
	}
}
