package deliveroo

// Menu represents the complete menu structure for Deliveroo
type Menu struct {
	Name         string     `json:"name"`
	Categories   []Category `json:"categories"`
	Items        []Item     `json:"items"`
	ModifierLists []interface{} `json:"modifier_lists,omitempty"`
}

// Category represents a menu category
type Category struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ItemIDs     []string `json:"item_ids"`
}

// Item represents a menu item/product
type Item struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Description     string        `json:"description,omitempty"`
	Price           Price         `json:"price"`
	Available       bool          `json:"available"`
	Visible         bool          `json:"visible"`
	ModifierListIDs []string      `json:"modifier_list_ids,omitempty"`
	Tags            []string      `json:"tags,omitempty"`
	ImageURL        string        `json:"image_url,omitempty"`
	NutritionalInfo *Nutritional  `json:"nutritional_info,omitempty"`
}

// Price represents the price of an item
type Price struct {
	Amount   int    `json:"amount"`   // Price in cents (e.g., 1250 for €12.50)
	Currency string `json:"currency"` // e.g., "EUR"
}

// Nutritional represents nutritional information (optional)
type Nutritional struct {
	Calories *int `json:"calories,omitempty"`
}