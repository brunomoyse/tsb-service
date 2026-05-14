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

// GetTranslationFor returns the translation for the given language,
// falling back through the configured chain (French first after the
// requested locale, since FR is the authoring language and always
// present). Translations with an empty Name are skipped so a blank row
// does not shadow a valid fallback.
func (c *Category) GetTranslationFor(language string) *Translation {
	for _, candidate := range translationFallbackOrder(language) {
		for i := range c.Translations {
			if c.Translations[i].Language == candidate && c.Translations[i].Name != "" {
				return &c.Translations[i]
			}
		}
	}
	for i := range c.Translations {
		if c.Translations[i].Name != "" {
			return &c.Translations[i]
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
