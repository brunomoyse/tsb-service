// Package realmenu is the restaurant's real menu (183 products in 19
// categories, four languages), for tests that need its ambiguities: the
// same Chinese or French name in several categories, names that contain a
// category word, and so on.
package realmenu

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"tsb-service/internal/mcp/upstream"
)

//go:embed menu.json
var raw []byte

type menu struct {
	Categories []struct {
		ID    string            `json:"id"`
		Order int               `json:"order"`
		Slug  string            `json:"slug"`
		Names map[string]string `json:"names"`
	} `json:"categories"`
	Products []struct {
		ID         string            `json:"id"`
		Code       *string           `json:"code"`
		Slug       string            `json:"slug"`
		Price      string            `json:"price"`
		CategoryID string            `json:"category_id"`
		Available  bool              `json:"available"`
		Visible    bool              `json:"visible"`
		Names      map[string]string `json:"names"`
	} `json:"products"`
}

var languages = []string{"en", "fr", "nl", "zh"}

func translations(names map[string]string) []upstream.Translation {
	var ts []upstream.Translation
	for _, l := range languages {
		if n, ok := names[l]; ok {
			ts = append(ts, upstream.Translation{Language: l, Name: n})
		}
	}
	return ts
}

// Load returns fresh copies of the categories and products.
func Load() ([]upstream.Category, []*upstream.Product) {
	var m menu
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(fmt.Sprintf("realmenu: %v", err))
	}
	cats := make([]upstream.Category, len(m.Categories))
	refs := map[string]upstream.CategoryRef{}
	for i, c := range m.Categories {
		cats[i] = upstream.Category{ID: c.ID, Order: c.Order, Slug: c.Slug, Name: c.Names["fr"], Translations: translations(c.Names)}
		refs[c.ID] = upstream.CategoryRef{ID: c.ID, Name: c.Names["fr"], Translations: translations(c.Names)}
	}
	prods := make([]*upstream.Product, len(m.Products))
	for i, p := range m.Products {
		ref, ok := refs[p.CategoryID]
		if !ok {
			panic(fmt.Sprintf("realmenu: product %s has an unknown category", p.ID))
		}
		prods[i] = &upstream.Product{ID: p.ID, Code: p.Code, Slug: p.Slug, Name: p.Names["fr"], Price: p.Price, VatCategory: "food",
			IsAvailable: p.Available, IsVisible: p.Visible, IsDiscountable: true, Category: ref, Translations: translations(p.Names)}
	}
	return cats, prods
}
