package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tr3() []Translation {
	return []Translation{{Language: "fr", Name: "Saumon"}, {Language: "en", Name: "Salmon"}, {Language: "nl", Name: "Zalm"}}
}

func TestNewCategory(t *testing.T) {
	c, err := NewCategory(3, "sushi", []Translation{{Language: "fr", Name: "Sushi"}})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, c.ID)
	assert.Equal(t, 3, c.Order)
	assert.Equal(t, "sushi", c.Slug)
	assert.Len(t, c.Translations, 1)

	other, _ := NewCategory(3, "sushi", []Translation{{Language: "fr", Name: "Sushi"}})
	assert.NotEqual(t, c.ID, other.ID, "every category gets its own id")

	_, err = NewCategory(1, "sushi", nil)
	assert.EqualError(t, err, "at least one translation is required")
	_, err = NewCategory(1, "", []Translation{{Language: "fr", Name: "x"}})
	assert.EqualError(t, err, "slug is required")
	_, err = NewCategory(1, "", nil)
	assert.EqualError(t, err, "at least one translation is required", "the translation check comes first")
}

func TestCategoryGetTranslationFor(t *testing.T) {
	c := &Category{Translations: []Translation{
		{Language: "fr", Name: "Boissons"}, {Language: "nl", Name: ""}, {Language: "en", Name: "Drinks"},
	}}
	assert.Equal(t, "Drinks", c.GetTranslationFor("en").Name)
	assert.Equal(t, "Boissons", c.GetTranslationFor("nl").Name, "a blank nl row does not shadow the French name")
	assert.Equal(t, "Boissons", c.GetTranslationFor("zh").Name)
	assert.Equal(t, "Boissons", c.GetTranslationFor("de").Name, "an unknown language falls back to French")

	onlyUnlisted := &Category{Translations: []Translation{{Language: "de", Name: "Getränke"}}}
	assert.Equal(t, "Getränke", onlyUnlisted.GetTranslationFor("fr").Name, "any named translation beats none")

	blank := &Category{Translations: []Translation{{Language: "fr", Name: ""}, {Language: "en", Name: ""}}}
	got := blank.GetTranslationFor("fr")
	require.NotNil(t, got, "a category with only blank names still returns its first row")
	assert.Equal(t, "fr", got.Language)

	assert.Nil(t, (&Category{}).GetTranslationFor("fr"))
}

func TestProductGetTranslationFor(t *testing.T) {
	p := &Product{Translations: []Translation{{Language: "en", Name: "Salmon"}, {Language: "fr", Name: ""}, {Language: "nl", Name: "Zalm"}}}
	assert.Equal(t, "Zalm", p.GetTranslationFor("nl").Name)
	assert.Equal(t, "Salmon", p.GetTranslationFor("fr").Name, "blank French is skipped")
	assert.Equal(t, "Salmon", p.GetTranslationFor("de").Name)

	blank := &Product{Translations: []Translation{{Language: "de", Name: ""}}}
	assert.Equal(t, "de", blank.GetTranslationFor("fr").Language)
	assert.Nil(t, (&Product{}).GetTranslationFor("fr"))

	odd := &Product{Translations: []Translation{{Language: "de", Name: "Lachs"}}}
	assert.Equal(t, "Lachs", odd.GetTranslationFor("fr").Name)
}

func TestNewProduct(t *testing.T) {
	cat := uuid.New()
	price := decimal.RequireFromString("12.50")

	t.Run("a visible product needs three translations", func(t *testing.T) {
		_, err := NewProduct(price, cat, true, true, VatCategoryFood, tr3()[:2])
		assert.EqualError(t, err, "visible product must have at least 3 translations")
		p, err := NewProduct(price, cat, true, true, VatCategoryFood, tr3())
		require.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, p.ID)
		assert.True(t, p.Price.Equal(price))
		assert.Equal(t, cat, p.CategoryID)
		assert.True(t, p.IsVisible)
		assert.True(t, p.IsAvailable)
		assert.Equal(t, VatCategoryFood, p.VatCategory)
		assert.Len(t, p.Translations, 3)
	})

	t.Run("a hidden product needs only one", func(t *testing.T) {
		p, err := NewProduct(price, cat, false, false, VatCategoryZeroRated, tr3()[:1])
		require.NoError(t, err)
		assert.False(t, p.IsVisible)
		assert.False(t, p.IsAvailable)
		_, err = NewProduct(price, cat, false, true, VatCategoryFood, nil)
		assert.EqualError(t, err, "at least one translation is required")
	})

	t.Run("the VAT category must be known", func(t *testing.T) {
		_, err := NewProduct(price, cat, true, true, "", tr3())
		assert.EqualError(t, err, "invalid vat category")
		_, err = NewProduct(price, cat, false, true, "bogus", tr3()[:1])
		assert.EqualError(t, err, "invalid vat category")
	})
}

func TestChoiceTranslations(t *testing.T) {
	choice := &ProductChoice{Translations: []ChoiceTranslation{{Locale: "fr", Name: "Soja"}, {Locale: "nl", Name: ""}, {Locale: "en", Name: "Soy"}}}
	assert.Equal(t, "Soy", choice.GetTranslationFor("en"))
	assert.Equal(t, "Soja", choice.GetTranslationFor("nl"), "a blank row falls back")
	assert.Equal(t, "Soja", choice.GetTranslationFor("de"))
	assert.Equal(t, "Gut", (&ProductChoice{Translations: []ChoiceTranslation{{Locale: "de", Name: "Gut"}}}).GetTranslationFor("fr"))
	assert.Equal(t, "", (&ProductChoice{Translations: []ChoiceTranslation{{Locale: "fr", Name: ""}}}).GetTranslationFor("fr"))
	assert.Equal(t, "", (&ProductChoice{}).GetTranslationFor("fr"))

	group := &ProductChoiceGroup{Translations: []ChoiceTranslation{{Locale: "fr", Name: "Sauce"}, {Locale: "en", Name: ""}}}
	assert.Equal(t, "Sauce", group.GetTranslationFor("en"))
	assert.Equal(t, "Sauce", group.GetTranslationFor("fr"))
	assert.Equal(t, "Gut", (&ProductChoiceGroup{Translations: []ChoiceTranslation{{Locale: "de", Name: "Gut"}}}).GetTranslationFor("fr"))
	assert.Equal(t, "", (&ProductChoiceGroup{Translations: []ChoiceTranslation{{Locale: "fr", Name: ""}}}).GetTranslationFor("fr"))
	assert.Equal(t, "", (&ProductChoiceGroup{}).GetTranslationFor("fr"))
}

func TestVatCategory(t *testing.T) {
	type row struct {
		category VatCategory
		svc      ServiceType
		rate     float64
		code     string
	}
	for _, r := range []row{
		{VatCategoryFood, ServiceTypeDineIn, 12, "B"},
		{VatCategoryFood, ServiceTypeTakeaway, 6, "C"},
		{VatCategoryFood, ServiceTypeDelivery, 6, "C"},
		{VatCategoryBeverage, ServiceTypeDineIn, 21, "A"},
		{VatCategoryBeverage, ServiceTypeTakeaway, 21, "A"},
		{VatCategoryBeverage, ServiceTypeDelivery, 21, "A"},
		{VatCategoryZeroRated, ServiceTypeDineIn, 0, "D"},
		{VatCategoryZeroRated, ServiceTypeDelivery, 0, "D"},
		{VatCategoryOutOfScope, ServiceTypeTakeaway, 0, "X"},
		{"unknown", ServiceTypeDineIn, 0, "X"},
		{"", ServiceTypeTakeaway, 0, "X"},
	} {
		t.Run(string(r.category)+"/"+string(r.svc), func(t *testing.T) {
			assert.InDelta(t, r.rate, r.category.VatRatePercent(r.svc), 0)
			assert.Equal(t, r.code, r.category.SceCode(r.svc))
		})
	}

	for _, valid := range []VatCategory{VatCategoryFood, VatCategoryBeverage, VatCategoryZeroRated, VatCategoryOutOfScope} {
		assert.True(t, valid.IsValid(), valid)
	}
	for _, invalid := range []VatCategory{"", "food ", "FOOD", "alcohol"} {
		assert.False(t, invalid.IsValid(), invalid)
	}
}
