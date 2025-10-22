package deliveroo

// Category represents a menu category (v1 API format with translations)
type Category struct {
	ID          string            `json:"id"`
	Name        map[string]string `json:"name"`
	Description map[string]string `json:"description,omitempty"`
	ItemIDs     []string          `json:"item_ids"`
}

// Item represents a menu item/product (v1 API format with translations)
type Item struct {
	ID                        string                 `json:"id"`
	Name                      map[string]string      `json:"name"`
	Description               map[string]string      `json:"description,omitempty"`
	OperationalName           string                 `json:"operational_name,omitempty"`
	PriceInfo                 PriceInfo              `json:"price_info"`
	PLU                       string                 `json:"plu,omitempty"`
	Barcodes                  []string               `json:"barcodes,omitempty"`
	Image                     *ImageInfo             `json:"image,omitempty"`
	IsEligibleAsReplacement   bool                   `json:"is_eligible_as_replacement"`
	IsEligibleForSubstitution bool                   `json:"is_eligible_for_substitution"`
	IsReturnable              bool                   `json:"is_returnable,omitempty"`
	TaxRate                   string                 `json:"tax_rate"`
	ModifierIDs               []string               `json:"modifier_ids,omitempty"`
	Allergies                 []string               `json:"allergies,omitempty"`
	Classifications           []string               `json:"classifications,omitempty"`
	Diets                     []string               `json:"diets,omitempty"`
	NutritionalInfo           *NutritionalInfo       `json:"nutritional_info,omitempty"`
	Type                      string                 `json:"type"` // "ITEM" or "CHOICE"
	ContainsAlcohol           bool                   `json:"contains_alcohol"`
	ExternalData              string                 `json:"external_data,omitempty"`
	Highlights                []string               `json:"highlights,omitempty"`
	MaxQuantity               *int                   `json:"max_quantity,omitempty"`
}