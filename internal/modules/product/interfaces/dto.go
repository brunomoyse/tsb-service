package interfaces

import (
	"time"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// CreateProductRequest is used when creating a new product.
type CreateProductRequest struct {
	CategoryID   uuid.UUID            `json:"categoryId" binding:"required"`
	Price        float64              `json:"price" binding:"required"`
	Code         *string              `json:"code"`
	IsActive     bool                 `json:"isActive"`
	IsHalal      bool                 `json:"isHalal"`
	IsVegan      bool                 `json:"isVegan"`
	Translations []domain.Translation `json:"translations" binding:"required"`
}

// UpdateProductRequest is used when updating an existing product.
type UpdateProductRequest struct {
	CategoryID   *uuid.UUID            `json:"categoryId"`
	Price        *float64              `json:"price"`
	Code         *string               `json:"code"`
	IsActive     *bool                 `json:"isActive"`
	IsHalal      *bool                 `json:"isHalal"`
	IsVegan      *bool                 `json:"isVegan"`
	Translations *[]domain.Translation `json:"translations"`
}

// AdminCategoryResponse represents a category for administrative views,
// including all translations.
type AdminCategoryResponse struct {
	ID           uuid.UUID            `json:"id"`
	Order        int                  `json:"order"`
	Translations []domain.Translation `json:"translations"`
}

// AdminProductResponse is returned for admin endpoints, containing all product details.
type AdminProductResponse struct {
	ID           uuid.UUID            `json:"id"`
	Price        float64              `json:"price"`
	Code         *string              `json:"code"`
	Slug         *string              `json:"slug"`
	IsActive     bool                 `json:"isActive"`
	IsHalal      bool                 `json:"isHalal"`
	IsVegan      bool                 `json:"isVegan"`
	CategoryID   uuid.UUID            `json:"categoryId"`
	Translations []domain.Translation `json:"translations"`
}

// PublicCategoryResponse represents a flattened category with its selected translation.
type PublicCategoryResponse struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Order int       `json:"order"`
}

// PublicProductResponse is returned to public users, merging product data with the selected translation.
type PublicProductResponse struct {
	ID          uuid.UUID `json:"id"`
	Price       float64   `json:"price"`
	Code        *string   `json:"code,omitempty"`
	Slug        *string   `json:"slug,omitempty"`
	IsActive    bool      `json:"isActive"`
	IsHalal     bool      `json:"isHalal"`
	IsVegan     bool      `json:"isVegan"`
	CategoryID  uuid.UUID `json:"categoryId"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NewPublicCategoryResponse builds a PublicCategoryResponse from a domain.Category,
// selecting the translation based on userLanguage.
func NewPublicCategoryResponse(category *domain.Category, userLanguage string) *PublicCategoryResponse {
	translation := category.GetTranslationFor(userLanguage)
	var name string
	if translation != nil {
		name = translation.Name
	}
	return &PublicCategoryResponse{
		ID:    category.ID,
		Name:  name,
		Order: category.Order,
	}
}

// NewPublicProductResponse builds a PublicProductResponse from a domain.Product,
// merging product fields with the selected translation based on userLanguage.
func NewPublicProductResponse(product *domain.Product, userLanguage string) *PublicProductResponse {
	translation := product.GetTranslationFor(userLanguage)
	return &PublicProductResponse{
		ID:          product.ID,
		Price:       product.Price,
		Code:        product.Code,
		Slug:        product.Slug,
		IsActive:    product.IsActive,
		IsHalal:     product.IsHalal,
		IsVegan:     product.IsVegan,
		CategoryID:  product.CategoryID,
		Name:        translation.Name,
		Description: translation.Description,
		CreatedAt:   product.CreatedAt,
		UpdatedAt:   product.UpdatedAt,
	}
}

// NewAdminProductResponse builds an AdminProductResponse from a domain.Product.
func NewAdminProductResponse(product *domain.Product) *AdminProductResponse {
	return &AdminProductResponse{
		ID:           product.ID,
		Price:        product.Price,
		Code:         product.Code,
		Slug:         product.Slug,
		IsActive:     product.IsActive,
		IsHalal:      product.IsHalal,
		IsVegan:      product.IsVegan,
		CategoryID:   product.CategoryID,
		Translations: product.Translations,
	}
}
