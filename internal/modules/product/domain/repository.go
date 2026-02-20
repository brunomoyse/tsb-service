package domain

import (
	"context"
	"github.com/google/uuid"
)

// ProductRepository defines the contract for persisting Product aggregates.
type ProductRepository interface {
	Create(ctx context.Context, product *Product) error
	Update(ctx context.Context, product *Product) error
	FindByID(ctx context.Context, id uuid.UUID) (*Product, error)
	FindAll(ctx context.Context) ([]*Product, error)
	FindByCategoryID(ctx context.Context, categoryID string) ([]*Product, error)
	FindAllCategories(ctx context.Context) ([]*Category, error)
	FindCategoryByID(ctx context.Context, id uuid.UUID) (*Category, error)
	FindByIDs(ctx context.Context, productIDs []string) ([]*ProductOrderDetails, error)

	FindCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*Category, error)
	FindByCategoryIDs(ctx context.Context, categoryIDs []string) (map[string][]*Product, error)
	BatchGetProductByIDs(ctx context.Context, productIDs []string) (map[string][]*Product, error)
	BatchGetCategoryTranslations(ctx context.Context, categoryIDs []string) (map[string][]*Translation, error)
	BatchGetProductTranslations(ctx context.Context, productIDs []string) (map[string][]*Translation, error)

	// Product choices
	FindChoicesByProductID(ctx context.Context, productID uuid.UUID) ([]*ProductChoice, error)
	FindChoiceByID(ctx context.Context, choiceID uuid.UUID) (*ProductChoice, error)
	BatchGetChoicesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*ProductChoice, error)
	CreateChoice(ctx context.Context, choice *ProductChoice) error
	UpdateChoice(ctx context.Context, choice *ProductChoice) error
	DeleteChoice(ctx context.Context, choiceID uuid.UUID) error
}
