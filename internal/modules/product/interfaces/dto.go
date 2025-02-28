package interfaces

import (
	"time"
	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// CreateProductForm is used when creating a new product.
type CreateProductForm struct {
	CategoryID   uuid.UUID            `json:"categoryId" binding:"required"`
	Price        float64              `json:"price" binding:"required"`
	Code         *string              `json:"code"`
	IsActive     bool                 `json:"isActive"`
	IsHalal      bool                 `json:"isHalal"`
	IsVegan      bool                 `json:"isVegan"`
	Translations []domain.Translation `json:"translations" binding:"required"`
}

// UpdateProductForm is used when updating an existing product.
type UpdateProductForm struct {
	CategoryID   *uuid.UUID            `json:"categoryId"`
	Price        *float64              `json:"price"`
	Code         *string               `json:"code"`
	IsActive     *bool                 `json:"isActive"`
	IsHalal      *bool                 `json:"isHalal"`
	IsVegan      *bool                 `json:"isVegan"`
	Translations *[]domain.Translation `json:"translations"`
}

type AdminCategoryResponse struct {
	ID           uuid.UUID            `json:"id"`
	Order        int                  `json:"order"`
	Translations []domain.Translation `json:"translations"`
}

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

// CategoryResponse represents the flattened category details with its selected translation.
type PublicCategoryResponse struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Order int       `json:"order"`
}

// ProductResponse contains product fields merged with its selected translation and the translated category.
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

// NewCategoryResponse builds a CategoryResponse from a domain.Category by selecting the translation for the user's language.
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

// NewProductResponse builds a ProductResponse from a domain.Product by selecting the translations based on userLanguage.
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

// NewAdminProductResponse builds a AdminProductResponse from a domain.Product
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
