package domain

import (
	"errors"

	"github.com/google/uuid"
)

// Category represents a product category with translations.
type Category struct {
	ID           uuid.UUID
	Order        int
	Translations []Translation
}

// NewCategory creates a new category ensuring at least one translation is provided.
func NewCategory(order int, translations []Translation) (*Category, error) {
	if len(translations) == 0 {
		return nil, errors.New("at least one translation is required")
	}
	return &Category{
		ID:           uuid.New(),
		Order:        order,
		Translations: translations,
	}, nil
}

// GetTranslationFor returns the translation for the given language, or falls back to the first available.
func (c *Category) GetTranslationFor(language string) *Translation {
	for _, t := range c.Translations {
		if t.Language == language {
			return &t
		}
	}
	if len(c.Translations) > 0 {
		return &c.Translations[0]
	}
	return nil
}

// CategoryTranslation holds localized details for a category.
type CategoryTranslation struct {
	Language string // e.g., "en"
	Name     string
}
