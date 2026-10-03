package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/upstream"
)

// Action kinds.
const (
	KindProductAvailability     = "product.availability"
	KindProductVisibility       = "product.visibility"
	KindProductAvailabilityBulk = "product.availability.bulk"
	KindProductPrice            = "product.price"
	KindProductVAT              = "product.vat"
	KindProductUpdate           = "product.update"
	KindProductCreate           = "product.create"
	KindProductImage            = "product.image"
)

// Languages the catalogue is translated into.
var Languages = []string{"fr", "en", "zh", "nl"}

// VATCategories accepted by tsb-service.
var VATCategories = []string{"food", "beverage", "zero_rated", "out_of_scope"}

// ProductLabel renders a product for summaries: French name plus Chinese name
// when it differs, e.g. `"Maki saumon" (三文鱼卷)`.
func ProductLabel(p *upstream.Product) string {
	fr, zh := p.NameIn("fr"), ""
	for _, t := range p.Translations {
		if t.Language == "zh" && t.Name != "" && t.Name != fr {
			zh = t.Name
		}
	}
	if zh != "" {
		return fmt.Sprintf("%q (%s)", fr, zh)
	}
	return fmt.Sprintf("%q", fr)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func availWord(b bool) string {
	if b {
		return "available"
	}
	return "unavailable"
}

func visWord(b bool) string {
	if b {
		return "visible"
	}
	return "hidden"
}

func getProduct(ctx context.Context, env *Env, id string) (*upstream.Product, error) {
	if strings.TrimSpace(id) == "" {
		return nil, Userf("product_id is required.")
	}
	p, err := env.Up.Product(ctx, id)
	if upstream.IsNotFound(err) {
		return nil, Userf("No product with id %q. Use search_products to find the id.", id)
	}
	return p, err
}

// --- availability / visibility (low) ----------------------------------------

type ProductToggleParams struct {
	ProductID string `json:"product_id"`
	Value     bool   `json:"value"`
}

type toggleBefore struct {
	Value bool `json:"value"`
}

func productToggle(kind, field string, get func(*upstream.Product) bool, word, wordZh func(bool) string, set func(*upstream.UpdateProductInput, bool)) handler {
	return spec[ProductToggleParams, toggleBefore]{
		Kind: kind,
		Risk: RiskLow,
		Prepare: func(ctx context.Context, env *Env, p *ProductToggleParams) (*prepared, error) {
			prod, err := getProduct(ctx, env, p.ProductID)
			if err != nil {
				return nil, err
			}
			cur := get(prod)
			pr := &prepared{EntityType: "product", EntityID: prod.ID, Before: toggleBefore{Value: cur}}
			if cur == p.Value {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("%s is already %s. Nothing changed.", ProductLabel(prod), word(cur))
				pr.SummaryZh = fmt.Sprintf("%s已经是%s状态，无需更改。", productLabelZh(prod), wordZh(cur))
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("%s: %s -> %s", ProductLabel(prod), word(cur), word(p.Value))
			pr.SummaryZh = productLabelZh(prod) + "：" + wordZh(cur) + arrowZh + wordZh(p.Value)
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p ProductToggleParams, _ []byte) (any, error) {
			var in upstream.UpdateProductInput
			set(&in, p.Value)
			prod, err := env.Up.UpdateProduct(ctx, p.ProductID, in)
			if err != nil {
				return nil, err
			}
			return toggleBefore{Value: get(prod)}, nil
		},
		Inverse: func(p ProductToggleParams, b toggleBefore, _ json.RawMessage) (string, any, error) {
			return kind, ProductToggleParams{ProductID: p.ProductID, Value: b.Value}, nil
		},
	}
}

// --- bulk availability (sensitive) ------------------------------------------

type BulkAvailabilityItem struct {
	ProductID string `json:"product_id"`
	Available bool   `json:"available"`
}

type BulkAvailabilityParams struct {
	Items []BulkAvailabilityItem `json:"items"`
}

func bulkAvailability() handler {
	return spec[BulkAvailabilityParams, map[string]bool]{
		Kind: KindProductAvailabilityBulk,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *BulkAvailabilityParams) (*prepared, error) {
			if len(p.Items) == 0 {
				return nil, Userf("At least one product is required.")
			}
			if len(p.Items) > 100 {
				return nil, Userf("At most 100 products can be changed at once.")
			}
			all, err := env.Up.Products(ctx)
			if err != nil {
				return nil, err
			}
			byID := map[string]*upstream.Product{}
			for i := range all {
				byID[all[i].ID] = &all[i]
			}
			seen := map[string]bool{}
			before := map[string]bool{}
			var kept []BulkAvailabilityItem
			var changed lines
			var unchanged, unchangedZh []string
			for _, it := range p.Items {
				if seen[it.ProductID] {
					continue
				}
				seen[it.ProductID] = true
				prod, ok := byID[it.ProductID]
				if !ok {
					return nil, Userf("No product with id %q. Use search_products to find the id.", it.ProductID)
				}
				if prod.IsAvailable == it.Available {
					unchanged = append(unchanged, ProductLabel(prod))
					unchangedZh = append(unchangedZh, productLabelZh(prod))
					continue
				}
				kept = append(kept, it)
				before[it.ProductID] = prod.IsAvailable
				changed.add(fmt.Sprintf("%s: %s -> %s", ProductLabel(prod), availWord(prod.IsAvailable), availWord(it.Available)),
					productLabelZh(prod)+"："+availZh(prod.IsAvailable)+arrowZh+availZh(it.Available))
			}
			p.Items = kept
			pr := &prepared{EntityType: "products", EntityID: strings.Join(slices.Sorted(maps.Keys(before)), ","), Before: before}
			if len(kept) == 0 {
				pr.NoOp = true
				pr.Summary = "All selected products are already in the requested state. Nothing changed."
				pr.SummaryZh = "所选商品都已是目标状态，无需更改。"
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("%d products: %s", len(kept), changed.enJoined())
			pr.SummaryZh = fmt.Sprintf("%d 个商品：%s", len(kept), changed.zhJoined())
			if len(unchanged) > 0 {
				pr.Summary += fmt.Sprintf(" (already in that state: %s)", strings.Join(unchanged, ", "))
				pr.SummaryZh += "（已是该状态：" + strings.Join(unchangedZh, "、") + "）"
			}
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p BulkAvailabilityParams, _ []byte) (any, error) {
			after := map[string]bool{}
			for i, it := range p.Items {
				v := it.Available
				if _, err := env.Up.UpdateProduct(ctx, it.ProductID, upstream.UpdateProductInput{IsAvailable: &v}); err != nil {
					if i > 0 {
						return nil, fmt.Errorf("%w (%d of %d products were already changed)", err, i, len(p.Items))
					}
					return nil, err
				}
				after[it.ProductID] = v
			}
			return after, nil
		},
		Inverse: func(_ BulkAvailabilityParams, b map[string]bool, _ json.RawMessage) (string, any, error) {
			inv := BulkAvailabilityParams{}
			for _, id := range slices.Sorted(maps.Keys(b)) {
				inv.Items = append(inv.Items, BulkAvailabilityItem{ProductID: id, Available: b[id]})
			}
			return KindProductAvailabilityBulk, inv, nil
		},
	}
}

// --- price (sensitive) ------------------------------------------------------

type PriceParams struct {
	ProductID     string `json:"product_id"`
	NewPriceCents int64  `json:"new_price_cents"`
	// SkipBounds is only set by undo: restoring an earlier price must not be
	// blocked by the percentage guardrail.
	SkipBounds bool `json:"skip_bounds,omitempty"`
}

type priceBefore struct {
	PriceCents int64 `json:"price_cents"`
}

func priceChange() handler {
	return spec[PriceParams, priceBefore]{
		Kind: KindProductPrice,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *PriceParams) (*prepared, error) {
			prod, err := getProduct(ctx, env, p.ProductID)
			if err != nil {
				return nil, err
			}
			cur := money.MustCents(prod.Price)
			pr := &prepared{EntityType: "product", EntityID: prod.ID, Before: priceBefore{PriceCents: cur}}
			if p.NewPriceCents <= 0 {
				return nil, Userf("The new price must be greater than 0.")
			}
			if !p.SkipBounds {
				if err := money.CheckPriceChange(cur, p.NewPriceCents, env.PriceMaxPct); err != nil {
					return nil, &UserError{Msg: "Price change refused: " + strings.TrimPrefix(err.Error(), money.ErrOutOfBounds.Error()+": ") + "."}
				}
			}
			if cur == p.NewPriceCents {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("%s already costs %s. Nothing changed.", ProductLabel(prod), money.Format(cur))
				pr.SummaryZh = fmt.Sprintf("%s的价格已经是 %s，无需更改。", productLabelZh(prod), moneyZh(cur))
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("%s: price %s -> %s", ProductLabel(prod), money.Format(cur), money.Format(p.NewPriceCents))
			pr.SummaryZh = productLabelZh(prod) + "价格：" + moneyZh(cur) + arrowZh + moneyZh(p.NewPriceCents)
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p PriceParams, _ []byte) (any, error) {
			price := money.FromCents(p.NewPriceCents)
			prod, err := env.Up.UpdateProduct(ctx, p.ProductID, upstream.UpdateProductInput{Price: &price})
			if err != nil {
				return nil, err
			}
			return priceBefore{PriceCents: money.MustCents(prod.Price)}, nil
		},
		Inverse: func(p PriceParams, b priceBefore, _ json.RawMessage) (string, any, error) {
			return KindProductPrice, PriceParams{ProductID: p.ProductID, NewPriceCents: b.PriceCents, SkipBounds: true}, nil
		},
	}
}

// --- VAT category (sensitive) -----------------------------------------------

type VATParams struct {
	ProductID   string `json:"product_id"`
	VatCategory string `json:"vat_category"`
}

type vatBefore struct {
	VatCategory string `json:"vat_category"`
}

func vatChange() handler {
	return spec[VATParams, vatBefore]{
		Kind: KindProductVAT,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *VATParams) (*prepared, error) {
			p.VatCategory = strings.ToLower(strings.TrimSpace(p.VatCategory))
			if !slices.Contains(VATCategories, p.VatCategory) {
				return nil, Userf("vat_category must be one of: %s.", strings.Join(VATCategories, ", "))
			}
			prod, err := getProduct(ctx, env, p.ProductID)
			if err != nil {
				return nil, err
			}
			pr := &prepared{EntityType: "product", EntityID: prod.ID, Before: vatBefore{VatCategory: prod.VatCategory}}
			if prod.VatCategory == p.VatCategory {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("%s already has VAT category %s. Nothing changed.", ProductLabel(prod), p.VatCategory)
				pr.SummaryZh = fmt.Sprintf("%s的增值税类别已经是%s，无需更改。", productLabelZh(prod), vatLabelZh(p.VatCategory))
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("%s: VAT category %s -> %s", ProductLabel(prod), prod.VatCategory, p.VatCategory)
			pr.SummaryZh = productLabelZh(prod) + "增值税类别：" + vatLabelZh(prod.VatCategory) + arrowZh + vatLabelZh(p.VatCategory)
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p VATParams, _ []byte) (any, error) {
			prod, err := env.Up.UpdateProduct(ctx, p.ProductID, upstream.UpdateProductInput{VatCategory: &p.VatCategory})
			if err != nil {
				return nil, err
			}
			return vatBefore{VatCategory: prod.VatCategory}, nil
		},
		Inverse: func(p VATParams, b vatBefore, _ json.RawMessage) (string, any, error) {
			return KindProductVAT, VATParams{ProductID: p.ProductID, VatCategory: b.VatCategory}, nil
		},
	}
}

// --- product details (sensitive) -------------------------------------------

// ProductUpdateParams changes product details. Nil/absent fields are kept.
type ProductUpdateParams struct {
	ProductID      string            `json:"product_id"`
	Names          map[string]string `json:"names,omitempty"`
	Descriptions   map[string]string `json:"descriptions,omitempty"`
	CategoryID     *string           `json:"category_id,omitempty"`
	Code           *string           `json:"code,omitempty"`
	PieceCount     *int              `json:"piece_count,omitempty"`
	IsHalal        *bool             `json:"is_halal,omitempty"`
	IsSpicy        *bool             `json:"is_spicy,omitempty"`
	IsVegetarian   *bool             `json:"is_vegetarian,omitempty"`
	IsLunchOnly    *bool             `json:"is_lunch_only,omitempty"`
	IsDiscountable *bool             `json:"is_discountable,omitempty"`
}

// productDetails is the editable state captured as before-state.
type productDetails struct {
	Names          map[string]string `json:"names"`
	Descriptions   map[string]string `json:"descriptions"`
	CategoryID     string            `json:"category_id"`
	Code           string            `json:"code"`
	PieceCount     *int              `json:"piece_count"`
	IsHalal        bool              `json:"is_halal"`
	IsSpicy        bool              `json:"is_spicy"`
	IsVegetarian   bool              `json:"is_vegetarian"`
	IsLunchOnly    bool              `json:"is_lunch_only"`
	IsDiscountable bool              `json:"is_discountable"`
}

func detailsOf(p *upstream.Product) productDetails {
	d := productDetails{Names: map[string]string{}, Descriptions: map[string]string{}, CategoryID: p.Category.ID, PieceCount: p.PieceCount,
		IsHalal: p.IsHalal, IsSpicy: p.IsSpicy, IsVegetarian: p.IsVegetarian, IsLunchOnly: p.IsLunchOnly, IsDiscountable: p.IsDiscountable}
	if p.Code != nil {
		d.Code = *p.Code
	}
	for _, t := range p.Translations {
		d.Names[t.Language] = t.Name
		if t.Description != nil {
			d.Descriptions[t.Language] = *t.Description
		}
	}
	return d
}

func validLanguage(lang string) error {
	if !slices.Contains(Languages, lang) {
		return Userf("Unknown language %q. Use one of: %s.", lang, strings.Join(Languages, ", "))
	}
	return nil
}

// categoryName returns a category's French and Chinese names.
func categoryName(ctx context.Context, env *Env, id string) (string, string, error) {
	cats, err := env.Up.Categories(ctx)
	if err != nil {
		return "", "", err
	}
	for i := range cats {
		if cats[i].ID == id {
			return cats[i].Name, categoryNameZh(&cats[i]), nil
		}
	}
	return "", "", Userf("No category with id %q. Use list_categories to find the id.", id)
}

func productUpdate() handler {
	return spec[ProductUpdateParams, productDetails]{
		Kind: KindProductUpdate,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ProductUpdateParams) (*prepared, error) {
			prod, err := getProduct(ctx, env, p.ProductID)
			if err != nil {
				return nil, err
			}
			cur := detailsOf(prod)
			var diffs lines
			for _, lang := range slices.Sorted(maps.Keys(p.Names)) {
				if err := validLanguage(lang); err != nil {
					return nil, err
				}
				n := strings.TrimSpace(p.Names[lang])
				if n == "" {
					return nil, Userf("The %s name cannot be empty.", lang)
				}
				p.Names[lang] = n
				if cur.Names[lang] != n {
					diffs.add(fmt.Sprintf("name (%s): %q -> %q", lang, cur.Names[lang], n),
						langZh[lang]+"名称："+quoteOrNoneZh(cur.Names[lang])+arrowZh+quoteZh(n))
				} else {
					delete(p.Names, lang)
				}
			}
			for _, lang := range slices.Sorted(maps.Keys(p.Descriptions)) {
				if err := validLanguage(lang); err != nil {
					return nil, err
				}
				d := strings.TrimSpace(p.Descriptions[lang])
				p.Descriptions[lang] = d
				if cur.Descriptions[lang] != d {
					diffs.add(fmt.Sprintf("description (%s) changed", lang), langZh[lang]+"描述已修改")
				} else {
					delete(p.Descriptions, lang)
				}
			}
			if p.CategoryID != nil {
				if *p.CategoryID == cur.CategoryID {
					p.CategoryID = nil
				} else {
					name, nameZh, err := categoryName(ctx, env, *p.CategoryID)
					if err != nil {
						return nil, err
					}
					_, curZh, err := categoryName(ctx, env, cur.CategoryID)
					if err != nil {
						curZh = prod.Category.Name
					}
					diffs.add(fmt.Sprintf("category: %s -> %s", prod.Category.Name, name), "分类："+curZh+arrowZh+nameZh)
				}
			}
			if p.Code != nil {
				c := strings.TrimSpace(*p.Code)
				if c == cur.Code {
					p.Code = nil
				} else {
					p.Code = &c
					diffs.add(fmt.Sprintf("code: %q -> %q", cur.Code, c), "编号："+quoteOrNoneZh(cur.Code)+arrowZh+quoteZh(c))
				}
			}
			if p.PieceCount != nil {
				if *p.PieceCount < 0 {
					return nil, Userf("piece_count cannot be negative.")
				}
				if cur.PieceCount != nil && *cur.PieceCount == *p.PieceCount {
					p.PieceCount = nil
				} else {
					old, oldZh := "none", "无"
					if cur.PieceCount != nil {
						old = fmt.Sprint(*cur.PieceCount)
						oldZh = old
					}
					diffs.add(fmt.Sprintf("piece count: %s -> %d", old, *p.PieceCount), fmt.Sprintf("件数：%s%s%d", oldZh, arrowZh, *p.PieceCount))
				}
			}
			flag := func(name, nameZh string, want **bool, cur bool) {
				if *want == nil {
					return
				}
				if **want == cur {
					*want = nil
					return
				}
				diffs.add(fmt.Sprintf("%s: %s -> %s", name, yesNo(cur), yesNo(**want)), nameZh+"："+yesNoZh(cur)+arrowZh+yesNoZh(**want))
			}
			flag("halal", "清真", &p.IsHalal, cur.IsHalal)
			flag("spicy", "辣", &p.IsSpicy, cur.IsSpicy)
			flag("vegetarian", "素食", &p.IsVegetarian, cur.IsVegetarian)
			flag("lunch only", "仅限午餐", &p.IsLunchOnly, cur.IsLunchOnly)
			flag("discountable", "可打折", &p.IsDiscountable, cur.IsDiscountable)

			pr := &prepared{EntityType: "product", EntityID: prod.ID, Before: cur}
			if diffs.empty() {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("%s already has these details. Nothing changed.", ProductLabel(prod))
				pr.SummaryZh = productLabelZh(prod) + "已经是这些信息，无需更改。"
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("%s: %s", ProductLabel(prod), diffs.enJoined())
			pr.SummaryZh = productLabelZh(prod) + "：" + diffs.zhJoined()
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p ProductUpdateParams, _ []byte) (any, error) {
			prod, err := env.Up.Product(ctx, p.ProductID)
			if err != nil {
				return nil, err
			}
			cur := detailsOf(prod)
			in := upstream.UpdateProductInput{CategoryID: p.CategoryID, Code: p.Code, PieceCount: p.PieceCount,
				IsHalal: p.IsHalal, IsSpicy: p.IsSpicy, IsVegetarian: p.IsVegetarian, IsLunchOnly: p.IsLunchOnly, IsDiscountable: p.IsDiscountable}
			langs := map[string]bool{}
			for l := range p.Names {
				langs[l] = true
			}
			for l := range p.Descriptions {
				langs[l] = true
			}
			for _, l := range slices.Sorted(maps.Keys(langs)) {
				name := cur.Names[l]
				if n, ok := p.Names[l]; ok {
					name = n
				}
				if name == "" {
					return nil, Userf("A description in %s needs a %s name first.", l, l)
				}
				t := upstream.Translation{Language: l, Name: name}
				if d, ok := p.Descriptions[l]; ok {
					t.Description = &d
				} else if d, ok := cur.Descriptions[l]; ok {
					t.Description = &d
				}
				in.Translations = append(in.Translations, t)
			}
			updated, err := env.Up.UpdateProduct(ctx, p.ProductID, in)
			if err != nil {
				return nil, err
			}
			return detailsOf(updated), nil
		},
		Inverse: func(p ProductUpdateParams, b productDetails, _ json.RawMessage) (string, any, error) {
			inv := ProductUpdateParams{ProductID: p.ProductID}
			for l := range p.Names {
				if b.Names[l] == "" {
					return "", nil, Userf("The last change added a new translation, which cannot be removed automatically.")
				}
				if inv.Names == nil {
					inv.Names = map[string]string{}
				}
				inv.Names[l] = b.Names[l]
			}
			for l := range p.Descriptions {
				if inv.Descriptions == nil {
					inv.Descriptions = map[string]string{}
				}
				inv.Descriptions[l] = b.Descriptions[l]
			}
			if p.CategoryID != nil {
				inv.CategoryID = &b.CategoryID
			}
			if p.Code != nil {
				inv.Code = &b.Code
			}
			if p.PieceCount != nil {
				if b.PieceCount == nil {
					return "", nil, Userf("The previous piece count was empty and cannot be restored automatically.")
				}
				inv.PieceCount = b.PieceCount
			}
			restore := func(set *bool, dst **bool, v bool) {
				if set != nil {
					*dst = &v
				}
			}
			restore(p.IsHalal, &inv.IsHalal, b.IsHalal)
			restore(p.IsSpicy, &inv.IsSpicy, b.IsSpicy)
			restore(p.IsVegetarian, &inv.IsVegetarian, b.IsVegetarian)
			restore(p.IsLunchOnly, &inv.IsLunchOnly, b.IsLunchOnly)
			restore(p.IsDiscountable, &inv.IsDiscountable, b.IsDiscountable)
			return KindProductUpdate, inv, nil
		},
	}
}

// --- product creation (sensitive) ------------------------------------------

type ProductCreateParams struct {
	CategoryID     string            `json:"category_id"`
	Names          map[string]string `json:"names"`
	Descriptions   map[string]string `json:"descriptions,omitempty"`
	PriceCents     int64             `json:"price_cents"`
	VatCategory    string            `json:"vat_category"`
	Code           *string           `json:"code,omitempty"`
	PieceCount     *int              `json:"piece_count,omitempty"`
	IsHalal        bool              `json:"is_halal"`
	IsSpicy        bool              `json:"is_spicy"`
	IsVegetarian   bool              `json:"is_vegetarian"`
	IsLunchOnly    bool              `json:"is_lunch_only"`
	IsDiscountable bool              `json:"is_discountable"`
	Available      bool              `json:"available"`
	Visible        bool              `json:"visible"`
}

type createBefore struct {
	CategoryID string `json:"category_id"`
}

type createAfter struct {
	ProductID string `json:"product_id"`
}

func productCreate() handler {
	return spec[ProductCreateParams, createBefore]{
		Kind: KindProductCreate,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ProductCreateParams) (*prepared, error) {
			for lang, n := range p.Names {
				if err := validLanguage(lang); err != nil {
					return nil, err
				}
				p.Names[lang] = strings.TrimSpace(n)
				if p.Names[lang] == "" {
					delete(p.Names, lang)
				}
			}
			if p.Names["fr"] == "" {
				return nil, Userf("A new product needs at least a French name (names.fr).")
			}
			if p.Visible && len(p.Names) < 3 {
				return nil, Userf("A product shown on the menu needs a name in at least 3 languages (fr, en, zh). Add the missing names or create it hidden (visible=false).")
			}
			for lang := range p.Descriptions {
				if err := validLanguage(lang); err != nil {
					return nil, err
				}
				if p.Names[lang] == "" {
					return nil, Userf("A description in %s needs a %s name.", lang, lang)
				}
			}
			if p.PriceCents <= 0 {
				return nil, Userf("The price must be greater than 0.")
			}
			if p.VatCategory == "" {
				p.VatCategory = "food"
			}
			if !slices.Contains(VATCategories, p.VatCategory) {
				return nil, Userf("vat_category must be one of: %s.", strings.Join(VATCategories, ", "))
			}
			if p.PieceCount != nil && *p.PieceCount < 0 {
				return nil, Userf("piece_count cannot be negative.")
			}
			catName, catNameZh, err := categoryName(ctx, env, p.CategoryID)
			if err != nil {
				return nil, err
			}
			all, err := env.Up.Products(ctx)
			if err != nil {
				return nil, err
			}
			for i := range all {
				if strings.EqualFold(all[i].NameIn("fr"), p.Names["fr"]) {
					return nil, Userf("A product named %q already exists (id %s).", p.Names["fr"], all[i].ID)
				}
			}
			summary := fmt.Sprintf("New product %q in %s at %s, %s, %s", p.Names["fr"], catName, money.Format(p.PriceCents), availWord(p.Available), visWord(p.Visible))
			if zh := p.Names["zh"]; zh != "" {
				summary = fmt.Sprintf("New product %q (%s) in %s at %s, %s, %s", p.Names["fr"], zh, catName, money.Format(p.PriceCents), availWord(p.Available), visWord(p.Visible))
			}
			summaryZh := fmt.Sprintf("新商品%s，分类%s，价格 %s，%s，%s", quoteZh(nameZh(p.Names, p.Names["fr"])), catNameZh, moneyZh(p.PriceCents), availZh(p.Available), visZh(p.Visible))
			return &prepared{EntityType: "product", EntityID: "new", Before: createBefore{CategoryID: p.CategoryID}, Summary: summary, SummaryZh: summaryZh}, nil
		},
		Execute: func(ctx context.Context, env *Env, p ProductCreateParams, _ []byte) (any, error) {
			in := upstream.CreateProductInput{CategoryID: p.CategoryID, Code: p.Code, PieceCount: p.PieceCount, Price: money.FromCents(p.PriceCents), VatCategory: p.VatCategory,
				IsAvailable: p.Available, IsVisible: p.Visible, IsHalal: p.IsHalal, IsSpicy: p.IsSpicy, IsVegetarian: p.IsVegetarian, IsLunchOnly: p.IsLunchOnly, IsDiscountable: p.IsDiscountable}
			langs := slices.Sorted(maps.Keys(p.Names))
			sort.SliceStable(langs, func(i, j int) bool { return langs[i] == "fr" })
			for _, l := range langs {
				t := upstream.Translation{Language: l, Name: p.Names[l]}
				if d, ok := p.Descriptions[l]; ok {
					t.Description = &d
				}
				in.Translations = append(in.Translations, t)
			}
			prod, err := env.Up.CreateProduct(ctx, in)
			if err != nil {
				return nil, err
			}
			return createAfter{ProductID: prod.ID}, nil
		},
		Inverse: func(_ ProductCreateParams, _ createBefore, after json.RawMessage) (string, any, error) {
			var a createAfter
			if err := json.Unmarshal(after, &a); err != nil || a.ProductID == "" {
				return "", nil, ErrNotUndoable
			}
			// Products cannot be deleted upstream: undo hides it instead.
			return KindProductVisibility, ProductToggleParams{ProductID: a.ProductID, Value: false}, nil
		},
	}
}

// --- product image (sensitive, not undoable) --------------------------------

type ImageParams struct {
	ProductID        string `json:"product_id"`
	ImageURL         string `json:"image_url"`
	Filename         string `json:"filename"`
	ContentType      string `json:"content_type"`
	SizeBytes        int    `json:"size_bytes"`
	SHA256           string `json:"sha256"`
	RemoveBackground bool   `json:"remove_background"`
}

type imageBefore struct {
	ProductID string `json:"product_id"`
}

func productImage() handler {
	return spec[ImageParams, imageBefore]{
		Kind: KindProductImage,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ImageParams) (*prepared, error) {
			prod, err := getProduct(ctx, env, p.ProductID)
			if err != nil {
				return nil, err
			}
			bg := ""
			if p.RemoveBackground {
				bg = ", background removed"
			}
			return &prepared{EntityType: "product", EntityID: prod.ID, Before: imageBefore{ProductID: prod.ID},
				Summary:   fmt.Sprintf("%s: new photo (%s, %d KB%s). The previous photo cannot be restored afterwards.", ProductLabel(prod), p.ContentType, (p.SizeBytes+1023)/1024, bg),
				SummaryZh: productLabelZh(prod) + "：更换新照片，之后无法恢复原照片。"}, nil
		},
		Execute: func(ctx context.Context, env *Env, p ImageParams, blob []byte) (any, error) {
			if len(blob) == 0 {
				return nil, Userf("The photo data is missing. Please propose the photo again.")
			}
			if _, err := env.Up.UpdateProductImage(ctx, p.ProductID, p.RemoveBackground, p.Filename, p.ContentType, blob); err != nil {
				return nil, err
			}
			return map[string]any{"product_id": p.ProductID, "sha256": p.SHA256}, nil
		},
	}
}
