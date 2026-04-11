package domain

import (
	"encoding/json"

	productDomain "tsb-service/internal/modules/product/domain"
)

// HubriseCatalog is the top-level payload sent via PUT /catalogs/:id.
type HubriseCatalog struct {
	Name string             `json:"name"`
	Data HubriseCatalogData `json:"data"`
}

// HubriseCatalogData mirrors the `data` field of the HubRise catalog
// resource. We populate only the subset TSB needs.
type HubriseCatalogData struct {
	Categories  []HubriseCategory    `json:"categories"`
	Products    []HubriseProduct     `json:"products"`
	OptionLists []HubriseOptionList  `json:"option_lists,omitempty"`
	Deals       []any                `json:"deals,omitempty"`
	Discounts   []any                `json:"discounts,omitempty"`
	Charges     []any                `json:"charges,omitempty"`
}

// HubriseCategory — note the `description` field is used as a JSON
// payload carrying translations (see encoding strategy in the plan).
type HubriseCategory struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// HubriseProduct represents one HubRise product, collapsing a TSB
// product + its single SKU.
type HubriseProduct struct {
	Ref         string       `json:"ref"`
	CategoryRef string       `json:"category_ref"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Skus        []HubriseSku `json:"skus"`
}

// HubriseSku holds the custom_fields payload with VAT + translations.
type HubriseSku struct {
	Ref            string                 `json:"ref"`
	Name           string                 `json:"name,omitempty"`
	Price          string                 `json:"price"`
	OptionListRefs []string               `json:"option_list_refs,omitempty"`
	CustomFields   SkuCustomFieldsPayload `json:"custom_fields"`
}

// SkuCustomFieldsPayload matches the shape consumed by the POS client.
type SkuCustomFieldsPayload struct {
	VatCategory  string                `json:"vat_category"`
	Translations SkuTranslationsPayload `json:"translations"`
}

// SkuTranslationsPayload carries all 4 language variants.
type SkuTranslationsPayload struct {
	Fr ProductTranslation `json:"fr"`
	En ProductTranslation `json:"en"`
	Nl ProductTranslation `json:"nl"`
	Zh ProductTranslation `json:"zh"`
}

// ProductTranslation is a single-language name+description.
type ProductTranslation struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// HubriseOptionList maps a TSB ProductChoice group (one list per
// product — we model a product's choices as a single option_list).
type HubriseOptionList struct {
	Ref               string          `json:"ref"`
	Name              string          `json:"name"`
	MinSelections     int             `json:"min_selections"`
	MaxSelections     *int            `json:"max_selections,omitempty"`
	MultipleSelection bool            `json:"multiple_selection"`
	Options           []HubriseOption `json:"options"`
}

type HubriseOption struct {
	Ref   string `json:"ref"`
	Name  string `json:"name"`
	Price string `json:"price"`
}

// CategoryDescriptionPayload is the JSON encoded in the `description`
// field of a HubriseCategory, carrying multi-language translations.
type CategoryDescriptionPayload struct {
	Translations CategoryTranslations `json:"translations"`
}

type CategoryTranslations struct {
	Fr string `json:"fr"`
	En string `json:"en"`
	Nl string `json:"nl"`
	Zh string `json:"zh"`
}

// EncodeCategoryDescription marshals the translation payload to the
// single JSON string stored in the HubRise category `description` field.
func EncodeCategoryDescription(t CategoryTranslations) string {
	b, err := json.Marshal(CategoryDescriptionPayload{Translations: t})
	if err != nil {
		return ""
	}
	return string(b)
}

// MapProductToHubrise converts a TSB product + its translations into
// a HubriseProduct with translations encoded inside the single SKU's
// custom_fields. Assumes the 1-product = 1-SKU convention.
//
// `categoryRef` is the HubRise ref assigned to the parent category.
func MapProductToHubrise(p *productDomain.Product, categoryRef string) HubriseProduct {
	skuRef := p.ID.String()
	productRef := p.ID.String()
	if p.Code != nil && *p.Code != "" {
		productRef = *p.Code
	}

	trFr := pickTranslation(p.Translations, "fr")
	trEn := pickTranslation(p.Translations, "en")
	trNl := pickTranslation(p.Translations, "nl")
	trZh := pickTranslation(p.Translations, "zh")

	return HubriseProduct{
		Ref:         productRef,
		CategoryRef: categoryRef,
		Name:        trFr.Name,
		Description: safeDesc(trFr.Description),
		Skus: []HubriseSku{
			{
				Ref:   skuRef,
				Name:  "Standard",
				Price: p.Price.String() + " EUR",
				CustomFields: SkuCustomFieldsPayload{
					VatCategory: string(p.VatCategory),
					Translations: SkuTranslationsPayload{
						Fr: trFr,
						En: trEn,
						Nl: trNl,
						Zh: trZh,
					},
				},
			},
		},
	}
}

// MapCategoryToHubrise builds a HubriseCategory with the
// translations-as-description encoding.
func MapCategoryToHubrise(c *productDomain.Category, categoryRef string) HubriseCategory {
	trFr := pickCategoryName(c.Translations, "fr")
	trEn := pickCategoryName(c.Translations, "en")
	trNl := pickCategoryName(c.Translations, "nl")
	trZh := pickCategoryName(c.Translations, "zh")

	return HubriseCategory{
		Ref:  categoryRef,
		Name: trFr,
		Description: EncodeCategoryDescription(CategoryTranslations{
			Fr: trFr,
			En: trEn,
			Nl: trNl,
			Zh: trZh,
		}),
	}
}

func pickTranslation(ts []productDomain.Translation, lang string) ProductTranslation {
	for _, t := range ts {
		if t.Language == lang {
			desc := ""
			if t.Description != nil {
				desc = *t.Description
			}
			return ProductTranslation{Name: t.Name, Description: desc}
		}
	}
	return ProductTranslation{}
}

func pickCategoryName(ts []productDomain.Translation, lang string) string {
	for _, t := range ts {
		if t.Language == lang {
			return t.Name
		}
	}
	return ""
}

func safeDesc(s string) string {
	return s
}
