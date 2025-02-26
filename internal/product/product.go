package product

import "github.com/google/uuid"

type ProductInfo struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Price       float64   `json:"price"`
	Code        *string   `json:"code"`
	Slug        *string   `json:"slug"`
	IsActive    bool      `json:"isActive"`
	IsHalal     bool      `json:"isHalal"`
	IsVegan     bool      `json:"isVegan"`
}

type DashboardProductListItem struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Code         *string   `json:"code"`
	IsActive     bool      `json:"isActive"`
	IsHalal      bool      `json:"isHalal"`
	IsVegan      bool      `json:"isVegan"`
	CategoryName string    `json:"category"`
}

type DashboardCategoryDetails struct {
	ID           uuid.UUID              `json:"id"`
	Translations []*CategoryTranslation `json:"translations"`
}

type DashboardProductDetails struct {
	ID           uuid.UUID             `json:"id"`
	Translations []*ProductTranslation `json:"translations"`
	Price        float64               `json:"price"`
	Code         *string               `json:"code"`
	Slug         *string               `json:"slug"`
	IsActive     bool                  `json:"isActive"`
	IsHalal      bool                  `json:"isHalal"`
	IsVegan      bool                  `json:"isVegan"`
	CategoryId   uuid.UUID             `json:"categoryId"`
}

type Category struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Order int       `json:"order"`
}

type CategoryWithProducts struct {
	ID       uuid.UUID     `json:"id"`
	Name     string        `json:"name"`
	Order    int           `json:"order"`
	Products []ProductInfo `json:"products"`
}

type UpdateProductForm struct {
	CategoryId   *uuid.UUID            `json:"categoryId"`
	Price        *float64              `json:"price"`
	Code         *string               `json:"code"`
	IsActive     *bool                 `json:"isActive"`
	IsHalal      *bool                 `json:"isHalal"`
	IsVegan      *bool                 `json:"isVegan"`
	Translations []*ProductTranslation `json:"translations"`
}

type CreateProductForm struct {
	CategoryId   *uuid.UUID           `json:"categoryId" binding:"required"`
	Price        float64              `json:"price" binding:"required"`
	Code         *string              `json:"code"`
	IsActive     bool                 `json:"isActive"`
	IsHalal      bool                 `json:"isHalal"`
	IsVegan      bool                 `json:"isVegan"`
	Translations []ProductTranslation `json:"translations" binding:"required"`
}

type CategoryTranslation struct {
	Locale string `json:"locale" binding:"required"`
	Name   string `json:"name" binding:"required"`
}

type ProductTranslation struct {
	Locale      string  `json:"locale" binding:"required"`
	Name        string  `json:"name" binding:"required"`
	Description *string `json:"description"`
}

type ProductFormResponse struct {
	ID           uuid.UUID            `json:"id"`
	Price        float64              `json:"price"`
	Code         *string              `json:"code"`
	Slug         *string              `json:"slug"`
	IsActive     bool                 `json:"isActive"`
	IsHalal      bool                 `json:"isHalal"`
	IsVegan      bool                 `json:"isVegan"`
	CategoryId   uuid.UUID            `json:"categoryId"`
	Translations []ProductTranslation `json:"translations"`
}
