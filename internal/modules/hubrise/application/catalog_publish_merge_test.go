package application_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
)

var linkSeq int64

func linkOf(e domain.EntityType, id uuid.UUID, key, ref string) domain.CatalogLink {
	linkSeq++
	l := domain.CatalogLink{ID: linkSeq, EntityType: e, HubriseKey: key, HubriseRef: ref, Origin: domain.OriginImport}
	l.SetLocalID(id)
	return l
}

func published(e domain.EntityType, key, ref string) domain.PublishedItem {
	return domain.PublishedItem{EntityType: e, HubriseKey: key, HubriseRef: ref, ContentHash: "h", PublishedAt: time.Now()}
}

func (p planResult) warned(code domain.WarningCode) []string {
	var keys []string
	for _, w := range p.Warnings {
		if w.Code == code {
			keys = append(keys, w.Key)
		}
	}
	return keys
}

// planResult is a plan with the PUT body decoded.
type planResult struct {
	*application.PublishPlan
	data map[string]any
}

func (p planResult) product(ref string) map[string]any {
	for _, pr := range asList(p.data["products"]) {
		if pr["ref"] == ref {
			return pr
		}
	}
	return nil
}

func (p planResult) sku(ref string) map[string]any {
	for _, pr := range asList(p.data["products"]) {
		for _, s := range asList(pr["skus"]) {
			if s["ref"] == ref {
				return s
			}
		}
	}
	return nil
}

func (p planResult) list(ref string) map[string]any {
	for _, l := range asList(p.data["option_lists"]) {
		if l["ref"] == ref {
			return l
		}
	}
	return nil
}

func (p planResult) category(ref string) map[string]any {
	for _, c := range asList(p.data["categories"]) {
		if c["ref"] == ref {
			return c
		}
	}
	return nil
}

func planWith(t *testing.T, in application.PublishInput) planResult {
	t.Helper()
	plan, err := application.PlanPublish(in)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(plan.Data, &data))
	return planResult{plan, data}
}

// Two SKUs of one HubRise product that the shop sees as products of two categories and two VAT
// categories cannot both be written on the one HubRise product: the HubRise values stay, and the
// admin is told.
func TestSkusOfOneHubRiseProductThatDisagreeKeepTheHubRiseValues(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{
			{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")},
			{ID: puid(2), Slug: "b", Order: 2, Names: ptexts("B", "")}},
		Products: []domain.LocalProduct{
			{ID: puid(10), CategoryID: puid(1), Code: "COLA-33", Price: pdec("2.20"), VatCategory: "beverage", Visible: true, Texts: ptexts("Cola", "")},
			{ID: puid(11), CategoryID: puid(2), Code: "COLA-50", Price: pdec("3.30"), VatCategory: "food", Visible: true, Texts: ptexts("Fanta 50cl", "")},
		},
	}
	links := []domain.CatalogLink{
		linkOf(domain.EntityCategory, puid(1), "a", "a"), linkOf(domain.EntityCategory, puid(2), "b", "b"),
		linkOf(domain.EntityProduct, puid(10), "COLA-33", "COLA-33"), linkOf(domain.EntityProduct, puid(11), "COLA-50", "COLA-50"),
	}
	base := `{"categories":[{"ref":"a","name":"A"},{"ref":"b","name":"B"}],
	  "products":[{"ref":"COLA","category_ref":"a","name":"Cola","tags":["hidden","organic"],
	    "tax_rate":{"delivery":"6","collection":"6","eat_in":"12"},
	    "skus":[{"ref":"COLA-33","name":"33cl","price":"2.00 EUR"},{"ref":"COLA-50","name":"50cl","price":"3.00 EUR"},{"ref":"COLA-70","name":"70cl","price":"4.00 EUR"}]}]}`
	p := planWith(t, application.PublishInput{Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})

	assert.Equal(t, []string{"COLA-33", "COLA-33"}, p.warned(domain.WarnCategoryDiverges), "the categories and the VAT categories differ")
	assert.Equal(t, []string{"COLA-50"}, p.warned(domain.WarnNameSplitAmbiguous))
	prod := p.product("COLA")
	assert.Equal(t, "a", prod["category_ref"], "the HubRise category is kept")
	assert.Equal(t, map[string]any{"delivery": "6", "collection": "6", "eat_in": "12"}, prod["tax_rate"], "and its tax rate")
	assert.Equal(t, []any{"organic"}, prod["tags"], "the hidden tag moves to the SKUs, the other tags stay")

	_, hasName := p.sku("COLA-33")["name"]
	assert.False(t, hasName, "a SKU named like its product has no name of its own")
	assert.Equal(t, "Fanta 50cl", p.sku("COLA-50")["name"], "a name that does not start with the product name becomes the SKU name")
	assert.Equal(t, []any{"web:hidden"}, p.sku("COLA-70")["tags"], "the SKU the shop does not own is hidden like its product was")
	assert.Equal(t, "2.20 EUR", p.sku("COLA-33")["price"])
}

// New items made of poor data still publish, with placeholders and a warning each.
func TestItemsWithoutNamesOrVatCategoryGetPlaceholders(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{
			{ID: puid(1), Slug: "nameless", Order: 1},
			{ID: puid(2), Slug: "named", Order: 2, Names: ptexts("Named", "Named EN")}},
		Products: []domain.LocalProduct{
			{ID: puid(10), CategoryID: puid(2), Code: "P1", Price: pdec("2.00"), VatCategory: "", Visible: true},
			{ID: puid(11), CategoryID: puid(2), Code: "P2", Price: pdec("3.00"), VatCategory: "exotic", Visible: false, Texts: ptexts("Exotic", "")},
			{ID: puid(12), CategoryID: puid(99), Code: "LOST", Price: pdec("1.00"), VatCategory: "food", Visible: true, Texts: ptexts("Lost", "")},
		},
		Groups:  []domain.LocalGroup{{ID: puid(20), ProductID: puid(11), Min: 0, Max: 1}},
		Choices: []domain.LocalChoice{{ID: puid(30), ProductID: puid(11), GroupID: puid(20), PriceModifier: pdec("0.50")}},
	}
	p := planWith(t, application.PublishInput{Local: local, Settings: application.PublishSettings{Language: "xx"}, Base: json.RawMessage(`{}`)})

	assert.Empty(t, p.Blockers)
	assert.ElementsMatch(t, []string{"nameless", puid(10).String()}, p.warned(domain.WarnNameMissing))
	assert.Equal(t, []string{"P1", "P2"}, p.warned(domain.WarnVatNotExpressible))
	assert.Equal(t, "nameless", p.category("nameless")["name"], "the slug is the placeholder of a category")
	assert.Equal(t, "Product 00000010", p.product("P1")["name"])
	assert.Nil(t, p.product("LOST"), "a product of a category the snapshot does not have is left out")
	_, hasVat := p.sku("P1")["custom_fields"].(map[string]any)["vat_category"]
	assert.False(t, hasVat, "no VAT category, no custom field")
	_, hasRate := p.product("P2")["tax_rate"]
	assert.False(t, hasRate)
	assert.Equal(t, "Options", p.list(p.warnedRefs()[0])["name"])
	assert.Equal(t, "Option", asList(p.list(p.warnedRefs()[0])["options"])[0]["name"])
	assert.Equal(t, []any{"web:hidden"}, p.sku("P2")["tags"])
	// Only the categories with a name in another language than the catalog language carry the
	// translations of the name.
	assert.Contains(t, p.category("named")["description"], "Named EN")
	assert.Nil(t, p.category("nameless")["description"])
}

func (p planResult) warnedRefs() []string {
	var refs []string
	for _, l := range asList(p.data["option_lists"]) {
		refs = append(refs, l["ref"].(string))
	}
	return refs
}

// A link whose item was retired (the product left the import) is brought back by the publish.
func TestARetiredLinkIsBroughtBackByThePublish(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "X", Price: pdec("2.00"), VatCategory: "food", Visible: true, Texts: ptexts("X", "")}},
	}
	retired := time.Now()
	l := linkOf(domain.EntityProduct, puid(10), "X", "X")
	l.RetiredAt = &retired
	p := planWith(t, application.PublishInput{Local: local, Links: []domain.CatalogLink{l}, Base: json.RawMessage(`{}`)})
	assert.Equal(t, []int64{l.ID}, p.Unretire)
}

// The category descriptions of HubRise are never destroyed to write translations into them.
func TestCategoryDescriptionsAreOnlyExtendedWhenTheyAreOurs(t *testing.T) {
	cat := func(n int, slug string) domain.LocalCategory {
		return domain.LocalCategory{ID: puid(n), Slug: slug, Order: n, Names: ptexts(slug, slug+" en")}
	}
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{cat(1, "empty"), cat(2, "json"), cat(3, "broken"), cat(4, "plain")},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "X", Price: pdec("2.00"), VatCategory: "food", Visible: true, Texts: ptexts("X", "")}},
	}
	var links []domain.CatalogLink
	for n, slug := range []string{"empty", "json", "broken", "plain"} {
		links = append(links, linkOf(domain.EntityCategory, puid(n+1), slug, slug))
	}
	base := `{"categories":[
	  {"ref":"empty","name":"empty"},
	  {"ref":"json","name":"json","description":"{\"translations\":{\"nl\":\"jsonnl\"},\"other\":1}"},
	  {"ref":"broken","name":"broken","description":"{not json"},
	  {"ref":"plain","name":"plain","description":"Our best sushi"}]}`
	p := planWith(t, application.PublishInput{Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})

	var empty, js map[string]any
	require.NoError(t, json.Unmarshal([]byte(p.category("empty")["description"].(string)), &empty))
	assert.Equal(t, map[string]any{"translations": map[string]any{"fr": "empty", "en": "empty en"}}, empty)
	require.NoError(t, json.Unmarshal([]byte(p.category("json")["description"].(string)), &js))
	assert.Equal(t, "jsonnl", js["translations"].(map[string]any)["nl"], "the translations of other languages stay")
	assert.Equal(t, "json en", js["translations"].(map[string]any)["en"])
	assert.EqualValues(t, 1, js["other"], "and so do the other keys")
	assert.Equal(t, "{not json", p.category("broken")["description"])
	assert.Equal(t, "Our best sushi", p.category("plain")["description"])
	assert.Equal(t, []string{"broken", "plain"}, p.warned(domain.WarnCategoryDescriptionKept))
}

// Categories and option lists the shop published and no longer has leave the catalog, unless
// somebody else still needs them.
func TestWhatTheShopStopsPublishingLeavesUnlessSomebodyUsesIt(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "keep", Order: 1, Names: ptexts("Keep", "")}},
		Products: []domain.LocalProduct{
			{ID: puid(10), CategoryID: puid(1), Code: "K1", Price: pdec("2.00"), VatCategory: "food", Visible: true, Texts: ptexts("K1", "")}},
	}
	pub := []domain.PublishedItem{
		published(domain.EntityCategory, "gone", "gone"),
		published(domain.EntityCategory, "used", "used"),
		published(domain.EntityCategory, "parent", "parent"),
		published(domain.EntityCategory, "moved", "keep"), // the same ref as a category the shop still has
		published(domain.EntityChoiceGroup, "K1|oldlist", "oldlist"),
		published(domain.EntityChoiceGroup, "K1|usedlist", "usedlist"),
		published(domain.EntityChoice, "K1|oldlist|o1", "o1"),
		published(domain.EntityChoice, "malformed", "x"),
	}
	base := `{"categories":[{"ref":"keep","name":"Keep"},{"ref":"gone","name":"Gone"},{"ref":"used","name":"Used"},
	    {"ref":"parent","name":"Parent"},{"ref":"child","name":"Child","parent_ref":"parent"}],
	  "option_lists":[
	    {"ref":"oldlist","name":"Old","options":[{"ref":"o1","name":"One","price":"0.00 EUR"}]},
	    {"ref":"usedlist","name":"Used","options":[{"ref":"o2","name":"Two","price":"0.00 EUR"}]}],
	  "products":[{"ref":"EPOS","category_ref":"used","name":"Epos","skus":[{"ref":"EPOS","price":"1.00 EUR","option_list_refs":["usedlist"]}]}],
	  "deals":[{"ref":"d","name":"Deal","category_ref":"gone","lines":[]}]}`
	links := []domain.CatalogLink{linkOf(domain.EntityCategory, puid(1), "keep", "keep"), linkOf(domain.EntityProduct, puid(10), "K1", "K1")}
	p := planWith(t, application.PublishInput{Local: local, Links: links, Published: pub, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})

	assert.Nil(t, p.list("oldlist"), "a list nothing uses is dropped")
	assert.NotNil(t, p.list("usedlist"), "a list a foreign SKU uses stays")
	assert.NotNil(t, p.category("used"), "a category a foreign product uses stays")
	assert.NotNil(t, p.category("gone"), "a category a deal uses stays")
	assert.NotNil(t, p.category("parent"), "a parent category stays")
	assert.Equal(t, []string{"gone", "parent", "used"}, p.warned(domain.WarnCategoryInUse))
	assert.NotNil(t, p.category("keep"))
}

// Groups of different products that shared one HubRise option list and no longer agree are split;
// the subset that still equals the HubRise list keeps its ref.
func TestDivergedGroupsOfASharedOptionListAreSplit(t *testing.T) {
	product := func(n int, code string) domain.LocalProduct {
		return domain.LocalProduct{ID: puid(n), CategoryID: puid(1), Code: code, Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts(code, "")}
	}
	group := func(n, product int, name string) domain.LocalGroup {
		return domain.LocalGroup{ID: puid(n), ProductID: puid(product), Min: 0, Max: 1, Names: ptexts(name, "")}
	}
	choice := func(n, group, product int) domain.LocalChoice {
		return domain.LocalChoice{ID: puid(n), ProductID: puid(product), GroupID: puid(group), PriceModifier: pdec("0.50"), Names: ptexts("Soja", "")}
	}
	build := func(products []domain.LocalProduct, groups []domain.LocalGroup, choices []domain.LocalChoice, base string) planResult {
		local := &domain.LocalSnapshot{
			Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
			Products:   products, Groups: groups, Choices: choices,
		}
		links := []domain.CatalogLink{}
		for _, g := range groups {
			prod := ""
			for _, p := range products {
				if p.ID == g.ProductID {
					prod = p.Code
				}
			}
			links = append(links, linkOf(domain.EntityChoiceGroup, g.ID, prod+"|sauces", "sauces"))
		}
		for _, c := range choices {
			prod := ""
			for _, g := range groups {
				if g.ID == c.GroupID {
					for _, p := range products {
						if p.ID == g.ProductID {
							prod = p.Code
						}
					}
				}
			}
			links = append(links, linkOf(domain.EntityChoice, c.ID, prod+"|sauces|soja", "soja"))
		}
		return planWith(t, application.PublishInput{Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
	}
	listBase := func(name, price string) string {
		return `{"option_lists":[{"ref":"sauces","name":"` + name + `","min_selections":0,"max_selections":1,"options":[{"ref":"soja","name":"Soja","price":"` + price + `"}]}]}`
	}
	three := func(base string, names ...string) planResult {
		return build([]domain.LocalProduct{product(10, "A"), product(11, "B"), product(12, "C")},
			[]domain.LocalGroup{group(20, 10, names[0]), group(21, 11, names[1]), group(22, 12, names[2])},
			[]domain.LocalChoice{choice(30, 20, 10), choice(31, 21, 11), choice(32, 22, 12)}, base)
	}

	t.Run("the subset that equals the HubRise list keeps its ref", func(t *testing.T) {
		p := build([]domain.LocalProduct{product(10, "A"), product(11, "B"), product(12, "C")},
			[]domain.LocalGroup{group(20, 10, "Sauce"), group(21, 11, "Sauce"), group(22, 12, "Other")},
			[]domain.LocalChoice{choice(30, 20, 10), choice(31, 21, 11), choice(32, 22, 12)}, listBase("Sauce", "0.50 EUR"))
		assert.Equal(t, []string{"sauces~C"}, p.warned(domain.WarnListSplit))
		assert.Equal(t, []any{"sauces"}, p.sku("A")["option_list_refs"])
		assert.Equal(t, []any{"sauces"}, p.sku("B")["option_list_refs"])
		assert.Equal(t, []any{"sauces~C"}, p.sku("C")["option_list_refs"])
		assert.Equal(t, "Other", p.list("sauces~C")["name"])
	})
	t.Run("without a matching HubRise list the largest subset keeps it", func(t *testing.T) {
		p := build([]domain.LocalProduct{product(10, "A"), product(11, "B"), product(12, "C")},
			[]domain.LocalGroup{group(20, 10, "Other"), group(21, 11, "Sauce"), group(22, 12, "Sauce")},
			[]domain.LocalChoice{choice(30, 20, 10), choice(31, 21, 11), choice(32, 22, 12)}, listBase("Old name", "oops"))
		assert.Equal(t, []string{"sauces~A"}, p.warned(domain.WarnListSplit))
		assert.Equal(t, []any{"sauces"}, p.sku("B")["option_list_refs"])
		assert.Equal(t, []any{"sauces"}, p.sku("C")["option_list_refs"])
	})
	t.Run("a HubRise list that allows repeats without a maximum, or has a fractional one, matches nothing", func(t *testing.T) {
		for _, base := range []string{
			`{"option_lists":[{"ref":"sauces","name":"Sauce","multiple_selection":true,"options":[{"ref":"soja","name":"Soja","price":"0.50 EUR"}]}]}`,
			`{"option_lists":[{"ref":"sauces","name":"Sauce","max_selections":2.5,"options":[{"ref":"soja","name":"Soja","price":"0.50 EUR"}]}]}`,
		} {
			p := three(base, "Sauce", "Sauce", "Other")
			assert.Equal(t, []string{"sauces~C"}, p.warned(domain.WarnListSplit), "the largest subset keeps the ref")
		}
	})
	t.Run("a split list whose name is taken gets a number", func(t *testing.T) {
		base := `{"option_lists":[{"ref":"sauces","name":"Sauce","min_selections":0,"max_selections":1,"options":[{"ref":"soja","name":"Soja","price":"0.50 EUR"}]},
		  {"ref":"sauces~C","name":"Taken","options":[{"ref":"x","name":"x","price":"0.00 EUR"}]}]}`
		p := three(base, "Sauce", "Sauce", "Other")
		assert.Equal(t, []string{"sauces~C~2"}, p.warned(domain.WarnListSplit))
		assert.NotNil(t, p.list("sauces~C"), "the list of somebody else stays")
	})
	t.Run("two subsets of the same size: the one with the smallest SKU wins, whatever its order", func(t *testing.T) {
		p := build([]domain.LocalProduct{product(10, "Z"), product(11, "Y"), product(12, "B"), product(13, "A")},
			[]domain.LocalGroup{group(20, 10, "Sauce"), group(21, 11, "Sauce"), group(22, 12, "Other"), group(23, 13, "Other")},
			[]domain.LocalChoice{choice(30, 20, 10), choice(31, 21, 11), choice(32, 22, 12), choice(33, 23, 13)}, listBase("Old name", "9.99 EUR"))
		assert.Equal(t, []string{"sauces~Z"}, p.warned(domain.WarnListSplit))
		assert.Equal(t, []any{"sauces"}, p.sku("A")["option_list_refs"])
		assert.Equal(t, []any{"sauces"}, p.sku("B")["option_list_refs"])
	})
	t.Run("a tie goes to the smallest SKU ref", func(t *testing.T) {
		p := build([]domain.LocalProduct{product(10, "Z"), product(11, "B")},
			[]domain.LocalGroup{group(20, 10, "Sauce"), group(21, 11, "Other")},
			[]domain.LocalChoice{choice(30, 20, 10), choice(31, 21, 11)}, listBase("Old name", "0.50 EUR"))
		assert.Equal(t, []string{"sauces~Z"}, p.warned(domain.WarnListSplit))
		assert.Equal(t, []any{"sauces"}, p.sku("B")["option_list_refs"])
	})
}

// A list with a maximum that HubRise reads as repeated picks is written back as such.
func TestOptionListLimitsAreWrittenTheWayHubRiseReadsThem(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products: []domain.LocalProduct{
			{ID: puid(10), CategoryID: puid(1), Code: "A", Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts("A", "")}},
		Groups: []domain.LocalGroup{
			{ID: puid(20), ProductID: puid(10), Min: 0, Max: 2, Names: ptexts("Pick", "")},
			{ID: puid(21), ProductID: puid(10), Min: 0, Max: 5, Names: ptexts("Many", "")},
			{ID: puid(22), ProductID: puid(10), Min: 0, Max: 1, Names: ptexts("One", "")}},
		Choices: []domain.LocalChoice{
			{ID: puid(30), ProductID: puid(10), GroupID: puid(20), Names: ptexts("a", ""), Sort: 1},
			{ID: puid(31), ProductID: puid(10), GroupID: puid(20), Names: ptexts("b", ""), Sort: 2},
			{ID: puid(32), ProductID: puid(10), GroupID: puid(20), Names: ptexts("c", ""), Sort: 3},
			{ID: puid(33), ProductID: puid(10), GroupID: puid(21), Names: ptexts("d", ""), Sort: 1},
			{ID: puid(34), ProductID: puid(10), GroupID: puid(22), Names: ptexts("e", ""), Sort: 1}},
	}
	links := []domain.CatalogLink{
		linkOf(domain.EntityChoiceGroup, puid(20), "A|pick", "pick"),
		linkOf(domain.EntityChoiceGroup, puid(21), "A|many", "many"),
	}
	base := `{"option_lists":[
	  {"ref":"pick","name":"Pick","multiple_selection":true,"max_selections":2,"options":[{"ref":"x","name":"x","price":"0.00 EUR"}]},
	  {"ref":"many","name":"Many","max_selections":2.5,"options":[{"ref":"y","name":"y","price":"0.00 EUR"}]}]}`
	p := planWith(t, application.PublishInput{Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
	assert.Empty(t, p.Blockers)

	pick, many := p.list("pick"), p.list("many")
	assert.Equal(t, true, pick["multiple_selection"], "a maximum of 2 over 3 options and a HubRise list that allowed repeats stays that way")
	assert.EqualValues(t, 2, pick["max_selections"])
	assert.Equal(t, true, many["multiple_selection"], "more picks than options can only be repeated picks")
	assert.EqualValues(t, 5, many["max_selections"])
	var one map[string]any
	for _, l := range asList(p.data["option_lists"]) {
		if l["name"] == "One" {
			one = l
		}
	}
	require.NotNil(t, one)
	assert.Equal(t, false, one["multiple_selection"])
}

func TestTheRemoteHashIgnoresTheSpellingsOfTheDocumentation(t *testing.T) {
	a := application.RemoteCatalogHash(json.RawMessage(`{"options_lists":[{"id":"1","ref":"l","product_ids":["p"],"image_ids":["i"]}],"categories":[{"ref":"c","images_ids":["i"]}]}`))
	b := application.RemoteCatalogHash(json.RawMessage(`{"option_lists":[{"id":"2","ref":"l","image_ids":["i"]}],"categories":[{"ref":"c","image_ids":["i"]}]}`))
	assert.Equal(t, a, b)
}

func TestTheLocalHashIgnoresTextsWithoutContent(t *testing.T) {
	cat := func(texts map[string]domain.LocalText) *domain.LocalSnapshot {
		return &domain.LocalSnapshot{Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: texts}}}
	}
	s := application.PublishSettings{Language: "fr"}
	assert.Equal(t,
		application.LocalMenuHash(cat(map[string]domain.LocalText{"fr": {Name: "A"}}), s),
		application.LocalMenuHash(cat(map[string]domain.LocalText{"fr": {Name: "A"}, "en": {Name: "  "}}), s))
}

// A list ref for a new group is the short id of the group, longer when the short one is taken, and
// numbered when even the whole id is taken.
func TestNewListRefsAvoidEveryRefThatExists(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "A", Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts("A", "")}},
		Groups:     []domain.LocalGroup{{ID: puid(20), ProductID: puid(10), Min: 0, Max: 1, Names: ptexts("Sauce", "")}},
		Choices:    []domain.LocalChoice{{ID: puid(30), ProductID: puid(10), GroupID: puid(20), Names: ptexts("Soja", "")}},
	}
	list := func(ref string) string {
		return `{"ref":"` + ref + `","name":"` + ref + `","options":[{"ref":"o","name":"o","price":"0.00 EUR"}]}`
	}
	for taken, want := range map[string]string{
		"":                          "g-00000020",
		"g-00000020":                "g-000000200000",
		"g-00000020,g-000000200000": "g-00000020000000000000000000000000",
		"g-00000020,g-000000200000,g-00000020000000000000000000000000": "g-00000020000000000000000000000000-2",
	} {
		base := `{"option_lists":[`
		first := true
		for _, ref := range splitComma(taken) {
			if !first {
				base += ","
			}
			base += list(ref)
			first = false
		}
		base += `]}`
		p := planWith(t, application.PublishInput{Local: local, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
		assert.Equal(t, []any{want}, p.sku("A")["option_list_refs"], "taken: %q", taken)
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// What a foreign option list says about the options of an owned SKU never stops the plan: the
// prices that cannot be read are reported as blockers.
func TestAnOwnedSkuOverAForeignOptionListWithUnreadablePrices(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "K1", Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts("K1", "")}},
	}
	links := []domain.CatalogLink{linkOf(domain.EntityCategory, puid(1), "a", "a"), linkOf(domain.EntityProduct, puid(10), "K1", "K1")}
	base := `{"categories":[{"ref":"a","name":"A"}],
	  "option_lists":[{"ref":"L","name":"L","options":[{"ref":"o1","name":"free"},{"ref":"o2","name":"odd","price":"bad"}]}],
	  "products":[{"ref":"K1","category_ref":"a","name":"K1","skus":[{"ref":"K1","price":"1.00 EUR","option_list_refs":["L"]}]}]}`
	p := planWith(t, application.PublishInput{Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
	assert.Equal(t, []string{"L|o1", "L|o2"}, blockerKeys(p.PublishPlan)[domain.BlockInvalidMoney], "a missing price and an unreadable one")
}

// A SKU the shop removed but a deal of HubRise sells stays, disabled, with the restrictions it had.
func TestADisabledSkuKeepsItsRestrictionsAndAnAbsentOneGetsSome(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "K1", Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts("K1", "")}},
	}
	links := []domain.CatalogLink{linkOf(domain.EntityCategory, puid(1), "a", "a"), linkOf(domain.EntityProduct, puid(10), "K1", "K1")}
	pub := []domain.PublishedItem{published(domain.EntityProduct, "OLD1", "OLD1"), published(domain.EntityProduct, "OLD2", "OLD2")}
	base := `{"categories":[{"ref":"a","name":"A"}],
	  "products":[
	    {"ref":"K1","category_ref":"a","name":"K1","skus":[{"ref":"K1","price":"1.00 EUR","price_overrides":[
	       {"price":"2.00 EUR","dow":1},{"price":"3.00 EUR","start_time":"12:00"},{"price":"4.00 EUR","variant_refs":["other"]}]}]},
	    {"ref":"OLD1","category_ref":"a","name":"Old one","skus":[{"ref":"OLD1","price":"1.00 EUR"}]},
	    {"ref":"OLD2","category_ref":"a","name":"Old two","skus":[{"ref":"OLD2","price":"1.00 EUR","restrictions":{"enabled":true,"channels":["web"]}}]}],
	  "deals":[{"ref":"d","name":"Deal","lines":[{"label":"l","pricing_effect":"fixed_price","pricing_value":"1.00 EUR","skus":[{"ref":"OLD1"},{"ref":"OLD2"}]}]}]}`
	p := planWith(t, application.PublishInput{Local: local, Links: links, Published: pub, Settings: application.PublishSettings{Language: "fr", VariantRef: "web"}, Base: json.RawMessage(base)})

	assert.ElementsMatch(t, []string{"OLD1", "OLD2"}, p.warned(domain.WarnRetainedByDeal))
	assert.Equal(t, map[string]any{"enabled": false}, p.sku("OLD1")["restrictions"])
	assert.Equal(t, map[string]any{"enabled": false, "channels": []any{"web"}}, p.sku("OLD2")["restrictions"])
	// None of the time, date or other variant rules is the price the shop edits.
	assert.Equal(t, "5.00 EUR", p.sku("K1")["price"])
	assert.Len(t, asList(p.sku("K1")["price_overrides"]), 3)
	assert.Equal(t, "2.00 EUR", asList(p.sku("K1")["price_overrides"])[0]["price"])
}

// The languages of a product with a blank name are not written.
func TestBlankTranslationsAreNotPublished(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products: []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "K1", Price: pdec("5.00"), VatCategory: "food", Visible: true,
			Texts: map[string]domain.LocalText{"fr": {Name: "K1", Description: "Good"}, "en": {Name: "  "}, "nl": {Name: "K1 nl"}}}},
	}
	p := planWith(t, application.PublishInput{Local: local, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(`{}`)})
	tr := p.sku("K1")["custom_fields"].(map[string]any)["translations"].(map[string]any)
	assert.Equal(t, map[string]any{"name": "K1", "description": "Good"}, tr["fr"])
	assert.Equal(t, map[string]any{"name": "K1 nl"}, tr["nl"])
	assert.NotContains(t, tr, "en")
}

// With the removal of foreign items asked for, a foreign option list nobody uses goes too.
func TestRemovingForeignItemsAlsoRemovesTheirUnusedOptionLists(t *testing.T) {
	local := &domain.LocalSnapshot{
		Categories: []domain.LocalCategory{{ID: puid(1), Slug: "a", Order: 1, Names: ptexts("A", "")}},
		Products:   []domain.LocalProduct{{ID: puid(10), CategoryID: puid(1), Code: "K1", Price: pdec("5.00"), VatCategory: "food", Visible: true, Texts: ptexts("K1", "")}},
	}
	base := `{"categories":[{"ref":"a","name":"A"},{"ref":"foreigncat","name":"F"}],
	  "option_lists":[{"ref":"ghostlist","name":"G","options":[{"ref":"o","name":"o","price":"0.00 EUR"}]}],
	  "products":[{"ref":"EPOS","category_ref":"foreigncat","name":"Epos","skus":[{"ref":"EPOS","price":"1.00 EUR","option_list_refs":["ghostlist"]}]}]}`
	keep := planWith(t, application.PublishInput{Local: local, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
	assert.NotNil(t, keep.list("ghostlist"))
	assert.NotNil(t, keep.sku("EPOS"))

	gone := planWith(t, application.PublishInput{Local: local, RemoveForeign: true, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base)})
	assert.Nil(t, gone.list("ghostlist"))
	assert.Nil(t, gone.sku("EPOS"))
	assert.Nil(t, gone.category("foreigncat"))
}
