package application_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// catalogOf wraps catalog data the way GET /catalogs/:id serves it.
func catalogOf(d hubrise.CatalogData) *hubrise.Catalog {
	return &hubrise.Catalog{ID: "cat", Name: "Menu", Data: &d}
}

func money(s string) hubrise.Money { return hubrise.Money(s + " EUR") }

// sku is a SKU with a price; the option lists are named by ref (the upload form).
func sku(ref, price string, lists ...string) hubrise.Sku {
	return hubrise.Sku{Ref: ref, Price: money(price), OptionListRefs: lists}
}

func product(ref, category, name string, skus ...hubrise.Sku) hubrise.Product {
	return hubrise.Product{Ref: ref, CategoryRef: category, Name: name, Skus: skus,
		TaxRate: &hubrise.TaxRate{Collection: "6", Delivery: "6", EatIn: "12"}}
}

func warningCodes(c *domain.Catalog) []domain.WarningCode {
	var out []domain.WarningCode
	for _, w := range c.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func productByKey(t *testing.T, c *domain.Catalog, key string) domain.CatalogProduct {
	t.Helper()
	for _, p := range c.Products {
		if p.Key == key {
			return p
		}
	}
	require.Failf(t, "product not found", "no product %q in %+v", key, c.Products)
	return domain.CatalogProduct{}
}

func TestMapCatalogOfTheRecordedMenu(t *testing.T) {
	srv := hubrisetest.New(t)
	cat, err := srv.NewClient(t).GetCatalog(context.Background(), hubrisetest.CatalogID)
	require.NoError(t, err)

	got := application.MapCatalog(cat, application.MapOptions{})
	assert.Equal(t, "fr", got.Language)
	// The soft drink is taxed 6 % to take away and 21 % to eat in, which no VAT category of the shop
	// expresses (a beverage is 21 % everywhere): it is reported and hidden, never guessed.
	require.Len(t, got.Warnings, 1)
	assert.Equal(t, domain.WarnVATUnmapped, got.Warnings[0].Code)
	assert.Equal(t, "COCA-33", got.Warnings[0].Key)

	require.Len(t, got.Categories, 3)
	assert.Equal(t, "MAKI", got.Categories[0].Key)
	assert.Equal(t, map[string]string{"fr": "Makis"}, got.Categories[0].Names)
	assert.Equal(t, []int{0, 1, 2}, []int{got.Categories[0].Order, got.Categories[1].Order, got.Categories[2].Order})

	keys := make([]string, len(got.Products))
	for i, p := range got.Products {
		keys[i] = p.Key
	}
	assert.Equal(t, []string{"MAKI-SAUMON-6", "MAKI-SAUMON-12", "CALIFORNIA-8", "MISO-1", "COCA-33", "ASAHI-33"}, keys)

	six := productByKey(t, got, "MAKI-SAUMON-6")
	assert.Equal(t, "Maki saumon 6 pièces", six.Texts["fr"].Name, "product name plus SKU name")
	assert.True(t, six.Price.Equal(dec("5.5")))
	assert.Equal(t, "food", six.VatCategory)
	assert.True(t, six.Visible)
	assert.True(t, six.Discountable)
	require.Len(t, six.Groups, 1)
	assert.Equal(t, "MAKI-SAUMON-6|SAUCE", six.Groups[0].Key)
	assert.Equal(t, 0, six.Groups[0].Min)
	assert.Equal(t, 1, six.Groups[0].Max)
	assert.Equal(t, "MAKI-SAUMON-6|SAUCE|SAUCE-SUCREE", six.Groups[0].Choices[1].Key)
	assert.True(t, six.Groups[0].Choices[1].PriceModifier.Equal(dec("0.5")))

	cali := productByKey(t, got, "CALIFORNIA-8")
	assert.Equal(t, "California roll", cali.Texts["fr"].Name, "an unnamed SKU adds nothing to the name")
	assert.Equal(t, "加州卷", cali.Texts["zh"].Name, "translations come from the SKU custom fields")
	require.Len(t, cali.Groups, 2)
	extra := cali.Groups[1]
	assert.Equal(t, 1, extra.Sort)
	assert.Equal(t, 2, extra.Max, "no maximum: the number of options")

	coca := productByKey(t, got, "COCA-33")
	assert.Empty(t, coca.VatCategory)
	assert.False(t, coca.Visible)
	assert.Equal(t, "beverage", productByKey(t, got, "ASAHI-33").VatCategory)

	// The upload form of the same catalog (refs instead of ids) maps to the same menu.
	again := application.MapCatalog(catalogOf(hubrisetest.SeedCatalog()), application.MapOptions{})
	require.Len(t, again.Products, len(got.Products))
	for i, p := range got.Products {
		assert.Equal(t, p.Key, again.Products[i].Key)
		assert.Equal(t, p.CategoryKey, again.Products[i].CategoryKey)
		assert.True(t, p.Price.Equal(again.Products[i].Price))
		assert.Len(t, again.Products[i].Groups, len(p.Groups))
	}
}

func TestMapCatalogOfNothing(t *testing.T) {
	for name, c := range map[string]*hubrise.Catalog{"nil": nil, "no data": {ID: "c"}, "empty data": catalogOf(hubrise.CatalogData{})} {
		t.Run(name, func(t *testing.T) {
			got := application.MapCatalog(c, application.MapOptions{Language: "zz"})
			assert.Equal(t, "fr", got.Language, "an unknown language falls back to French")
			assert.Empty(t, got.Products)
			assert.NotNil(t, got.Products)
			assert.NotNil(t, got.Categories)
		})
	}
}

func TestMapCategoryTree(t *testing.T) {
	desc := `{"translations":{"fr":"Entrées","en":"Starters","zh":"前菜","xx":"ignored","nl":"  "}}`
	got := application.MapCatalog(catalogOf(hubrise.CatalogData{
		Categories: []hubrise.Category{
			{ID: "c-menu", Ref: "MENU", Name: "Menu"},                                                // a parent without products
			{ID: "c-start", Ref: "START", Name: "Entrées", ParentID: "c-menu", Description: desc},    // child by id
			{ID: "c-main", Ref: "MAIN", Name: "Plats", ParentRef: "MENU", Description: "plain text"}, // child by ref
			{ID: "c-solo", Ref: "SOLO", Name: "Seul"},
			{ID: "c-loop-a", Ref: "LA", Name: "Boucle A", ParentRef: "LB"},
			{ID: "c-loop-b", Ref: "LB", Name: "Boucle B", ParentRef: "LA"},
			{ID: "c-self", Ref: "SELF", Name: "Soi", ParentRef: "SELF"},
			{ID: "c-noref", Name: "Sans ref"},
		},
		Products: []hubrise.Product{
			product("P1", "SOLO", "Un", sku("S1", "1")),
			product("P2", "MAIN", "Deux", sku("S2", "2")),
			product("P3", "START", "Trois", sku("S3", "3")),
			{Ref: "P4", CategoryID: "c-loop-a", Name: "Quatre", Skus: []hubrise.Sku{sku("S4", "4")}, TaxRate: &hubrise.TaxRate{Collection: "6", EatIn: "12"}},
			product("P5", "SELF", "Cinq", sku("S5", "5")),
			product("P6", "NOWHERE", "Six", sku("S6", "6")),
		},
	}), application.MapOptions{Language: "fr"})

	var keys []string
	order := map[string]int{}
	for _, c := range got.Categories {
		keys = append(keys, c.Key)
		order[c.Key] = c.Order
	}
	// Depth first: MENU (no products, ignored) then its children in catalog order.
	assert.Equal(t, []string{"START", "MAIN", "SOLO", "SELF", "LA"}, keys, "only categories with products, depth first; a cycle is cut")
	assert.Less(t, order["START"], order["MAIN"])
	assert.Less(t, order["MAIN"], order["SOLO"])
	assert.Equal(t, map[string]string{"fr": "Entrées", "en": "Starters", "zh": "前菜"}, got.Categories[0].Names,
		"the name wins for the catalog language; other languages come from the JSON; blank and unknown ones are dropped")
	assert.Equal(t, map[string]string{"fr": "Plats"}, got.Categories[1].Names, "a plain description is not a translation")

	assert.ElementsMatch(t, []domain.WarningCode{domain.WarnMissingRef, domain.WarnUnknownCategory}, warningCodes(got))
	assert.Len(t, got.Products, 5)
}

func TestMapProductNamesAndTranslations(t *testing.T) {
	custom, err := json.Marshal(map[string]any{
		"code": " A2 ", "vat_category": "beverage",
		"translations": map[string]any{
			"fr": map[string]string{"name": "ignored for fr", "description": "desc fr custom"},
			"en": map[string]string{"name": "Coke", "description": "A soda"},
			"nl": map[string]string{"name": ""},
			"xx": map[string]string{"name": "unknown language"},
		},
	})
	require.NoError(t, err)

	p := product("COLA", "DRINK", "Cola", hubrise.Sku{Ref: "COLA-33", Name: "33cl", Price: money("3"), CustomFields: custom},
		hubrise.Sku{Ref: "COLA-50", Name: "  ", Price: money("4"), CustomFields: json.RawMessage(`{broken`)},
		hubrise.Sku{Ref: "COLA-X", Price: money("5"), CustomFields: json.RawMessage(`{"code": 12}`)})
	p.Description = ""
	got := application.MapCatalog(catalogOf(hubrise.CatalogData{
		Categories: []hubrise.Category{{Ref: "DRINK", Name: "Boissons"}},
		Products:   []hubrise.Product{p},
	}), application.MapOptions{Language: "fr"})

	cola := productByKey(t, got, "COLA-33")
	assert.Equal(t, "Cola 33cl", cola.Texts["fr"].Name)
	assert.Equal(t, "desc fr custom", cola.Texts["fr"].Description, "no product description: the custom field one")
	assert.Equal(t, domain.Text{Name: "Coke", Description: "A soda"}, cola.Texts["en"])
	assert.NotContains(t, cola.Texts, "nl", "a blank translation is dropped")
	assert.NotContains(t, cola.Texts, "xx")
	assert.Equal(t, "A2", cola.Code)
	assert.Equal(t, "beverage", cola.VatCategory, "the custom field wins over the tax rate")

	assert.Equal(t, "Cola", productByKey(t, got, "COLA-50").Texts["fr"].Name, "a blank SKU name adds nothing")
	assert.Empty(t, productByKey(t, got, "COLA-50").Code, "broken custom fields are ignored")
	assert.Empty(t, productByKey(t, got, "COLA-X").Code, "a numeric code is not a code")

	// A product description beats the custom field, and the catalog language is configurable.
	p.Description = "Soda"
	en := application.MapCatalog(catalogOf(hubrise.CatalogData{
		Categories: []hubrise.Category{{Ref: "DRINK", Name: "Drinks", Description: `{"translations":{"fr":"Boissons"}}`}},
		Products:   []hubrise.Product{p},
	}), application.MapOptions{Language: "en"})
	assert.Equal(t, "en", en.Language)
	assert.Equal(t, domain.Text{Name: "Cola 33cl", Description: "Soda"}, productByKey(t, en, "COLA-33").Texts["en"])
	assert.Equal(t, "ignored for fr", productByKey(t, en, "COLA-33").Texts["fr"].Name, "French comes from the custom field when English is the catalog language")
	assert.Equal(t, map[string]string{"fr": "Boissons", "en": "Drinks"}, en.Categories[0].Names)
}

func TestMapVatCategory(t *testing.T) {
	rate := func(collection, eatIn string) *hubrise.TaxRate {
		return &hubrise.TaxRate{Collection: collection, EatIn: eatIn}
	}
	cases := []struct {
		name   string
		custom string
		rate   *hubrise.TaxRate
		want   string
		warn   bool
	}{
		{"custom field", `{"vat_category":"zero_rated"}`, rate("6", "12"), "zero_rated", false},
		{"custom field out of scope", `{"vat_category":"out_of_scope"}`, nil, "out_of_scope", false},
		{"food rates", ``, rate("6", "12"), "food", false},
		{"food rates with decimals", ``, rate("6.0", "12.00"), "food", false},
		{"beverage rates", ``, rate("21", "21"), "beverage", false},
		{"zero rates", ``, rate("0", "0"), "zero_rated", false},
		{"unknown custom value falls back to the rates", `{"vat_category":"weird"}`, rate("6", "12"), "food", false},
		{"unusual rates", ``, rate("9", "12"), "", true},
		{"no rates", ``, nil, "", true},
		{"no eat in rate", ``, rate("6", ""), "", true},
		{"unreadable rate", ``, rate("six", "12"), "", true},
		{"unreadable eat in rate", ``, rate("6", "x"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sku("S", "1")
			if c.custom != "" {
				s.CustomFields = json.RawMessage(c.custom)
			}
			p := product("P", "C", "Plat", s)
			p.TaxRate = c.rate
			got := application.MapCatalog(catalogOf(hubrise.CatalogData{Categories: []hubrise.Category{{Ref: "C", Name: "C"}}, Products: []hubrise.Product{p}}), application.MapOptions{})
			prod := productByKey(t, got, "S")
			assert.Equal(t, c.want, prod.VatCategory)
			assert.Equal(t, c.want != "", prod.Visible, "an unmapped VAT category hides the product")
			if c.warn {
				assert.Equal(t, []domain.WarningCode{domain.WarnVATUnmapped}, warningCodes(got))
			} else {
				assert.Empty(t, got.Warnings)
			}
		})
	}
}

func TestMapTagsAndImages(t *testing.T) {
	build := func(productTags, skuTags []string) domain.CatalogProduct {
		s := sku("S", "1")
		s.Tags = skuTags
		p := product("P", "C", "Plat", s)
		p.Tags = productTags
		p.ImageIDs = []string{"", "img-1"}
		got := application.MapCatalog(catalogOf(hubrise.CatalogData{Categories: []hubrise.Category{{Ref: "C", Name: "C"}}, Products: []hubrise.Product{p}}), application.MapOptions{})
		return productByKey(t, got, "S")
	}

	plain := build(nil, nil)
	assert.True(t, plain.Visible)
	assert.True(t, plain.Discountable)
	assert.False(t, plain.Halal || plain.Vegetarian || plain.Spicy)
	assert.Equal(t, "img-1", plain.ImageID)

	tagged := build([]string{" Halal ", "VEGAN"}, []string{"spicy", "no-discount"})
	assert.True(t, tagged.Halal && tagged.Vegetarian && tagged.Spicy)
	assert.False(t, tagged.Discountable)
	assert.True(t, tagged.Visible)

	assert.False(t, build([]string{"hidden"}, nil).Visible)
	assert.False(t, build(nil, []string{"web:hidden"}).Visible)
	assert.True(t, build([]string{"vegetarian"}, nil).Vegetarian)

	// The documentation's other spelling of image_ids.
	p := product("P", "C", "Plat", sku("S", "1"))
	p.ImagesIDs = []string{"alt-img"}
	got := application.MapCatalog(catalogOf(hubrise.CatalogData{Categories: []hubrise.Category{{Ref: "C", Name: "C"}}, Products: []hubrise.Product{p}}), application.MapOptions{})
	assert.Equal(t, "alt-img", productByKey(t, got, "S").ImageID)
}

func TestMapSkuProblems(t *testing.T) {
	bad := sku("BAD", "1")
	bad.Price = "oops"
	noRef := sku("", "1")
	got := application.MapCatalog(catalogOf(hubrise.CatalogData{
		Categories: []hubrise.Category{{Ref: "C", Name: "C"}},
		Products: []hubrise.Product{
			product("P1", "C", "Un", sku("DUP", "1"), noRef, bad),
			product("P2", "C", "Deux", sku("DUP", "2"), sku("OK", "3")),
		},
	}), application.MapOptions{})

	assert.Equal(t, []string{"DUP", "OK"}, []string{got.Products[0].Key, got.Products[1].Key})
	assert.True(t, got.Products[0].Price.Equal(dec("1")), "the first use of a ref wins")
	assert.ElementsMatch(t, []domain.WarningCode{domain.WarnMissingRef, domain.WarnInvalidPrice, domain.WarnDuplicateRef}, warningCodes(got))
}

func TestMapPricesAndVariants(t *testing.T) {
	ov := func(price string, variants ...string) hubrise.PriceOverride {
		return hubrise.PriceOverride{VariantRefs: variants, Price: money(price)}
	}
	window := hubrise.PriceOverride{StartTime: "17:00", Price: money("1")}
	window2 := hubrise.PriceOverride{DOW: "12", Price: money("1")}
	service := hubrise.PriceOverride{ServiceTypes: []string{"delivery"}, Price: money("1")}
	broken := hubrise.PriceOverride{Price: "nope"}

	cases := []struct {
		name      string
		variant   string
		overrides []hubrise.PriceOverride
		want      string
		warns     []domain.WarningCode
	}{
		{"no override", "", nil, "10", nil},
		{"override for the variant", "uber", []hubrise.PriceOverride{ov("12", "uber")}, "12", nil},
		{"another variant is ignored", "web", []hubrise.PriceOverride{ov("12", "uber")}, "10", nil},
		{"no variant configured ignores variant rules", "", []hubrise.PriceOverride{ov("12", "uber")}, "10", nil},
		{"a rule without variant applies to all", "", []hubrise.PriceOverride{ov("11")}, "11", nil},
		{"the last matching rule wins", "uber", []hubrise.PriceOverride{ov("12", "uber"), ov("13")}, "13", nil},
		{"a time window is ignored and reported", "", []hubrise.PriceOverride{window}, "10", []domain.WarningCode{domain.WarnPriceWindow}},
		{"a day of week is ignored and reported", "", []hubrise.PriceOverride{window2}, "10", []domain.WarningCode{domain.WarnPriceWindow}},
		{"a service type is ignored and reported", "", []hubrise.PriceOverride{service}, "10", []domain.WarningCode{domain.WarnPriceWindow}},
		{"an unreadable override is ignored", "", []hubrise.PriceOverride{broken}, "10", []domain.WarningCode{domain.WarnInvalidPrice}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sku("S", "10")
			s.PriceOverrides = c.overrides
			got := application.MapCatalog(catalogOf(hubrise.CatalogData{Categories: []hubrise.Category{{Ref: "C", Name: "C"}},
				Products: []hubrise.Product{product("P", "C", "Plat", s)}}), application.MapOptions{VariantRef: c.variant})
			assert.True(t, productByKey(t, got, "S").Price.Equal(dec(c.want)), "price %s", productByKey(t, got, "S").Price)
			assert.Equal(t, c.warns, warningCodes(got))
		})
	}
}

func TestMapRestrictions(t *testing.T) {
	off, on := ptr(false), ptr(true)
	cases := []struct {
		name     string
		variant  string
		r        *hubrise.Restrictions
		disabled bool
		warns    []domain.WarningCode
	}{
		{"none", "", nil, false, nil},
		{"explicitly enabled", "", &hubrise.Restrictions{Enabled: on}, false, nil},
		{"disabled", "", &hubrise.Restrictions{Enabled: off}, true, nil},
		{"only for another variant", "web", &hubrise.Restrictions{VariantRefs: []string{"uber"}}, true, nil},
		{"for our variant", "web", &hubrise.Restrictions{VariantRefs: []string{"web"}}, false, nil},
		{"variant restriction without a configured variant", "", &hubrise.Restrictions{VariantRefs: []string{"uber"}}, false, nil},
		{"a time window is reported", "", &hubrise.Restrictions{StartTime: "11:00"}, false, []domain.WarningCode{domain.WarnRestrictionIgnored}},
		{"a day restriction is reported", "", &hubrise.Restrictions{DOW: "1"}, false, []domain.WarningCode{domain.WarnRestrictionIgnored}},
		{"a per customer limit is reported", "", &hubrise.Restrictions{MaxPerCustomer: ptr(1)}, false, []domain.WarningCode{domain.WarnRestrictionIgnored}},
		{"a minimum amount is reported", "", &hubrise.Restrictions{MinOrderAmount: money("10")}, false, []domain.WarningCode{domain.WarnRestrictionIgnored}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sku("S", "1")
			s.Restrictions = c.r
			got := application.MapCatalog(catalogOf(hubrise.CatalogData{Categories: []hubrise.Category{{Ref: "C", Name: "C"}},
				Products: []hubrise.Product{product("P", "C", "Plat", s)}}), application.MapOptions{VariantRef: c.variant})
			p := productByKey(t, got, "S")
			assert.Equal(t, c.disabled, p.Disabled)
			assert.Equal(t, !c.disabled, p.Visible, "a product off sale is hidden")
			assert.Equal(t, c.warns, warningCodes(got))
		})
	}
}

func TestMapOptionLists(t *testing.T) {
	opt := func(ref, name, price string) hubrise.Option {
		return hubrise.Option{Ref: ref, Name: name, Price: money(price)}
	}
	list := func(ref string, min int, max *int, multiple bool, options ...hubrise.Option) hubrise.OptionList {
		return hubrise.OptionList{ID: "id-" + ref, Ref: ref, Name: "List " + ref, MinSelections: min, MaxSelections: max, MultipleSelection: multiple, Options: options}
	}
	three := []hubrise.Option{opt("A", "A", "0"), opt("B", "B", "0.5"), opt("C", "C", "1")}
	disabled := opt("D", "D", "0")
	disabled.Restrictions = &hubrise.Restrictions{Enabled: ptr(false), StartTime: "10:00"}
	negative := opt("N", "N", "-1")
	overridden := opt("O", "O", "1")
	overridden.PriceOverrides = []hubrise.PriceOverride{{VariantRefs: []string{"web"}, Price: money("2")}}
	brokenPrice := hubrise.Option{Ref: "X", Name: "X", Price: "bad"}

	data := hubrise.CatalogData{
		Categories: []hubrise.Category{{Ref: "C", Name: "C"}},
		OptionLists: []hubrise.OptionList{
			list("ONE", 1, ptr(1), false, three...),
			list("NOLIMIT", 0, nil, false, three...),
			list("ZERO", 0, ptr(0), false, three...),
			list("MULTI", 0, nil, true, three...),
			list("UPPER", 0, ptr(5), true, three...),
			list("TOOHIGHMIN", 4, ptr(2), false, three...),
			list("NEGMIN", -3, ptr(2), false, three...),
			list("EMPTY", 0, nil, false),
			list("MIXED", 0, nil, false, opt("", "no ref", "0"), opt("A", "A", "0"), opt("A", "A again", "0"), disabled, negative, overridden, brokenPrice),
			{ID: "id-", Name: "no ref", Options: three},
		},
		Products: []hubrise.Product{product("P", "C", "Plat", hubrise.Sku{Ref: "S", Price: money("10"),
			OptionListIDs: []string{"id-ONE", "id-NOLIMIT", "id-ZERO", "id-MULTI", "id-UPPER", "id-TOOHIGHMIN", "id-NEGMIN", "id-EMPTY", "id-MIXED", "id-", "id-GONE"}})},
	}
	got := application.MapCatalog(catalogOf(data), application.MapOptions{VariantRef: "web"})
	p := productByKey(t, got, "S")

	byList := map[string]domain.CatalogGroup{}
	for _, g := range p.Groups {
		byList[g.ListRef] = g
	}
	limits := func(ref string) [2]int { return [2]int{byList[ref].Min, byList[ref].Max} }
	assert.Equal(t, [2]int{1, 1}, limits("ONE"))
	assert.Equal(t, [2]int{0, 3}, limits("NOLIMIT"), "no maximum: the number of options")
	assert.Equal(t, [2]int{0, 3}, limits("ZERO"), "a zero maximum means the same")
	assert.Equal(t, [2]int{0, 99}, limits("MULTI"), "the same option may be picked again: a generous maximum")
	assert.Equal(t, [2]int{0, 5}, limits("UPPER"))
	assert.Equal(t, [2]int{2, 2}, limits("TOOHIGHMIN"), "a minimum above the maximum is lowered")
	assert.Equal(t, [2]int{0, 2}, limits("NEGMIN"))
	assert.NotContains(t, byList, "EMPTY")
	assert.Equal(t, "S|ONE", byList["ONE"].Key)
	assert.Equal(t, map[string]string{"fr": "List ONE"}, byList["ONE"].Names)
	assert.Equal(t, 0, byList["ONE"].Sort)
	assert.Equal(t, 8, byList["MIXED"].Sort, "sort is the position among the SKU's lists, skipped ones included")

	mixed := byList["MIXED"].Choices
	require.Len(t, mixed, 5, "no ref, the duplicate and... only usable options remain")
	refs := make([]string, len(mixed))
	for i, c := range mixed {
		refs[i] = c.Ref
	}
	assert.Equal(t, []string{"A", "D", "N", "O", "X"}, refs)
	assert.True(t, mixed[1].Disabled, "restrictions.enabled=false")
	assert.True(t, mixed[2].PriceModifier.IsZero(), "a negative price is raised to zero")
	assert.True(t, mixed[3].PriceModifier.Equal(dec("2")), "the variant's price override applies to options too")
	assert.True(t, mixed[4].PriceModifier.IsZero(), "an unreadable price is zero")
	assert.Equal(t, []int{0, 1, 2, 3, 4}, []int{mixed[0].Sort, mixed[1].Sort, mixed[2].Sort, mixed[3].Sort, mixed[4].Sort})
	assert.Equal(t, "S|MIXED|D", mixed[1].Key)

	assert.ElementsMatch(t, []domain.WarningCode{
		domain.WarnSelectionLimits, domain.WarnEmptyOptionList, domain.WarnMissingRef, domain.WarnDuplicateRef,
		domain.WarnRestrictionIgnored, domain.WarnNegativePrice, domain.WarnInvalidPrice, domain.WarnMissingRef, domain.WarnUnknownOptionList,
	}, warningCodes(got))

	// By ref instead of by id (the upload form) finds the same list.
	data.Products[0].Skus[0].OptionListIDs = nil
	data.Products[0].Skus[0].OptionListRefs = []string{"ONE"}
	byRef := application.MapCatalog(catalogOf(data), application.MapOptions{})
	assert.Len(t, productByKey(t, byRef, "S").Groups, 1)
}

func TestCatalogHashFollowsWhatTheImportWrites(t *testing.T) {
	base := func() hubrise.CatalogData {
		return hubrise.CatalogData{
			Categories: []hubrise.Category{{Ref: "C", Name: "C"}},
			Products:   []hubrise.Product{product("P", "C", "Plat", sku("S", "1"))},
		}
	}
	hash := func(d hubrise.CatalogData, opt application.MapOptions) string {
		return application.MapCatalog(catalogOf(d), opt).Hash()
	}
	ref := hash(base(), application.MapOptions{})
	assert.Equal(t, ref, hash(base(), application.MapOptions{}), "deterministic")

	withDeal := base()
	withDeal.Deals = []hubrise.Deal{{Ref: "D", Name: "Deal"}}
	assert.Equal(t, ref, hash(withDeal, application.MapOptions{}), "data the import ignores does not change the hash")

	pricier := base()
	pricier.Products[0].Skus[0].Price = money("2")
	assert.NotEqual(t, ref, hash(pricier, application.MapOptions{}))
	assert.NotEqual(t, ref, hash(base(), application.MapOptions{Language: "en"}), "the catalog language shapes the menu")
}

func TestContentHashesCarryTheVisibilityTheImportWanted(t *testing.T) {
	visible := domain.CatalogProduct{Key: "S", Visible: true, Price: dec("1")}
	hidden := visible
	hidden.Visible = false

	v, known := domain.HashVisible(visible.ProductHash())
	assert.True(t, v)
	assert.True(t, known)
	v, known = domain.HashVisible(hidden.ProductHash())
	assert.False(t, v)
	assert.True(t, known)
	_, known = domain.HashVisible("")
	assert.False(t, known, "a link made by hand has no opinion")
	_, known = domain.HashVisible("abc")
	assert.False(t, known)

	assert.NotEqual(t, visible.ProductHash(), hidden.ProductHash())
	withGroup := visible
	withGroup.Groups = []domain.CatalogGroup{{Key: "S|L"}}
	assert.Equal(t, visible.ProductHash(), withGroup.ProductHash(), "the option groups have their own hashes")
}
