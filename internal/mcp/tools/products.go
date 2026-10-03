package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/search"
	"tsb-service/internal/mcp/upstream"
)

// ProductOut is a product as returned by tools.
type ProductOut struct {
	ProductID      string            `json:"product_id"`
	Name           string            `json:"name" jsonschema:"French name"`
	Names          map[string]string `json:"names" jsonschema:"name per language (fr, en, zh, nl)"`
	CategoryID     string            `json:"category_id"`
	Category       string            `json:"category"`
	PriceCents     int64             `json:"price_cents"`
	Currency       string            `json:"currency"`
	Available      bool              `json:"available" jsonschema:"false means sold out"`
	Visible        bool              `json:"visible" jsonschema:"false means hidden from the menu"`
	Code           string            `json:"code,omitempty"`
	VatCategory    string            `json:"vat_category"`
	PieceCount     int               `json:"piece_count,omitempty"`
	IsHalal        bool              `json:"is_halal"`
	IsSpicy        bool              `json:"is_spicy"`
	IsVegetarian   bool              `json:"is_vegetarian"`
	IsLunchOnly    bool              `json:"is_lunch_only"`
	IsDiscountable bool              `json:"is_discountable"`
}

func productOut(p *upstream.Product) ProductOut {
	o := ProductOut{ProductID: p.ID, Name: p.NameIn("fr"), Names: map[string]string{}, CategoryID: p.Category.ID, Category: p.Category.Name,
		PriceCents: money.MustCents(p.Price), Currency: money.Currency, Available: p.IsAvailable, Visible: p.IsVisible, VatCategory: p.VatCategory,
		IsHalal: p.IsHalal, IsSpicy: p.IsSpicy, IsVegetarian: p.IsVegetarian, IsLunchOnly: p.IsLunchOnly, IsDiscountable: p.IsDiscountable}
	for _, t := range p.Translations {
		o.Names[t.Language] = t.Name
	}
	if p.Code != nil {
		o.Code = *p.Code
	}
	if p.PieceCount != nil {
		o.PieceCount = *p.PieceCount
	}
	return o
}

// --- search_products ---------------------------------------------------------

type SearchProductsIn struct {
	Query string `json:"query" jsonschema:"product name in any language (Chinese, French, English...), partial words and missing accents are fine; or the product code"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 10, max 50"`
}

type ProductMatch struct {
	ProductOut
	Score   int    `json:"score" jsonschema:"100 exact, 80 prefix, 60 word prefix, 40 substring, 20-30 typo"`
	Matched string `json:"matched" jsonschema:"the name or field that matched"`
}

type SearchProductsOut struct {
	Query   string         `json:"query"`
	Results []ProductMatch `json:"results"`
}

func productCandidates(ps []upstream.Product) []search.Candidate {
	cs := make([]search.Candidate, len(ps))
	for i := range ps {
		p := &ps[i]
		var names []string
		for _, t := range p.Translations {
			names = append(names, t.Name)
		}
		names = append(names, p.Name)
		if p.Code != nil {
			names = append(names, *p.Code)
		}
		cs[i] = search.Candidate{ID: p.ID, Primary: names, Secondary: []string{p.Category.Name}, Preferred: p.IsAvailable && p.IsVisible, SortKey: p.NameIn("fr")}
	}
	return cs
}

// --- product image download ------------------------------------------------------

const maxImageBytes = 5 << 20

var allowedImageTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}

// safeHTTPClient refuses to connect to loopback, private, link-local or
// unspecified addresses, so an image URL cannot be used to reach cluster
// internals.
func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("address %s is not allowed", host)
		}
		return nil
	}}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

func (d *Deps) downloadImage(ctx context.Context, raw string) ([]byte, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	httpsOrAllowed := u != nil && (u.Scheme == "https" || (d.AllowHTTPImages && u.Scheme == "http"))
	if err != nil || u.Host == "" || !httpsOrAllowed {
		return nil, "", "", actions.Userf("image_url must be an https link to the photo.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", actions.Userf("image_url is not a valid link.")
	}
	resp, err := d.ImageClient.Do(req)
	if err != nil {
		d.Log.Warn("image download failed", "error", err)
		return nil, "", "", actions.Userf("The photo could not be downloaded.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", actions.Userf("The photo could not be downloaded (HTTP %d).", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", "", actions.Userf("The photo could not be downloaded.")
	}
	if len(data) > maxImageBytes {
		return nil, "", "", actions.Userf("The photo is larger than 5 MB.")
	}
	ct := http.DetectContentType(data)
	if ct == "application/octet-stream" && len(data) > 12 && string(data[8:12]) == "WEBP" {
		ct = "image/webp"
	}
	ext, ok := allowedImageTypes[ct]
	if !ok {
		return nil, "", "", actions.Userf("The photo must be a JPEG, PNG or WebP image.")
	}
	name := strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
	if name == "" || name == "." || name == "/" {
		name = "photo"
	}
	return data, ct, name + ext, nil
}

// --- registration -------------------------------------------------------------------

func registerProducts(s *mcp.Server, d *Deps) {
	add(s, d, &mcp.Tool{
		Name: "search_products", Annotations: readOnly,
		Description: describe(`Find products by name (any language incl. Chinese), partial name or code. Case and accents are ignored; exact and prefix matches come first, then available products. Use it to get the product_id before any product action. Results include hidden and sold-out products.`,
			`search_products({"query": "三文鱼", "limit": 5})`),
	}, func(ctx context.Context, in SearchProductsIn) (SearchProductsOut, error) {
		ps, err := d.Up.Products(ctx)
		if err != nil {
			return SearchProductsOut{}, err
		}
		byID := map[string]*upstream.Product{}
		for i := range ps {
			byID[ps[i].ID] = &ps[i]
		}
		out := SearchProductsOut{Query: in.Query, Results: []ProductMatch{}}
		for _, r := range search.Rank(in.Query, productCandidates(ps), limitOr(in.Limit, 10, 50)) {
			out.Results = append(out.Results, ProductMatch{ProductOut: productOut(byID[r.ID]), Score: r.Score, Matched: r.Matched})
		}
		return out, nil
	})

	type GetProductIn struct {
		ProductID string `json:"product_id"`
	}
	type ChoiceOut struct {
		ChoiceID           string            `json:"choice_id"`
		Name               string            `json:"name"`
		Names              map[string]string `json:"names"`
		PriceModifierCents int64             `json:"price_modifier_cents" jsonschema:"surcharge added to the product price"`
		SortOrder          int               `json:"sort_order"`
	}
	type GroupOut struct {
		GroupID       string            `json:"group_id"`
		Name          string            `json:"name"`
		Names         map[string]string `json:"names"`
		MinSelections int               `json:"min_selections"`
		MaxSelections int               `json:"max_selections"`
		SortOrder     int               `json:"sort_order"`
		Choices       []ChoiceOut       `json:"choices"`
	}
	type GetProductOut struct {
		ProductOut
		Descriptions map[string]string `json:"descriptions"`
		ChoiceGroups []GroupOut        `json:"choice_groups"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_product", Annotations: readOnly,
		Description: describe(`Full details of one product: names and descriptions per language, price, flags, and its choice groups with their choices (ids, surcharges). Use it before changing choices.`,
			`get_product({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab"})`),
	}, func(ctx context.Context, in GetProductIn) (GetProductOut, error) {
		p, err := d.Up.Product(ctx, in.ProductID)
		if upstream.IsNotFound(err) {
			return GetProductOut{}, actions.Userf("No product with id %q. Use search_products to find the id.", in.ProductID)
		}
		if err != nil {
			return GetProductOut{}, err
		}
		out := GetProductOut{ProductOut: productOut(p), Descriptions: map[string]string{}, ChoiceGroups: []GroupOut{}}
		for _, t := range p.Translations {
			if t.Description != nil {
				out.Descriptions[t.Language] = *t.Description
			}
		}
		for _, g := range p.ChoiceGroups {
			go_ := GroupOut{GroupID: g.ID, Name: g.Name, Names: map[string]string{}, MinSelections: g.MinSelections, MaxSelections: g.MaxSelections, SortOrder: g.SortOrder, Choices: []ChoiceOut{}}
			for _, t := range g.Translations {
				go_.Names[t.Locale] = t.Name
			}
			for _, c := range g.Choices {
				co := ChoiceOut{ChoiceID: c.ID, Name: c.Name, Names: map[string]string{}, PriceModifierCents: money.MustCents(c.PriceModifier), SortOrder: c.SortOrder}
				for _, t := range c.Translations {
					co.Names[t.Locale] = t.Name
				}
				go_.Choices = append(go_.Choices, co)
			}
			out.ChoiceGroups = append(out.ChoiceGroups, go_)
		}
		return out, nil
	})

	type ListCategoriesIn struct {
		Query string `json:"query,omitempty" jsonschema:"optional name filter in any language"`
	}
	type CategoryOut struct {
		CategoryID string            `json:"category_id"`
		Name       string            `json:"name"`
		Names      map[string]string `json:"names"`
		Position   int               `json:"position"`
	}
	type ListCategoriesOut struct {
		Categories []CategoryOut `json:"categories"`
	}
	add(s, d, &mcp.Tool{
		Name: "list_categories", Annotations: readOnly,
		Description: describe(`List menu categories with their ids, optionally filtered by name (any language, partial words). Use it to find a category_id for product creation or moving a product.`,
			`list_categories({"query": "sashimi"})`),
	}, func(ctx context.Context, in ListCategoriesIn) (ListCategoriesOut, error) {
		cats, err := d.Up.Categories(ctx)
		if err != nil {
			return ListCategoriesOut{}, err
		}
		toOut := func(c upstream.Category) CategoryOut {
			o := CategoryOut{CategoryID: c.ID, Name: c.Name, Names: map[string]string{}, Position: c.Order}
			for _, t := range c.Translations {
				o.Names[t.Language] = t.Name
			}
			return o
		}
		out := ListCategoriesOut{Categories: []CategoryOut{}}
		if strings.TrimSpace(in.Query) == "" {
			slices.SortFunc(cats, func(a, b upstream.Category) int { return a.Order - b.Order })
			for _, c := range cats {
				out.Categories = append(out.Categories, toOut(c))
			}
			return out, nil
		}
		cands := make([]search.Candidate, len(cats))
		byID := map[string]upstream.Category{}
		for i, c := range cats {
			names := []string{c.Name, c.Slug}
			for _, t := range c.Translations {
				names = append(names, t.Name)
			}
			cands[i] = search.Candidate{ID: c.ID, Primary: names, SortKey: c.Name}
			byID[c.ID] = c
		}
		for _, r := range search.Rank(in.Query, cands, 0) {
			out.Categories = append(out.Categories, toOut(byID[r.ID]))
		}
		return out, nil
	})

	// --- low-risk writes ---

	type AvailabilityIn struct {
		ProductID string `json:"product_id"`
		Available bool   `json:"available" jsonschema:"false = sold out / not available today, true = available again"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "set_product_availability", Annotations: lowRisk,
		Description: describe(`Mark one product as sold out (available=false) or available again (true). Applied immediately and logged; reversible with undo_last_change. It is never switched back automatically. For several products at once use propose_bulk_availability. A repeated identical call changes nothing and returns no_op=true.`,
			`set_product_availability({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "available": false, "request_context": "今天三文鱼卖完了"})`),
	}, func(ctx context.Context, in AvailabilityIn) (ApplyOut, error) {
		return d.applyNow(ctx, "set_product_availability", actions.KindProductAvailability, actions.ProductToggleParams{ProductID: in.ProductID, Value: in.Available}, in.RequestContext)
	})

	type VisibilityIn struct {
		ProductID string `json:"product_id"`
		Visible   bool   `json:"visible" jsonschema:"false = hide from the menu, true = show on the menu (needs names in 3 languages)"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "set_product_visibility", Annotations: lowRisk,
		Description: describe(`Show or hide one product on the online menu. Hiding is how a product is removed (products cannot be deleted). Applied immediately and logged; reversible with undo_last_change.`,
			`set_product_visibility({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "visible": false, "request_context": "把这个菜从菜单上拿掉"})`),
	}, func(ctx context.Context, in VisibilityIn) (ApplyOut, error) {
		return d.applyNow(ctx, "set_product_visibility", actions.KindProductVisibility, actions.ProductToggleParams{ProductID: in.ProductID, Value: in.Visible}, in.RequestContext)
	})

	// --- sensitive proposals ---

	type BulkIn struct {
		ProductIDs []string `json:"product_ids" jsonschema:"products to change, up to 100"`
		Available  bool     `json:"available"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_bulk_availability", Annotations: proposeOnly,
		Description: describe(`Propose marking several products sold out or available at once (e.g. every salmon dish). Creates a pending change only; products already in the requested state are skipped and listed.`,
			`propose_bulk_availability({"product_ids": ["id-1", "id-2"], "available": false, "request_context": "所有三文鱼的都没有了"})`),
	}, func(ctx context.Context, in BulkIn) (actions.Proposal, error) {
		p := actions.BulkAvailabilityParams{}
		for _, id := range in.ProductIDs {
			p.Items = append(p.Items, actions.BulkAvailabilityItem{ProductID: id, Available: in.Available})
		}
		return d.propose(ctx, "propose_bulk_availability", actions.KindProductAvailabilityBulk, p, nil, in.RequestContext)
	})

	type PriceIn struct {
		ProductID     string `json:"product_id"`
		NewPriceCents int64  `json:"new_price_cents" jsonschema:"new price in euro cents, e.g. 1450 for 14.50 EUR"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_price_change", Annotations: proposeOnly,
		Description: describe(`Propose a new price for one product. Creates a pending change only. Refused when the price is 0 or negative, or differs from the current price by more than the configured limit (default ±50%); the error states the allowed range.`,
			`propose_price_change({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "new_price_cents": 1450, "request_context": "卷寿司套餐现在14.50"})`),
	}, func(ctx context.Context, in PriceIn) (actions.Proposal, error) {
		return d.propose(ctx, "propose_price_change", actions.KindProductPrice, actions.PriceParams{ProductID: in.ProductID, NewPriceCents: in.NewPriceCents}, nil, in.RequestContext)
	})

	type VATIn struct {
		ProductID   string `json:"product_id"`
		VatCategory string `json:"vat_category" jsonschema:"food, beverage, zero_rated or out_of_scope"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_vat_category_change", Annotations: proposeOnly,
		Description: describe(`Propose changing a product's VAT category (food, beverage, zero_rated, out_of_scope). This changes the VAT on future orders. Creates a pending change only.`,
			`propose_vat_category_change({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "vat_category": "beverage", "request_context": "这个是饮料"})`),
	}, func(ctx context.Context, in VATIn) (actions.Proposal, error) {
		return d.propose(ctx, "propose_vat_category_change", actions.KindProductVAT, actions.VATParams{ProductID: in.ProductID, VatCategory: in.VatCategory}, nil, in.RequestContext)
	})

	type ProductUpdateIn struct {
		ProductID      string            `json:"product_id"`
		Names          map[string]string `json:"names,omitempty" jsonschema:"new names by language code (fr, en, zh, nl); only the given languages change"`
		Descriptions   map[string]string `json:"descriptions,omitempty" jsonschema:"new descriptions by language code"`
		CategoryID     string            `json:"category_id,omitempty" jsonschema:"move to this category (see list_categories)"`
		Code           string            `json:"code,omitempty"`
		PieceCount     *int              `json:"piece_count,omitempty"`
		IsHalal        *bool             `json:"is_halal,omitempty"`
		IsSpicy        *bool             `json:"is_spicy,omitempty"`
		IsVegetarian   *bool             `json:"is_vegetarian,omitempty"`
		IsLunchOnly    *bool             `json:"is_lunch_only,omitempty"`
		IsDiscountable *bool             `json:"is_discountable,omitempty" jsonschema:"whether the pickup discount applies"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_product_update", Annotations: proposeOnly,
		Description: describe(`Propose changing product details: names or descriptions per language, category, code, piece count, halal/spicy/vegetarian/lunch-only/discountable flags. Only the given fields change. Price, VAT, availability, visibility and photo have their own tools. Creates a pending change only.`,
			`propose_product_update({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "names": {"zh": "三文鱼卷"}, "is_spicy": true, "request_context": "改中文名字"})`),
	}, func(ctx context.Context, in ProductUpdateIn) (actions.Proposal, error) {
		p := actions.ProductUpdateParams{ProductID: in.ProductID, Names: in.Names, Descriptions: in.Descriptions, PieceCount: in.PieceCount,
			IsHalal: in.IsHalal, IsSpicy: in.IsSpicy, IsVegetarian: in.IsVegetarian, IsLunchOnly: in.IsLunchOnly, IsDiscountable: in.IsDiscountable}
		if in.CategoryID != "" {
			p.CategoryID = &in.CategoryID
		}
		if in.Code != "" {
			p.Code = &in.Code
		}
		return d.propose(ctx, "propose_product_update", actions.KindProductUpdate, p, nil, in.RequestContext)
	})

	type ProductCreateIn struct {
		CategoryID     string            `json:"category_id" jsonschema:"see list_categories"`
		Names          map[string]string `json:"names" jsonschema:"names by language code; fr is required, fr+en+zh needed to be visible"`
		Descriptions   map[string]string `json:"descriptions,omitempty"`
		PriceCents     int64             `json:"price_cents"`
		VatCategory    string            `json:"vat_category,omitempty" jsonschema:"default food"`
		Code           string            `json:"code,omitempty"`
		PieceCount     *int              `json:"piece_count,omitempty"`
		IsHalal        bool              `json:"is_halal,omitempty"`
		IsSpicy        bool              `json:"is_spicy,omitempty"`
		IsVegetarian   bool              `json:"is_vegetarian,omitempty"`
		IsLunchOnly    bool              `json:"is_lunch_only,omitempty"`
		IsDiscountable *bool             `json:"is_discountable,omitempty" jsonschema:"default true"`
		Available      *bool             `json:"available,omitempty" jsonschema:"default true"`
		Visible        *bool             `json:"visible,omitempty" jsonschema:"default true"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_product_creation", Annotations: proposeOnly,
		Description: describe(`Propose a new menu product (without photo; add one afterwards with propose_product_image). Refused if a product with the same French name exists. Creates a pending change only.`,
			`propose_product_creation({"category_id": "cat-id", "names": {"fr": "Maki avocat", "en": "Avocado maki", "zh": "牛油果卷"}, "price_cents": 450, "piece_count": 6, "is_vegetarian": true, "request_context": "加一个牛油果卷 4块5"})`),
	}, func(ctx context.Context, in ProductCreateIn) (actions.Proposal, error) {
		p := actions.ProductCreateParams{CategoryID: in.CategoryID, Names: in.Names, Descriptions: in.Descriptions, PriceCents: in.PriceCents, VatCategory: in.VatCategory,
			PieceCount: in.PieceCount, IsHalal: in.IsHalal, IsSpicy: in.IsSpicy, IsVegetarian: in.IsVegetarian, IsLunchOnly: in.IsLunchOnly,
			IsDiscountable: valueOr(in.IsDiscountable, true), Available: valueOr(in.Available, true), Visible: valueOr(in.Visible, true)}
		if p.Names == nil {
			p.Names = map[string]string{}
		}
		if in.Code != "" {
			p.Code = &in.Code
		}
		return d.propose(ctx, "propose_product_creation", actions.KindProductCreate, p, nil, in.RequestContext)
	})

	type ImageIn struct {
		ProductID        string `json:"product_id"`
		ImageURL         string `json:"image_url" jsonschema:"https link to the photo (JPEG, PNG or WebP, max 5 MB)"`
		RemoveBackground bool   `json:"remove_background,omitempty" jsonschema:"remove the photo background automatically"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_product_image", Annotations: proposeOnly,
		Description: describe(`Propose a new photo for a product. The photo is downloaded now and stored with the pending change, so exactly this photo is uploaded on confirmation. The previous photo cannot be restored. Creates a pending change only.`,
			`propose_product_image({"product_id": "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab", "image_url": "https://example.com/maki.jpg", "remove_background": true, "request_context": "用这张照片"})`),
	}, func(ctx context.Context, in ImageIn) (actions.Proposal, error) {
		data, ct, name, err := d.downloadImage(ctx, in.ImageURL)
		if err != nil {
			return actions.Proposal{}, err
		}
		sum := sha256.Sum256(data)
		p := actions.ImageParams{ProductID: in.ProductID, ImageURL: in.ImageURL, Filename: name, ContentType: ct, SizeBytes: len(data), SHA256: hex.EncodeToString(sum[:]), RemoveBackground: in.RemoveBackground}
		return d.propose(ctx, "propose_product_image", actions.KindProductImage, p, data, in.RequestContext)
	})
}

// ApplyOut is returned by low-risk tools.
type ApplyOut struct {
	Applied    bool   `json:"applied"`
	NoOp       bool   `json:"no_op" jsonschema:"true when the state was already as requested"`
	Summary    string `json:"summary"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Before     any    `json:"before,omitempty"`
	After      any    `json:"after,omitempty"`
}

func (d *Deps) applyNow(ctx context.Context, tool, kind string, params any, rc string) (ApplyOut, error) {
	r, err := d.Svc.ApplyNow(ctx, tool, kind, params, rc, nil)
	if err != nil {
		return ApplyOut{}, err
	}
	return ApplyOut{Applied: r.Applied, NoOp: r.NoOp, Summary: r.Summary, EntityType: r.EntityType, EntityID: r.EntityID, Before: toAny(r.Before), After: toAny(r.After)}, nil
}

func (d *Deps) propose(ctx context.Context, tool, kind string, params any, blob []byte, rc string) (actions.Proposal, error) {
	p, err := d.Svc.Propose(ctx, tool, kind, params, blob, rc, nil)
	if err != nil {
		return actions.Proposal{}, err
	}
	return *p, nil
}
