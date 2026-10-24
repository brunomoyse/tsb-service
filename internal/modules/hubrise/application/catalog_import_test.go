package application_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// sushiMenu is a small HubRise menu: two categories, two SKUs of one product, a SKU with an option
// list (a required sauce and optional extras), a drink.
func sushiMenu() *menu {
	one := 1
	return newMenu().
		category("MAKI", "Makis").category("DRINK", "Boissons").
		list("SAUCE", "Sauce", 1, &one, option("SOJA", "Soja", "0"), option("SUCREE", "Soja sucrée", "0.50")).
		list("EXTRA", "Suppléments", 0, nil, option("AVOCAT", "Avocat", "1"), option("EGG", "Oeuf", "0.80")).
		product("SAUMON", "MAKI", "Maki saumon", codeSku("SAUMON-6", "5.50", "M1", "SAUCE", "EXTRA"), hubrise.Sku{Ref: "SAUMON-12", Name: "12 pièces", Price: money("10")}).
		product("COLA", "DRINK", "Cola", sku("COLA-33", "3"))
}

func TestFullImportCreatesTheMenu(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())

	res := e.fullImport()

	// Categories, products, groups and choices exist with links, and nothing is hidden.
	assert.Equal(t, 2, res.Result.Count(domain.EntityCategory, domain.ActionCreate))
	assert.Equal(t, 3, res.Result.Count(domain.EntityProduct, domain.ActionCreate))
	assert.Equal(t, 2, res.Result.Count(domain.EntityChoiceGroup, domain.ActionCreate))
	assert.Equal(t, 4, res.Result.Count(domain.EntityChoice, domain.ActionCreate))
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM product_categories`))
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`))
	assert.Equal(t, 11, e.count(`SELECT count(*) FROM hubrise_catalog_links WHERE retired_at IS NULL`))

	six := e.product("SAUMON-6")
	assert.Equal(t, "5.50", six.Price)
	assert.Equal(t, "M1", *six.Code)
	assert.Equal(t, "food", six.VatCategory)
	assert.True(t, six.Visible)
	assert.True(t, six.Available)
	assert.True(t, six.Discount)
	assert.Equal(t, "makis-maki-saumon-12-pieces", e.product("SAUMON-12").Slug, "category name plus product name, accents folded")
	assert.Equal(t, "makis-maki-saumon", six.Slug)
	assert.Nil(t, e.product("SAUMON-12").Code, "no code in HubRise, none locally")

	texts := e.productTexts(six.ID)
	assert.Equal(t, textRow{Language: "fr", Name: "Maki saumon", Source: "hubrise"}, texts["fr"])
	assert.Equal(t, "Maki saumon 12 pièces", e.productTexts(e.product("SAUMON-12").ID)["fr"].Name)

	// The option lists became groups and choices of the SKU that has them, in HubRise's order.
	var groups []struct {
		Min, Max, Sort int
		Name, Source   string
	}
	require.NoError(t, e.db.SelectContext(t.Context(), &groups, `
		SELECT g.min_selections AS min, g.max_selections AS max, g.sort_order AS sort, t.name, t.source
		FROM product_choice_groups g JOIN product_choice_group_translations t ON t.product_choice_group_id = g.id
		WHERE g.product_id = $1 ORDER BY g.sort_order`, six.ID))
	require.Len(t, groups, 2)
	assert.Equal(t, "Sauce", groups[0].Name)
	assert.Equal(t, "hubrise", groups[0].Source)
	assert.Equal(t, [3]int{1, 1, 0}, [3]int{groups[0].Min, groups[0].Max, groups[0].Sort})
	assert.Equal(t, [3]int{0, 2, 1}, [3]int{groups[1].Min, groups[1].Max, groups[1].Sort}, "no maximum: two options")

	var choices []struct {
		Name, Modifier string
		Available      bool
	}
	require.NoError(t, e.db.SelectContext(t.Context(), &choices, `
		SELECT t.name, c.price_modifier::text AS modifier, c.is_available AS available
		FROM product_choices c JOIN product_choice_translations t ON t.product_choice_id = c.id
		WHERE c.product_id = $1 ORDER BY c.choice_group_id, c.sort_order`, six.ID))
	require.Len(t, choices, 4)
	names := []string{choices[0].Name, choices[1].Name, choices[2].Name, choices[3].Name}
	assert.ElementsMatch(t, []string{"Soja", "Soja sucrée", "Avocat", "Oeuf"}, names)
	for _, c := range choices {
		assert.True(t, c.Available)
	}

	// The links carry the HubRise ids, refs and hashes; the keys are the documented ones.
	link, err := e.links.FindByKey(t.Context(), domain.EntityChoice, "SAUMON-6|SAUCE|SUCREE")
	require.NoError(t, err)
	assert.Equal(t, "SUCREE", link.HubriseRef)
	assert.Equal(t, domain.OriginImport, link.Origin)
	assert.NotNil(t, link.HubriseID)
	assert.NotEmpty(t, link.ContentHash)
	group, err := e.links.FindByKey(t.Context(), domain.EntityChoiceGroup, "SAUMON-6|SAUCE")
	require.NoError(t, err)
	assert.Equal(t, "SAUCE", group.HubriseRef)

	// The dashboards were told, the catalog hash recorded, no error left.
	assert.ElementsMatch(t, []uuid.UUID{six.ID, e.product("SAUMON-12").ID, e.product("COLA-33").ID}, e.notes.all())
	conn := e.connection()
	require.NotNil(t, conn.CatalogHash)
	assert.Equal(t, res.Diff.CatalogHash, *conn.CatalogHash)
	assert.NotNil(t, conn.LastCatalogSyncAt)
	assert.Nil(t, conn.LastError)
}

func TestSecondImportOfTheSameCatalogWritesNothing(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	first := e.fullImport()
	e.notes.reset()
	var updatedAt string
	require.NoError(t, e.db.GetContext(t.Context(), &updatedAt, `SELECT max(updated_at)::text FROM hubrise_catalog_links`))

	e.clock.Advance(time.Hour)
	second := e.fullImport()

	assert.Empty(t, second.Diff.Changes)
	assert.Equal(t, first.Diff.CatalogHash, second.Diff.CatalogHash)
	assert.Positive(t, second.Diff.Unchanged)
	assert.Empty(t, e.notes.all(), "nothing changed, nobody is told")
	var after string
	require.NoError(t, e.db.GetContext(t.Context(), &after, `SELECT max(updated_at)::text FROM hubrise_catalog_links`))
	assert.Equal(t, updatedAt, after, "unchanged entries cost no write")
}

func TestImportFollowsHubRiseChanges(t *testing.T) {
	e := newCatEnv(t)
	m := sushiMenu()
	e.publish(m)
	e.fullImport()
	slug := e.product("SAUMON-6").Slug
	coke := e.product("COLA-33")

	// HubRise: a new price, a rename, a code, an extra option, a category change.
	m2 := sushiMenu()
	m2.d.Products[0].Name = "Maki au saumon"
	m2.d.Products[0].Skus[0].Price = money("6.20")
	m2.d.Products[0].Skus[0].CustomFields = []byte(`{"code":"M9"}`)
	m2.d.Products[1].CategoryRef = "MAKI"
	m2.d.OptionLists[1].Options = append(m2.d.OptionLists[1].Options, option("CHEESE", "Fromage", "0.60"))
	m2.d.OptionLists[0].Options[1].Price = money("0.70")
	e.publish(m2)
	e.notes.reset()

	res := e.fullImport()

	six := e.product("SAUMON-6")
	assert.Equal(t, "6.20", six.Price)
	assert.Equal(t, "M9", *six.Code)
	assert.Equal(t, slug, six.Slug, "the slug is generated once and never regenerated")
	assert.Equal(t, "Maki au saumon", e.productTexts(six.ID)["fr"].Name)
	assert.Equal(t, e.product("SAUMON-6").CategoryID, e.product("COLA-33").CategoryID, "the drink moved to the makis")
	assert.Equal(t, coke.ID, e.product("COLA-33").ID, "same row, not a new product")
	assert.Equal(t, 1, res.Result.Count(domain.EntityChoice, domain.ActionCreate))
	assert.Equal(t, "0.70", func() string {
		var s string
		require.NoError(t, e.db.GetContext(t.Context(), &s, `
			SELECT c.price_modifier::text FROM product_choices c JOIN hubrise_catalog_links l ON l.choice_id = c.id
			WHERE l.hubrise_key = 'SAUMON-6|SAUCE|SUCREE'`))
		return s
	}())
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`), "no duplicate")
	assert.Contains(t, e.notes.all(), six.ID)
	assert.Contains(t, e.notes.all(), coke.ID)

	for _, c := range res.Diff.Changes {
		if c.Entity == domain.EntityProduct && c.Key == "SAUMON-6" {
			assert.ElementsMatch(t, []string{domain.FieldPrice, domain.FieldCode, domain.FieldNames}, c.Fields)
		}
	}
}

func TestManualTranslationsSurviveEveryImport(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	cola := e.product("COLA-33")

	// The shop overrides the French name, adds Dutch and Chinese names, and the English description.
	e.exec(`UPDATE product_translations SET name = 'Coca maison', source = 'manual' WHERE product_id = $1 AND language = 'fr'`, cola.ID)
	e.exec(`INSERT INTO product_translations (product_id, language, name, description) VALUES ($1, 'nl', 'Cola NL', 'frisdrank'), ($1, 'zh', '可乐', NULL)`, cola.ID)

	for round := 1; round <= 3; round++ {
		m := sushiMenu()
		m.d.Products[1].Name = "Cola HubRise v" + string(rune('0'+round))
		m.d.Products[1].Description = "desc " + string(rune('0'+round))
		e.publish(m)
		e.fullImport()

		texts := e.productTexts(cola.ID)
		assert.Equal(t, "Coca maison", texts["fr"].Name, "round %d: a manual override is never overwritten", round)
		assert.Equal(t, "manual", texts["fr"].Source)
		assert.Equal(t, "Cola NL", texts["nl"].Name, "round %d: a language HubRise does not carry is untouched", round)
		assert.Equal(t, "manual", texts["nl"].Source)
		assert.Equal(t, "可乐", texts["zh"].Name)
	}

	// A translation the import wrote is refreshed.
	e.exec(`DELETE FROM product_translations WHERE product_id = $1 AND language = 'fr'`, cola.ID)
	m := sushiMenu()
	m.d.Products[1].Name = "Cola frais"
	e.publish(m)
	e.fullImport()
	assert.Equal(t, textRow{Language: "fr", Name: "Cola frais", Source: "hubrise"}, e.productTexts(cola.ID)["fr"],
		"a missing translation is written, as an import one")
	m.d.Products[1].Name = "Cola glacé"
	e.publish(m)
	e.fullImport()
	assert.Equal(t, "Cola glacé", e.productTexts(cola.ID)["fr"].Name, "an import translation follows HubRise")
}

func TestImportReadsTranslationsFromTheCustomFields(t *testing.T) {
	e := newCatEnv(t)
	s := sku("COLA-33", "3")
	s.CustomFields = []byte(`{"translations":{"en":{"name":"Cola","description":"A soda"},"zh":{"name":"可乐"}}}`)
	e.publish(newMenu().category("DRINK", `Boissons`).product("COLA", "DRINK", "Cola", s))
	e.fullImport()

	texts := e.productTexts(e.product("COLA-33").ID)
	assert.Equal(t, textRow{Language: "en", Name: "Cola", Description: strPtr("A soda"), Source: "hubrise"}, texts["en"])
	assert.Equal(t, "可乐", texts["zh"].Name)
	assert.Equal(t, "Cola", texts["fr"].Name)
	assert.Nil(t, texts["fr"].Description)
}

func strPtr(s string) *string { return &s }

func TestImportRetiresAndRevives(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	six, twelve, cola := e.product("SAUMON-6"), e.product("SAUMON-12"), e.product("COLA-33")
	rowsBefore := e.count(`SELECT count(*) FROM products`)

	// HubRise drops the 12 piece SKU, the whole drink category and the extras list of the 6 piece SKU.
	m := newMenu().category("MAKI", "Makis").
		list("SAUCE", "Sauce", 1, ptr(1), option("SOJA", "Soja", "0"), option("SUCREE", "Soja sucrée", "0.50")).
		product("SAUMON", "MAKI", "Maki saumon", codeSku("SAUMON-6", "5.50", "M1", "SAUCE"))
	e.publish(m)
	e.notes.reset()
	res := e.fullImport()

	assert.Equal(t, 2, res.Result.Count(domain.EntityProduct, domain.ActionRetire))
	assert.Equal(t, 1, res.Result.Count(domain.EntityChoiceGroup, domain.ActionRetire))
	assert.Equal(t, 2, res.Result.Count(domain.EntityChoice, domain.ActionRetire))
	assert.Equal(t, 1, res.Result.Count(domain.EntityCategory, domain.ActionRetire))
	assert.Equal(t, rowsBefore, e.count(`SELECT count(*) FROM products`), "nothing is ever deleted")

	for _, p := range []productRow{e.productByID(twelve.ID), e.productByID(cola.ID)} {
		assert.False(t, p.Visible)
		assert.False(t, p.Available)
	}
	assert.True(t, e.productByID(six.ID).Visible)
	retired, err := e.links.FindByKey(t.Context(), domain.EntityProduct, "SAUMON-12")
	require.NoError(t, err)
	assert.NotNil(t, retired.RetiredAt)
	assert.Equal(t, e.clock.Now().Unix(), retired.RetiredAt.Unix())

	// The extras list: its group stops requiring a selection, its choices cannot be selected.
	var min int
	require.NoError(t, e.db.GetContext(t.Context(), &min, `
		SELECT g.min_selections FROM product_choice_groups g JOIN hubrise_catalog_links l ON l.choice_group_id = g.id WHERE l.hubrise_key = 'SAUMON-6|EXTRA'`))
	assert.Zero(t, min)
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM product_choices c JOIN hubrise_catalog_links l ON l.choice_id = c.id
		WHERE l.hubrise_key LIKE 'SAUMON-6|EXTRA|%' AND NOT c.is_available`))
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM product_choices WHERE product_id = $1 AND is_available`, six.ID), "the sauces stay selectable")

	// A later import does not retire again, and nothing else changes.
	again := e.fullImport()
	assert.Empty(t, again.Diff.Changes)

	// HubRise brings them back: the same rows are revived, visible and available again.
	e.publish(sushiMenu())
	revived := e.fullImport()
	assert.Equal(t, 2, revived.Result.Count(domain.EntityProduct, domain.ActionRevive))
	assert.Equal(t, rowsBefore, e.count(`SELECT count(*) FROM products`))
	for _, id := range []uuid.UUID{twelve.ID, cola.ID} {
		p := e.productByID(id)
		assert.True(t, p.Visible)
		assert.True(t, p.Available)
	}
	link, err := e.links.FindByKey(t.Context(), domain.EntityProduct, "SAUMON-12")
	require.NoError(t, err)
	assert.Nil(t, link.RetiredAt)
	assert.Equal(t, twelve.ID, link.LocalID())
	assert.Equal(t, 4, e.count(`SELECT count(*) FROM product_choices WHERE product_id = $1 AND is_available`, six.ID), "revived choices are selectable")
	require.NoError(t, e.db.GetContext(t.Context(), &min, `
		SELECT g.min_selections FROM product_choice_groups g JOIN hubrise_catalog_links l ON l.choice_group_id = g.id WHERE l.hubrise_key = 'SAUMON-6|EXTRA'`))
	assert.Zero(t, min, "the extras are optional in HubRise too")
}

func TestOffSaleAndHiddenEntriesFollowHubRise(t *testing.T) {
	e := newCatEnv(t)
	m := sushiMenu()
	e.publish(m)
	e.fullImport()
	cola := e.product("COLA-33")
	// The shop sold the cola out by hand, and re-hid the 12 piece SKU: those are the shop's decisions.
	e.exec(`UPDATE products SET is_available = false WHERE id = $1`, cola.ID)

	// HubRise takes the 6 piece SKU off sale (restrictions.enabled=false), tags the cola hidden, and
	// switches an option off.
	m2 := sushiMenu()
	m2.d.Products[0].Skus[0].Restrictions = &hubrise.Restrictions{Enabled: ptr(false)}
	m2.d.Products[1].Tags = []string{"hidden"}
	m2.d.OptionLists[1].Options[1].Restrictions = &hubrise.Restrictions{Enabled: ptr(false)}
	e.publish(m2)
	e.fullImport()

	six := e.product("SAUMON-6")
	assert.False(t, six.Available)
	assert.False(t, six.Visible)
	link, err := e.links.FindByKey(t.Context(), domain.EntityProduct, "SAUMON-6")
	require.NoError(t, err)
	assert.True(t, link.CatalogDisabled)
	assert.False(t, e.product("COLA-33").Visible, "the tag hides it")
	assert.False(t, e.product("COLA-33").Available, "its manual sold out stays")
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM product_choices c JOIN hubrise_catalog_links l ON l.choice_id = c.id
		WHERE l.hubrise_key = 'SAUMON-6|EXTRA|EGG' AND NOT c.is_available`))
	choiceLink, err := e.links.FindByKey(t.Context(), domain.EntityChoice, "SAUMON-6|EXTRA|EGG")
	require.NoError(t, err)
	assert.True(t, choiceLink.CatalogDisabled)

	// HubRise gives them back. What it took away is returned; the shop's own sold out is not.
	e.exec(`UPDATE products SET is_visible = true WHERE id = $1`, cola.ID) // the shop shows the cola again
	e.publish(sushiMenu())
	e.fullImport()

	back := e.product("SAUMON-6")
	assert.True(t, back.Available, "HubRise took it off sale, so HubRise gives it back")
	assert.True(t, back.Visible)
	assert.False(t, e.product("COLA-33").Available, "a sold out HubRise never took away is never restored")
	assert.True(t, e.product("COLA-33").Visible, "the shop's visibility decision stands when HubRise's hidden tag goes")
	assert.Equal(t, 0, e.count(`SELECT count(*) FROM product_choices WHERE NOT is_available`))
	link, err = e.links.FindByKey(t.Context(), domain.EntityProduct, "SAUMON-6")
	require.NoError(t, err)
	assert.False(t, link.CatalogDisabled)
}

func TestInventorySoldOutIsNotRestoredByTheCatalog(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	link, err := e.links.FindByKey(t.Context(), domain.EntityProduct, "COLA-33")
	require.NoError(t, err)
	// The inventory sync (a later task) marked the cola sold out by HubRise inventory.
	require.NoError(t, e.links.UpdateInventory(t.Context(), link.ID, domain.InventoryState{SoldOut: true, SyncedAt: e.clock.Now()}))
	e.exec(`UPDATE products SET is_available = false WHERE id = $1`, link.LocalID())

	// The catalog flips its enabled flag off and on again: availability is the inventory's business.
	m := sushiMenu()
	m.d.Products[1].Skus[0].Restrictions = &hubrise.Restrictions{Enabled: ptr(false)}
	e.publish(m)
	e.fullImport()
	e.publish(sushiMenu())
	e.fullImport()

	assert.False(t, e.productByID(link.LocalID()).Available, "still sold out by inventory")
}

func TestVatUnmappedProductsAreCreatedHidden(t *testing.T) {
	e := newCatEnv(t)
	m := newMenu().category("DRINK", "Boissons").product("COLA", "DRINK", "Cola", sku("COLA-33", "3"))
	m.d.Products[0].TaxRate = &hubrise.TaxRate{Delivery: "6", Collection: "6", EatIn: "21"}
	e.publish(m)

	res := e.fullImport()

	assert.Contains(t, res.Warnings, domain.Warning{Code: domain.WarnVATUnmapped, Key: "COLA-33",
		Message: res.Warnings[0].Message})
	cola := e.product("COLA-33")
	assert.False(t, cola.Visible)
	assert.True(t, cola.Available)
	assert.Equal(t, "food", cola.VatCategory, "the column is mandatory, the product stays hidden")

	// The VAT category arrives in HubRise: the product gets it, and is shown.
	s := sku("COLA-33", "3")
	s.CustomFields = []byte(`{"vat_category":"beverage"}`)
	e.publish(newMenu().category("DRINK", "Boissons").product("COLA", "DRINK", "Cola", s))
	e.fullImport()
	cola = e.product("COLA-33")
	assert.Equal(t, "beverage", cola.VatCategory)
	assert.True(t, cola.Visible)
}

func TestImportCreatedProductsAreNotSubjectToTheDashboardTranslationRule(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	// A visible product with a single translation: the dashboard would refuse to create it, the
	// import must not.
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products WHERE is_visible`))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM product_translations WHERE product_id = $1`, e.product("COLA-33").ID))
}

func TestSlugsAreUniqueAndStable(t *testing.T) {
	e := newCatEnv(t)
	// Two SKUs whose composed names are identical, and a category whose name equals another's.
	e.publish(newMenu().category("A", "Plats").category("B", "Plats").
		product("P1", "A", "Riz", sku("RIZ-1", "4")).
		product("P2", "A", "Riz", sku("RIZ-2", "4.50")).
		product("P3", "B", "Riz", sku("RIZ-3", "5")))
	e.fullImport()

	slugs := []string{e.product("RIZ-1").Slug, e.product("RIZ-2").Slug, e.product("RIZ-3").Slug}
	assert.Equal(t, []string{"plats-riz", "plats-riz-2", "plats-riz-3"}, slugs)
	var cats []string
	require.NoError(t, e.db.SelectContext(t.Context(), &cats, `SELECT slug FROM product_categories ORDER BY "order"`))
	assert.Equal(t, []string{"plats", "plats-2"}, cats)

	// Renaming in HubRise does not move the public URL.
	m := newMenu().category("A", "Plats du jour").category("B", "Plats").
		product("P1", "A", "Riz blanc", sku("RIZ-1", "4")).
		product("P2", "A", "Riz", sku("RIZ-2", "4.50")).
		product("P3", "B", "Riz", sku("RIZ-3", "5"))
	e.publish(m)
	e.fullImport()
	assert.Equal(t, slugs[0], e.product("RIZ-1").Slug)
	assert.Equal(t, "Riz blanc", e.productTexts(e.product("RIZ-1").ID)["fr"].Name)
}

func TestNewCategoriesFollowTheExistingOnes(t *testing.T) {
	e := newCatEnv(t)
	local := newLocalCategory(t, e, 7, "Sushi")
	_ = local
	e.publish(newMenu().category("A", "Plats").category("B", "Desserts").product("P", "A", "Riz", sku("RIZ", "4")).product("Q", "B", "Mochi", sku("MOCHI", "3")))
	e.fullImport()

	var orders []int
	require.NoError(t, e.db.SelectContext(t.Context(), &orders, `
		SELECT c."order" FROM product_categories c JOIN hubrise_catalog_links l ON l.category_id = c.id ORDER BY c."order"`))
	assert.Equal(t, []int{8, 9}, orders, "after the highest existing order, in catalog order")
}

func newLocalCategory(t *testing.T, e *cenv, order int, frName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO product_categories (id, "order", slug) VALUES ($1, $2, $3)`, id, order, "local-"+id.String()[:6])
	e.exec(`INSERT INTO product_category_translations (product_category_id, language, name) VALUES ($1, 'fr', $2)`, id, frName)
	return id
}

func TestAFailingBatchRollsBackAloneAndTheOthersLand(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.svc.Local = failingLocal{LocalCatalogRepository: e.local, failOn: "COLA-33"}

	res, err := e.svc.Sync(t.Context(), syncReq(domain.SyncFull))
	require.NoError(t, err)

	require.Len(t, res.Result.Failed, 1)
	assert.Equal(t, "DRINK", res.Result.Failed[0].Batch)
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM products`), "the makis landed")
	assert.Equal(t, 0, e.count(`SELECT count(*) FROM hubrise_catalog_links WHERE hubrise_key IN ('COLA-33', 'DRINK')`), "the drinks batch left nothing behind")
	conn := e.connection()
	assert.Nil(t, conn.CatalogHash, "a partial import is not recorded as done")
	require.NotNil(t, conn.LastError)
	assert.Contains(t, *conn.LastError, "injected batch failure")

	// The next run retries what is missing.
	e.svc.Local = e.local
	res = e.fullImport()
	assert.Equal(t, 1, res.Result.Count(domain.EntityProduct, domain.ActionCreate))
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`))
	assert.NotNil(t, e.connection().CatalogHash)
}

func syncReq(mode domain.SyncMode) application.SyncRequest {
	return application.SyncRequest{Mode: mode}
}
