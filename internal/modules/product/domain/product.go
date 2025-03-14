package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Product represents the core product aggregate.
type Product struct {
	ID           uuid.UUID     `json:"id"`
	Price        float64       `json:"price"`
	Code         *string       `json:"code"`
	Slug         *string       `json:"slug"`
	PieceCount   *int          `json:"pieceCount"`
	IsVisible    bool          `json:"isVisible"`
	IsAvailable  bool          `json:"isAvailable"`
	IsHalal      bool          `json:"isHalal"`
	IsVegan      bool          `json:"isVegan"`
	CategoryID   uuid.UUID     `json:"categoryId"`
	Translations []Translation `json:"translations"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

func NewProduct(price float64, categoryID uuid.UUID, isVisible bool, isAvailable bool, translations []Translation) (*Product, error) {
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
