package actions

import (
	"errors"
	"strings"
	"testing"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

func (f *fixture) editProduct(id string, edit func(p *upstream.Product)) {
	f.fake.Lock()
	defer f.fake.Unlock()
	for _, p := range f.fake.Products {
		if p.ID == id {
			edit(p)
		}
	}
}

func TestGetProduct(t *testing.T) {
	f := newFixture(t)
	p, err := getProduct(f.ctx, f.svc.env, "p-maki-saumon")
	if err != nil || p.Name != "Maki saumon" {
		t.Fatal(p, err)
	}
	for _, id := range []string{"", "  "} {
		_, err = getProduct(f.ctx, f.svc.env, id)
		wantUserErr(t, err, "product_id is required")
	}
	_, err = getProduct(f.ctx, f.svc.env, "p-nope")
	wantUserErr(t, err, `No product with id "p-nope"`)
	f.fail("McpProduct")
	_, err = getProduct(f.ctx, f.svc.env, "p-maki-saumon")
	wantUpstreamErr(t, err)
}

func TestProductLabel(t *testing.T) {
	f := newFixture(t)
	p := f.fake.ProductByID("p-maki-saumon")
	if got := ProductLabel(&p); got != `Makis "Maki saumon" (卷「三文鱼卷」)` {
		t.Errorf("label = %s", got)
	}
	if got := productLabelZh(&p); got != "卷「三文鱼卷」" {
		t.Errorf("label zh = %s", got)
	}
	// Same Chinese and French name: no duplicate in brackets.
	same := p
	same.Translations = []upstream.Translation{{Language: "fr", Name: "Maki saumon"}, {Language: "zh", Name: "Maki saumon"}}
	if got := ProductLabel(&same); got != `Makis "Maki saumon"` {
		t.Errorf("same name label = %s", got)
	}
	// Without a category or a Chinese name only the French name remains.
	bare := upstream.Product{Name: "Thé", Translations: []upstream.Translation{{Language: "fr", Name: "Thé"}}}
	if got := ProductLabel(&bare); got != `"Thé"` {
		t.Errorf("bare label = %s", got)
	}
	if got := CategoryNameIn(upstream.CategoryRef{Name: "Boxes"}, "zh"); got != "Boxes" {
		t.Errorf("category fallback = %s", got)
	}
}

func TestProductToggleNoOpAndErrors(t *testing.T) {
	f := newFixture(t)
	for _, kind := range []string{KindProductAvailability, KindProductVisibility} {
		_, err := f.prepare(t, kind, ProductToggleParams{ProductID: "p-nope", Value: true})
		wantUserErr(t, err, "No product")
		_, err = f.prepare(t, kind, ProductToggleParams{ProductID: "", Value: true})
		wantUserErr(t, err, "product_id is required")
		// p-maki-saumon is available and visible.
		pr, err := f.prepare(t, kind, ProductToggleParams{ProductID: "p-maki-saumon", Value: true})
		if err != nil || !pr.NoOp {
			t.Fatalf("%s: want no-op, got %+v %v", kind, pr, err)
		}
		contains(t, kind+" summary", pr.Summary, "is already")
		contains(t, kind+" summary_zh", pr.SummaryZh, "无需更改")
	}
	pr, _ := f.prepare(t, KindProductVisibility, ProductToggleParams{ProductID: "p-maki-saumon", Value: true})
	contains(t, "visibility", pr.Summary, "already visible")
	pr, _ = f.prepare(t, KindProductAvailability, ProductToggleParams{ProductID: "p-creme", Value: false})
	contains(t, "availability", pr.Summary, "already unavailable")

	f.fail("McpUpdateProduct")
	_, err := f.svc.ApplyNow(f.ctx, "t", KindProductAvailability, ProductToggleParams{ProductID: "p-maki-saumon", Value: false}, "", nil)
	wantUpstreamErr(t, err)
	if !f.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Error("failed toggle changed the product")
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 1)
	if len(audit) != 1 || audit[0].Outcome != changes.OutcomeFailed {
		t.Errorf("audit: %+v", audit)
	}
}

func TestProductVisibilityHideAndUndo(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.ApplyNow(f.ctx, "set_product_visibility", KindProductVisibility, ProductToggleParams{ProductID: "p-sashimi-saumon", Value: false}, "", nil)
	if err != nil || !r.Applied {
		t.Fatal(r, err)
	}
	contains(t, "summary", r.Summary, "visible -> hidden")
	contains(t, "summary_zh", r.SummaryZh, "显示 → 隐藏")
	if f.fake.ProductByID("p-sashimi-saumon").IsVisible {
		t.Fatal("product still visible")
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "applied" || !f.fake.ProductByID("p-sashimi-saumon").IsVisible {
		t.Fatalf("undo: %+v %v", u, err)
	}
}

func TestBulkAvailabilityValidationAndNoOp(t *testing.T) {
	f := newFixture(t)
	_, err := f.prepare(t, KindProductAvailabilityBulk, BulkAvailabilityParams{})
	wantUserErr(t, err, "At least one product")

	many := BulkAvailabilityParams{}
	for range 101 {
		many.Items = append(many.Items, BulkAvailabilityItem{ProductID: "p-maki-saumon"})
	}
	_, err = f.prepare(t, KindProductAvailabilityBulk, many)
	wantUserErr(t, err, "At most 100")
	many.Items = many.Items[:100]
	if _, err = f.prepare(t, KindProductAvailabilityBulk, many); err != nil {
		t.Errorf("exactly 100 items must be accepted: %v", err)
	}

	_, err = f.prepare(t, KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-maki-saumon"}, {ProductID: "p-nope"}}})
	wantUserErr(t, err, `No product with id "p-nope"`)

	pr, err := f.prepare(t, KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-maki-saumon", Available: true}, {ProductID: "p-creme", Available: false}}})
	if err != nil || !pr.NoOp {
		t.Fatalf("want no-op: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "already in the requested state")
	contains(t, "summary_zh", pr.SummaryZh, "无需更改")

	f.fail("McpProducts")
	_, err = f.prepare(t, KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-maki-saumon"}}})
	wantUpstreamErr(t, err)
}

func TestBulkAvailabilityDedupesAndUndoes(t *testing.T) {
	f := newFixture(t)
	params := BulkAvailabilityParams{Items: []BulkAvailabilityItem{
		{ProductID: "p-maki-saumon", Available: false},
		{ProductID: "p-maki-saumon", Available: true}, // duplicate: the first one wins
		{ProductID: "p-creme", Available: true},
	}}
	p := f.propose(t, KindProductAvailabilityBulk, params)
	contains(t, "summary", p.Summary, "2 products", "available -> unavailable", "unavailable -> available")
	f.applyChange(t, KindProductAvailabilityBulk, params)
	if f.fake.ProductByID("p-maki-saumon").IsAvailable || !f.fake.ProductByID("p-creme").IsAvailable {
		t.Fatal("bulk change not applied")
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if !f.fake.ProductByID("p-maki-saumon").IsAvailable || f.fake.ProductByID("p-creme").IsAvailable {
		t.Error("undo did not restore availability")
	}
}

func TestBulkAvailabilityExecuteFailures(t *testing.T) {
	f := newFixture(t)
	// First product fails: nothing was changed, plain error.
	_, err := f.execute(t, KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-nope"}, {ProductID: "p-maki-saumon"}}}, nil)
	wantUpstreamErr(t, err)
	if strings.Contains(err.Error(), "already changed") {
		t.Errorf("no step ran, got %q", err)
	}
	// Second product fails: the owner is told one change already went through.
	_, err = f.execute(t, KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-maki-saumon"}, {ProductID: "p-nope"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "(1 of 2 products were already changed)") {
		t.Fatalf("partial failure message: %v", err)
	}
	if _, ok := errors.AsType[*upstream.Error](err); !ok {
		t.Error("partial failure must keep the upstream error in the chain")
	}
	if f.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Error("first product should have been changed")
	}
}

func TestPriceChange(t *testing.T) {
	f := newFixture(t)
	_, err := f.prepare(t, KindProductPrice, PriceParams{ProductID: "p-nope", NewPriceCents: 500})
	wantUserErr(t, err, "No product")
	_, err = f.prepare(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 676})
	wantUserErr(t, err, "Price change refused: a price change is limited to ±50% of the current price 4.50 EUR")

	// The same price is a no-op, never an error.
	pr, err := f.prepare(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 450})
	if err != nil || !pr.NoOp {
		t.Fatalf("same price: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "already costs 4.50 EUR")
	contains(t, "summary_zh", pr.SummaryZh, "价格已经是 4.50 欧元")

	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	contains(t, "summary", p.Summary, "price 4.50 EUR -> 5.00 EUR")
	contains(t, "summary_zh", p.SummaryZh, "价格：4.50 欧元 → 5.00 欧元")

	f.fail("McpUpdateProduct")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed {
		t.Errorf("status = %s", c.Status)
	}
	if f.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("price changed despite the failure")
	}
}

func TestPriceUndoIsNotBlockedByGuardrail(t *testing.T) {
	f := newFixture(t)
	// -50% is allowed; going back +100% is only allowed as an undo.
	f.applyChange(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 225})
	_, err := f.prepare(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 450})
	wantUserErr(t, err, "Price change refused")
	if _, err := f.prepare(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 450, SkipBounds: true}); err != nil {
		t.Fatalf("an undo must skip the guardrail: %v", err)
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "4.5" {
		t.Errorf("price after undo = %s", got)
	}
}

func TestVATChange(t *testing.T) {
	f := newFixture(t)
	_, err := f.prepare(t, KindProductVAT, VATParams{ProductID: "p-maki-saumon", VatCategory: "luxury"})
	wantUserErr(t, err, "vat_category must be one of: food, beverage, zero_rated, out_of_scope")
	_, err = f.prepare(t, KindProductVAT, VATParams{ProductID: "p-maki-saumon"})
	wantUserErr(t, err, "vat_category must be one of")
	_, err = f.prepare(t, KindProductVAT, VATParams{ProductID: "p-nope", VatCategory: "food"})
	wantUserErr(t, err, "No product")

	pr, err := f.prepare(t, KindProductVAT, VATParams{ProductID: "p-maki-saumon", VatCategory: " FOOD "})
	if err != nil || !pr.NoOp {
		t.Fatalf("same category: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "already has VAT category food")
	contains(t, "summary_zh", pr.SummaryZh, "增值税类别已经是餐食")

	p := f.propose(t, KindProductVAT, VATParams{ProductID: "p-maki-saumon", VatCategory: " Beverage "})
	contains(t, "summary", p.Summary, "VAT category food -> beverage")
	contains(t, "summary_zh", p.SummaryZh, "餐食 → 饮料")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").VatCategory; got != "beverage" {
		t.Fatalf("vat = %s", got)
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").VatCategory; got != "food" {
		t.Errorf("vat after undo = %s", got)
	}

	f.fail("McpUpdateProduct")
	_, err = f.execute(t, KindProductVAT, VATParams{ProductID: "p-maki-saumon", VatCategory: "beverage"}, nil)
	wantUpstreamErr(t, err)
	if vatLabelZh("exotic") != "exotic" {
		t.Error("unknown VAT labels fall back to the raw value")
	}
}

func TestDetailsOf(t *testing.T) {
	desc := "Rouleau"
	p := &upstream.Product{Code: sp("M1"), Category: upstream.CategoryRef{ID: "cat"}, PieceCount: ip(6), IsHalal: true,
		Translations: []upstream.Translation{{Language: "fr", Name: "Maki", Description: &desc}, {Language: "en", Name: "Maki EN"}}}
	d := detailsOf(p)
	if d.Code != "M1" || d.CategoryID != "cat" || *d.PieceCount != 6 || !d.IsHalal || d.Names["fr"] != "Maki" || d.Names["en"] != "Maki EN" ||
		d.Descriptions["fr"] != "Rouleau" || len(d.Descriptions) != 1 {
		t.Errorf("details = %+v", d)
	}
	if d := detailsOf(&upstream.Product{}); d.Code != "" || d.PieceCount != nil {
		t.Errorf("empty details = %+v", d)
	}
}

func TestProductUpdateValidation(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    ProductUpdateParams
		want string
	}{
		{"unknown product", ProductUpdateParams{ProductID: "p-nope"}, "No product"},
		{"unknown language", ProductUpdateParams{ProductID: "p-maki-saumon", Names: map[string]string{"de": "x"}}, `Unknown language "de"`},
		{"empty name", ProductUpdateParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "  "}}, "The fr name cannot be empty"},
		{"description language", ProductUpdateParams{ProductID: "p-maki-saumon", Descriptions: map[string]string{"de": "x"}}, "Unknown language"},
		{"unknown category", ProductUpdateParams{ProductID: "p-maki-saumon", CategoryID: sp("cat-nope")}, "No category with id"},
		{"negative piece count", ProductUpdateParams{ProductID: "p-maki-saumon", PieceCount: ip(-1)}, "piece_count cannot be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindProductUpdate, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}
	f.fail("McpCategories")
	_, err := f.prepare(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", CategoryID: sp("cat-box")})
	wantUpstreamErr(t, err)
}

func TestProductUpdateNoOp(t *testing.T) {
	f := newFixture(t)
	desc := "Un maki"
	f.editProduct("p-maki-saumon", func(p *upstream.Product) { p.Translations[0].Description = &desc })
	pr, err := f.prepare(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon",
		Names: map[string]string{"fr": " Maki saumon "}, Descriptions: map[string]string{"fr": "Un maki "}, CategoryID: sp("cat-maki"), Code: sp(" M1 "), PieceCount: ip(6),
		IsHalal: bp(false), IsSpicy: bp(false), IsVegetarian: bp(false), IsLunchOnly: bp(false), IsDiscountable: bp(true)})
	if err != nil || !pr.NoOp {
		t.Fatalf("want no-op, got %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "already has these details")
	contains(t, "summary_zh", pr.SummaryZh, "无需更改")
}

func TestProductUpdateAndUndo(t *testing.T) {
	f := newFixture(t)
	params := ProductUpdateParams{ProductID: "p-maki-saumon",
		Names: map[string]string{"fr": "Maki au saumon", "zh": "鲑鱼卷"}, Descriptions: map[string]string{"fr": "Frais"}, CategoryID: sp("cat-box"), Code: sp("MX9"), PieceCount: ip(8),
		IsHalal: bp(true), IsSpicy: bp(true), IsVegetarian: bp(true), IsLunchOnly: bp(true), IsDiscountable: bp(false)}
	p := f.propose(t, KindProductUpdate, params)
	contains(t, "summary", p.Summary, `name (fr): "Maki saumon" -> "Maki au saumon"`, `name (zh): "三文鱼卷" -> "鲑鱼卷"`, "description (fr) changed",
		"category: Makis -> Boxes", `code: "M1" -> "MX9"`, "piece count: 6 -> 8", "halal: no -> yes", "spicy: no -> yes", "vegetarian: no -> yes", "lunch only: no -> yes", "discountable: yes -> no")
	contains(t, "summary_zh", p.SummaryZh, "法语名称：「Maki saumon」 → 「Maki au saumon」", "法语描述已修改", "分类：卷 → 套餐", "编号：「M1」 → 「MX9」", "件数：6 → 8", "清真：否 → 是", "可打折：是 → 否")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	got := f.fake.ProductByID("p-maki-saumon")
	if got.Name != "Maki au saumon" || got.Category.ID != "cat-box" || *got.Code != "MX9" || *got.PieceCount != 8 || !got.IsHalal || !got.IsSpicy || !got.IsVegetarian || !got.IsLunchOnly || got.IsDiscountable {
		t.Fatalf("product after update: %+v", got)
	}
	var fr, zh upstream.Translation
	for _, tr := range got.Translations {
		switch tr.Language {
		case "fr":
			fr = tr
		case "zh":
			zh = tr
		}
	}
	if fr.Description == nil || *fr.Description != "Frais" || zh.Name != "鲑鱼卷" {
		t.Errorf("translations: %+v", got.Translations)
	}

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	got = f.fake.ProductByID("p-maki-saumon")
	if got.Name != "Maki saumon" || got.Category.ID != "cat-maki" || *got.Code != "M1" || *got.PieceCount != 6 || got.IsHalal || got.IsSpicy || got.IsVegetarian || got.IsLunchOnly || !got.IsDiscountable {
		t.Fatalf("product after undo: %+v", got)
	}
}

func TestProductUpdateFromEmptyPieceCountAndCode(t *testing.T) {
	f := newFixture(t)
	// p-sashimi-saumon has neither a piece count nor ... a code ("S1" is set), so use p-creme.
	p := f.propose(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-creme", Code: sp("D1"), PieceCount: ip(1)})
	contains(t, "summary", p.Summary, `code: "" -> "D1"`, "piece count: none -> 1")
	contains(t, "summary_zh", p.SummaryZh, "编号：无 → 「D1」", "件数：无 → 1")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	// A piece count that was empty cannot be restored.
	_, err := f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "piece count was empty")
}

func TestProductUpdateCategoryWhoseCurrentCategoryIsGone(t *testing.T) {
	f := newFixture(t)
	f.editProduct("p-maki-saumon", func(p *upstream.Product) { p.Category = upstream.CategoryRef{ID: "cat-ghost", Name: "Ancienne"} })
	p := f.propose(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", CategoryID: sp("cat-box")})
	contains(t, "summary", p.Summary, "category: Ancienne -> Boxes")
	contains(t, "summary_zh", p.SummaryZh, "分类：Ancienne → 套餐")
}

func TestProductUpdateInverse(t *testing.T) {
	f := newFixture(t)
	before := productDetails{Names: map[string]string{"fr": "A"}, Descriptions: map[string]string{}, CategoryID: "cat-maki", Code: "C", PieceCount: ip(4)}
	_, _, err := f.inverse(t, KindProductUpdate, ProductUpdateParams{ProductID: "p", Names: map[string]string{"zh": "新"}}, before, "{}")
	wantUserErr(t, err, "added a new translation")

	kind, inv, err := f.inverse(t, KindProductUpdate, ProductUpdateParams{ProductID: "p", Names: map[string]string{"fr": "B"}, Descriptions: map[string]string{"fr": "d"},
		CategoryID: sp("cat-box"), Code: sp("D"), PieceCount: ip(9), IsHalal: bp(true)}, before, "{}")
	got := inv.(ProductUpdateParams)
	if err != nil || kind != KindProductUpdate || got.Names["fr"] != "A" || got.Descriptions["fr"] != "" || *got.CategoryID != "cat-maki" || *got.Code != "C" || *got.PieceCount != 4 || *got.IsHalal {
		t.Errorf("inverse: %v %+v %v", kind, got, err)
	}
	if got.IsSpicy != nil || got.IsVegetarian != nil || got.IsLunchOnly != nil || got.IsDiscountable != nil {
		t.Errorf("only the changed flags are restored: %+v", got)
	}
}

func TestProductUpdateExecute(t *testing.T) {
	f := newFixture(t)
	// A description in a language needs a name: the product has none in nl.
	_, err := f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", Descriptions: map[string]string{"nl": "Lekker"}}, nil)
	wantUserErr(t, err, "A description in nl needs a nl name first")

	// A description alone keeps the existing name of that language.
	if _, err := f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", Descriptions: map[string]string{"en": "Tasty"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.fake.ProductByID("p-maki-saumon").Translations {
		if tr.Language == "en" && (tr.Name != "Salmon maki" || tr.Description == nil || *tr.Description != "Tasty") {
			t.Errorf("en translation: %+v", tr)
		}
	}
	// A name alone keeps the existing description of that language.
	if _, err := f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", Names: map[string]string{"en": "Salmon roll"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.fake.ProductByID("p-maki-saumon").Translations {
		if tr.Language == "en" && (tr.Name != "Salmon roll" || tr.Description == nil || *tr.Description != "Tasty") {
			t.Errorf("en translation after rename: %+v", tr)
		}
	}
	// A new language with name and description together.
	if _, err := f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", Names: map[string]string{"nl": "Zalmrol"}, Descriptions: map[string]string{"nl": "Lekker"}}, nil); err != nil {
		t.Fatal(err)
	}

	_, err = f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-nope", Code: sp("x")}, nil)
	wantUpstreamErr(t, err)
	f.fail("McpUpdateProduct")
	_, err = f.execute(t, KindProductUpdate, ProductUpdateParams{ProductID: "p-maki-saumon", Code: sp("x")}, nil)
	wantUpstreamErr(t, err)
}

func TestProductCreateValidation(t *testing.T) {
	f := newFixture(t)
	ok := func() ProductCreateParams {
		return ProductCreateParams{CategoryID: "cat-box", Names: map[string]string{"fr": "Tempura", "en": "Tempura", "zh": "天妇罗"}, PriceCents: 800}
	}
	tests := []struct {
		name string
		edit func(p *ProductCreateParams)
		want string
	}{
		{"unknown language", func(p *ProductCreateParams) { p.Names["de"] = "x" }, "Unknown language"},
		{"no french name", func(p *ProductCreateParams) { delete(p.Names, "fr") }, "French name"},
		{"blank french name", func(p *ProductCreateParams) { p.Names["fr"] = "   " }, "French name"},
		{"visible with two names", func(p *ProductCreateParams) { delete(p.Names, "zh"); p.Visible = true }, "at least 3 languages"},
		{"blank names do not count", func(p *ProductCreateParams) { p.Names["zh"] = " "; p.Visible = true }, "at least 3 languages"},
		{"description language", func(p *ProductCreateParams) { p.Descriptions = map[string]string{"de": "x"} }, "Unknown language"},
		{"description without name", func(p *ProductCreateParams) { p.Descriptions = map[string]string{"nl": "x"} }, "A description in nl needs a nl name"},
		{"zero price", func(p *ProductCreateParams) { p.PriceCents = 0 }, "price must be greater than 0"},
		{"negative price", func(p *ProductCreateParams) { p.PriceCents = -100 }, "price must be greater than 0"},
		{"bad vat", func(p *ProductCreateParams) { p.VatCategory = "luxury" }, "vat_category must be one of"},
		{"negative pieces", func(p *ProductCreateParams) { p.PieceCount = ip(-2) }, "piece_count cannot be negative"},
		{"unknown category", func(p *ProductCreateParams) { p.CategoryID = "cat-nope" }, "No category with id"},
		{"duplicate name", func(p *ProductCreateParams) { p.Names["fr"] = "maki SAUMON" }, `A product named "maki SAUMON" already exists (id p-maki-saumon)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ok()
			tt.edit(&p)
			_, err := f.prepare(t, KindProductCreate, p)
			wantUserErr(t, err, tt.want)
		})
	}
	if _, err := f.prepare(t, KindProductCreate, ok()); err != nil {
		t.Fatalf("valid params: %v", err)
	}
	f.fail("McpCategories")
	_, err := f.prepare(t, KindProductCreate, ok())
	wantUpstreamErr(t, err)
	f.unfail("McpCategories")
	f.fail("McpProducts")
	_, err = f.prepare(t, KindProductCreate, ok())
	wantUpstreamErr(t, err)
}

func TestProductCreateAndUndo(t *testing.T) {
	f := newFixture(t)
	params := ProductCreateParams{CategoryID: "cat-box", Names: map[string]string{"zh": "天妇罗", "en": "Tempura", "fr": " Tempura "}, Descriptions: map[string]string{"fr": "Croustillant"},
		PriceCents: 850, Code: sp("T1"), PieceCount: ip(5), IsSpicy: true, IsDiscountable: true, Available: true, Visible: true}
	p := f.propose(t, KindProductCreate, params)
	contains(t, "summary", p.Summary, `New product "Tempura" (天妇罗) in Boxes at 8.50 EUR, available, visible`)
	contains(t, "summary_zh", p.SummaryZh, "新商品「天妇罗」", "分类套餐", "价格 8.50 欧元", "可售", "显示")
	if len(f.fake.Products) != 4 {
		t.Fatal("proposal must not create the product")
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.Products) != 5 {
		t.Fatalf("products = %d", len(f.fake.Products))
	}
	created := f.fake.Products[4]
	if created.Name != "Tempura" || created.Price != "8.5" || created.VatCategory != "food" || *created.Code != "T1" || *created.PieceCount != 5 || !created.IsSpicy || !created.IsAvailable || !created.IsVisible || !created.IsDiscountable || created.Category.ID != "cat-box" {
		t.Fatalf("created product: %+v", created)
	}
	// French is sent first (it is the base language), the others sorted.
	if len(created.Translations) != 3 || created.Translations[0].Language != "fr" || created.Translations[1].Language != "en" || created.Translations[2].Language != "zh" {
		t.Errorf("translation order: %+v", created.Translations)
	}
	if created.Translations[0].Description == nil || *created.Translations[0].Description != "Croustillant" || created.Translations[1].Description != nil {
		t.Errorf("descriptions: %+v", created.Translations)
	}

	// Undo hides the product (products cannot be deleted upstream).
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.fake.ProductByID(created.ID).IsVisible {
		t.Error("undo must hide the new product")
	}
}

func TestProductCreateMinimalHiddenProduct(t *testing.T) {
	f := newFixture(t)
	params := ProductCreateParams{CategoryID: "cat-maki", Names: map[string]string{"fr": "Maki thon"}, PriceCents: 500}
	p := f.propose(t, KindProductCreate, params)
	contains(t, "summary", p.Summary, `New product "Maki thon" in Makis at 5.00 EUR, unavailable, hidden`)
	contains(t, "summary_zh", p.SummaryZh, "「Maki thon」", "售罄", "隐藏")
	f.applyChange(t, KindProductCreate, params)
	got := f.fake.Products[4]
	if got.VatCategory != "food" || len(got.Translations) != 1 || got.IsVisible {
		t.Errorf("created: %+v", got)
	}
	// Names only in French and German-less: a lone non-fr language is refused above.
	f.fail("McpCreateProduct")
	_, err := f.execute(t, KindProductCreate, params, nil)
	wantUpstreamErr(t, err)
}

func TestProductCreateInverseErrors(t *testing.T) {
	f := newFixture(t)
	for _, after := range []string{"", "junk", `{"product_id":""}`} {
		_, _, err := f.inverse(t, KindProductCreate, ProductCreateParams{}, createBefore{}, after)
		if !errors.Is(err, ErrNotUndoable) {
			t.Errorf("after %q: %v", after, err)
		}
	}
	kind, inv, err := f.inverse(t, KindProductCreate, ProductCreateParams{}, createBefore{}, `{"product_id":"p-9"}`)
	got := inv.(ProductToggleParams)
	if err != nil || kind != KindProductVisibility || got.ProductID != "p-9" || got.Value {
		t.Errorf("inverse: %v %+v %v", kind, got, err)
	}
}

func TestProductImage(t *testing.T) {
	f := newFixture(t)
	params := ImageParams{ProductID: "p-maki-saumon", ImageURL: "https://img.example/maki.png", Filename: "maki.png", ContentType: "image/png", SizeBytes: 2049, SHA256: "abc", RemoveBackground: true}
	_, err := f.prepare(t, KindProductImage, ImageParams{ProductID: "p-nope"})
	wantUserErr(t, err, "No product")

	blob := []byte("PNGDATA")
	prop, err := f.svc.Propose(f.ctx, "propose_product_image", KindProductImage, params, blob, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 2049 bytes round up to 3 KB.
	contains(t, "summary", prop.Summary, "new photo (image/png, 3 KB, background removed)", "cannot be restored")
	contains(t, "summary_zh", prop.SummaryZh, "更换新照片")
	noBg := params
	noBg.RemoveBackground, noBg.SizeBytes = false, 1024
	pr, err := f.prepare(t, KindProductImage, noBg)
	if err != nil || strings.Contains(pr.Summary, "background") || !strings.Contains(pr.Summary, "1 KB") {
		t.Errorf("summary without background removal: %+v %v", pr, err)
	}

	if _, err := f.svc.ApplyPending(f.ctx, prop.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if string(f.fake.Images["p-maki-saumon"]) != "PNGDATA" {
		t.Errorf("uploaded image = %q", f.fake.Images["p-maki-saumon"])
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil || !strings.Contains(err.Error(), "cannot be undone automatically") {
		t.Errorf("a photo change must not be undoable: %v", err)
	}
}

func TestProductImageExecuteErrors(t *testing.T) {
	f := newFixture(t)
	params := ImageParams{ProductID: "p-maki-saumon", Filename: "m.png", ContentType: "image/png", SHA256: "abc"}
	for _, blob := range [][]byte{nil, {}} {
		_, err := f.execute(t, KindProductImage, params, blob)
		wantUserErr(t, err, "photo data is missing")
	}
	f.fail("McpUpdateProduct")
	_, err := f.execute(t, KindProductImage, params, []byte("x"))
	wantUpstreamErr(t, err)
}
