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
	IsVegetarian   bool            `db:"is_vegetarian" json:"isVegetarian"`
	IsSpicy        bool            `db:"is_spicy" json:"isSpicy"`
	IsDiscountable bool            `db:"is_discountable" json:"isDiscountable"`
	VatCategory    VatCategory     `db:"vat_category" json:"vatCategory"`
	CategoryID     uuid.UUID       `db:"category_id" json:"categoryId"`
	CreatedAt      time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updatedAt"`
	Translations   []Translation   `json:"translations"`
}

// ProductChoice represents a selectable option for a product.
type ProductChoice struct {
	ID            uuid.UUID           `db:"id" json:"id"`
	ProductID     uuid.UUID           `db:"product_id" json:"productId"`
	PriceModifier decimal.Decimal     `db:"price_modifier" json:"priceModifier"`
	SortOrder     int                 `db:"sort_order" json:"sortOrder"`
	Translations  []ChoiceTranslation `json:"translations"`
}

type ChoiceTranslation struct {
	ProductChoiceID uuid.UUID `db:"product_choice_id" json:"productChoiceId"`
	Locale          string    `db:"locale" json:"locale"`
	Name            string    `db:"name" json:"name"`
}

// GetTranslationFor returns the translation matching the given locale.
func (c *ProductChoice) GetTranslationFor(locale string) string {
	for _, candidate := range translationFallbackOrder(locale) {
		for i := range c.Translations {
			if c.Translations[i].Locale == candidate {
				return c.Translations[i].Name
			}
		}
	}
	if len(c.Translations) > 0 {
		return c.Translations[0].Name
	}
	return ""
}

type ProductOrderDetails struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	Code           *string         `db:"code" json:"code"`
	CategoryName   string          `db:"category_name" json:"categoryName"`
	Name           string          `db:"name" json:"name"`
	Price          decimal.Decimal `db:"price" json:"price"`
	IsDiscountable bool            `db:"is_discountable" json:"isDiscountable"`
	VatCategory    VatCategory     `db:"vat_category" json:"vatCategory"`
}

func NewProduct(price decimal.Decimal, categoryID uuid.UUID, isVisible bool, isAvailable bool, vatCategory VatCategory, translations []Translation) (*Product, error) {
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

	if !vatCategory.IsValid() {
		return nil, errors.New("invalid vat category")
	}

	return &Product{
		ID:           uuid.New(),
		Price:        price,
		IsVisible:    isVisible,
		IsAvailable:  isAvailable,
		VatCategory:  vatCategory,
		CategoryID:   categoryID,
		Translations: translations,
	}, nil
}

// GetTranslationFor returns the translation matching the given language,
// or falls back to the first available translation if no exact match is found.
func (p *Product) GetTranslationFor(language string) *Translation {
	for _, candidate := range translationFallbackOrder(language) {
		for i := range p.Translations {
			if p.Translations[i].Language == candidate {
				return &p.Translations[i]
			}
		}
	}
	if len(p.Translations) > 0 {
		return &p.Translations[0]
	}
	return nil
}
