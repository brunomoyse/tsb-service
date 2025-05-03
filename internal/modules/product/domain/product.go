package domain

import (
	"errors"
	"github.com/shopspring/decimal"
	"time"

	"github.com/google/uuid"
)

// Product represents the core product aggregate.
type Product struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	Price          decimal.Decimal `db:"price" json:"price"`
	Code           *string         `db:"code" json:"code"`
	Slug           *string         `db:"slug" json:"slug"`
	PieceCount     *int            `db:"piece_count" json:"pieceCount"`
	IsVisible      bool            `db:"is_visible" json:"isVisible"`
	IsAvailable    bool            `db:"is_available" json:"isAvailable"`
	IsHalal        bool            `db:"is_halal" json:"isHalal"`
	IsVegan        bool            `db:"is_vegan" json:"isVegan"`
	IsDiscountable bool            `db:"is_discountable" json:"isDiscountable"`
	CategoryID     uuid.UUID       `db:"category_id" json:"categoryId"`
	CreatedAt      time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updatedAt"`
	Translations   []Translation   `json:"translations"`
}

type ProductOrderDetails struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	Code           *string         `db:"code" json:"code"`
	CategoryName   string          `db:"category_name" json:"categoryName"`
	Name           string          `db:"name" json:"name"`
	Price          decimal.Decimal `db:"price" json:"price"`
	IsDiscountable bool            `db:"is_discountable" json:"isDiscountable"`
}

func NewProduct(price decimal.Decimal, categoryID uuid.UUID, isVisible bool, isAvailable bool, translations []Translation) (*Product, error) {
	if isVisible {
		// For visible products, require at least 3 translations.
		if len(translations) < 3 {
			return nil, errors.New("visible product must have at least 3 translations")
		}
	} else {
		// For inactive products, ensure at least one translation is French.
		// Always require at least one translation.
		if len(translations) == 0 {
			return nil, errors.New("at least one translation is required")
		}
	}

	return &Product{
		ID:           uuid.New(),
		Price:        price,
		IsVisible:    isVisible,
		IsAvailable:  isAvailable,
		CategoryID:   categoryID,
		Translations: translations,
	}, nil
}

// GetTranslationFor returns the translation matching the given language,
// or falls back to the first available translation if no exact match is found.
func (p *Product) GetTranslationFor(language string) *Translation {
	for _, t := range p.Translations {
		if t.Language == language {
			return &t
		}
	}
	if len(p.Translations) > 0 {
		return &p.Translations[0]
	}
	return nil
}
