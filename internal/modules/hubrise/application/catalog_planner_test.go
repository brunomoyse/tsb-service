package application_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
)

// ---- builders of the pure tests ----

func txt(name string) map[string]domain.LocalText {
	return map[string]domain.LocalText{"fr": {Name: name, Source: domain.SourceManual}}
}

func hubTxt(name string) map[string]domain.LocalText {
	return map[string]domain.LocalText{"fr": {Name: name, Source: domain.SourceHubrise}}
}

func names(name string) map[string]string { return map[string]string{"fr": name} }

type world struct {
	cat   *domain.Catalog
	local *domain.LocalSnapshot
	links []domain.CatalogLink
}

func newWorld() *world {
	// The slices are sized up front: the builders hand out pointers into them.
	return &world{
		cat: &domain.Catalog{Language: "fr", Categories: make([]domain.CatalogCategory, 0, 32), Products: make([]domain.CatalogProduct, 0, 32)},
		local: &domain.LocalSnapshot{
			Categories: make([]domain.LocalCategory, 0, 32), Products: make([]domain.LocalProduct, 0, 32),
			Groups: make([]domain.LocalGroup, 0, 32), Choices: make([]domain.LocalChoice, 0, 32),
		},
		links: make([]domain.CatalogLink, 0, 64),
	}
}

func (w *world) category(key, name string) *domain.CatalogCategory {
	w.cat.Categories = append(w.cat.Categories, domain.CatalogCategory{Key: key, Names: names(name), Order: len(w.cat.Categories)})
	return &w.cat.Categories[len(w.cat.Categories)-1]
}

func (w *world) product(key, category, name, price string) *domain.CatalogProduct {
	w.cat.Products = append(w.cat.Products, domain.CatalogProduct{
		Key: key, CategoryKey: category, Texts: map[string]domain.Text{"fr": {Name: name}},
		Price: dec(price), VatCategory: domain.VatFood, Visible: true,
	})
	return &w.cat.Products[len(w.cat.Products)-1]
}

func group(key, name string, min, max int, choices ...domain.CatalogChoice) domain.CatalogGroup {
	return domain.CatalogGroup{Key: key, Names: names(name), Min: min, Max: max, Choices: choices}
}

func choice(key, name, price string) domain.CatalogChoice {
	return domain.CatalogChoice{Key: key, Names: names(name), PriceModifier: dec(price)}
}

func (w *world) localCategory(name string, order int) uuid.UUID {
	id := uuid.New()
	w.local.Categories = append(w.local.Categories, domain.LocalCategory{ID: id, Slug: "slug-" + id.String()[:4], Order: order, Names: txt(name)})
	return id
}

func (w *world) localProduct(cat uuid.UUID, code, name, price string) *domain.LocalProduct {
	w.local.Products = append(w.local.Products, domain.LocalProduct{
		ID: uuid.New(), CategoryID: cat, Code: code, Price: dec(price), VatCategory: domain.VatFood, Visible: true, Available: true, Texts: txt(name),
	})
	return &w.local.Products[len(w.local.Products)-1]
}

func (w *world) localGroup(product uuid.UUID, name string, min, max, sort int) *domain.LocalGroup {
	w.local.Groups = append(w.local.Groups, domain.LocalGroup{ID: uuid.New(), ProductID: product, Min: min, Max: max, Sort: sort, Names: txt(name)})
	return &w.local.Groups[len(w.local.Groups)-1]
}

func (w *world) localChoice(group, product uuid.UUID, name, modifier string, sort int) *domain.LocalChoice {
	w.local.Choices = append(w.local.Choices, domain.LocalChoice{ID: uuid.New(), GroupID: group, ProductID: product, PriceModifier: dec(modifier), Sort: sort, Names: txt(name)})
	return &w.local.Choices[len(w.local.Choices)-1]
}

func (w *world) link(t domain.EntityType, key string, local uuid.UUID, hash string) *domain.CatalogLink {
	l := domain.CatalogLink{ID: int64(len(w.links) + 1), EntityType: t, HubriseKey: key, HubriseRef: key, ContentHash: hash, Origin: domain.OriginImport}
	l.SetLocalID(local)
	w.links = append(w.links, l)
	return &w.links[len(w.links)-1]
}

func (w *world) plan(mode domain.SyncMode) *domain.Diff {
	return application.Plan(application.PlanInput{Catalog: w.cat, Local: w.local, Links: application.NewLinkIndex(w.links), Mode: mode})
}

func find(d *domain.Diff, t domain.EntityType, key string) *domain.Change {
	for i := range d.Changes {
		if d.Changes[i].Entity == t && d.Changes[i].Key == key {
			return &d.Changes[i]
		}
	}
	return nil
}

// ---- tests ----

func TestPlanCreatesEverythingOnAnEmptyMenu(t *testing.T) {
	w := newWorld()
	w.category("A", "Plats")
	w.category("B", "Desserts")
	p := w.product("RIZ", "A", "Riz", "4")
	p.Groups = []domain.CatalogGroup{group("RIZ|S", "Sauce", 1, 1, choice("RIZ|S|X", "Soja", "0"))}
	disabled := w.product("OFF", "B", "Hors vente", "2")
	disabled.Disabled, disabled.Visible = true, false

	d := w.plan(domain.SyncFull)

	assert.Equal(t, 2, d.Count(domain.EntityCategory, domain.ActionCreate))
	assert.Equal(t, 2, d.Count(domain.EntityProduct, domain.ActionCreate))
	assert.Equal(t, 1, d.Count(domain.EntityChoiceGroup, domain.ActionCreate))
	assert.Equal(t, 1, d.Count(domain.EntityChoice, domain.ActionCreate))
	assert.Equal(t, 1, find(d, domain.EntityCategory, "A").Order)
	assert.Equal(t, 2, find(d, domain.EntityCategory, "B").Order)
	assert.Equal(t, domain.OriginImport, find(d, domain.EntityProduct, "RIZ").Origin)
	off := find(d, domain.EntityProduct, "OFF")
	assert.False(t, *off.SetVisible)
	assert.False(t, *off.SetAvailable)
	assert.True(t, off.Disabled)
	assert.NotEmpty(t, d.CatalogHash)
	assert.False(t, d.RetireNeedsConfirm)
	assert.Equal(t, []string{"A", "B"}, []string{d.Batches()[0][0].Batch, d.Batches()[1][0].Batch})
}

func TestPlanNewCategoriesFollowTheHighestLocalOrder(t *testing.T) {
	w := newWorld()
	w.localCategory("Existante", 9)
	w.category("A", "Nouvelle")
	assert.Equal(t, 10, find(w.plan(domain.SyncFull), domain.EntityCategory, "A").Order)
}

func TestPlanAdoptsByCodeThenByName(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	byCode := w.localProduct(cat, "a2", "Un autre nom", "3")
	byName := w.localProduct(cat, "", "  RIZ nature ", "4")
	w.localProduct(cat, "", "Sans lien", "1")
	w.category("A", "plats")
	c := w.product("SKU-CODE", "A", "Riz sauté", "3.50")
	c.Code = "A2"
	w.product("SKU-NAME", "A", "Riz Nature", "4")

	d := w.plan(domain.SyncFull)

	assert.Equal(t, domain.ActionLink, find(d, domain.EntityCategory, "A").Action)
	code := find(d, domain.EntityProduct, "SKU-CODE")
	assert.Equal(t, domain.ActionLink, code.Action)
	assert.Equal(t, byCode.ID, code.LocalID)
	assert.Equal(t, domain.OriginAdopted, code.Origin)
	assert.Contains(t, code.Fields, domain.FieldPrice, "a FULL adoption updates what HubRise owns")
	assert.NotContains(t, code.Fields, domain.FieldNames, "a manual name is never rewritten")
	name := find(d, domain.EntityProduct, "SKU-NAME")
	assert.Equal(t, byName.ID, name.LocalID)
	assert.Empty(t, d.Conflicts)
	assert.Zero(t, d.Count(domain.EntityProduct, domain.ActionCreate))
}

func TestPlanNeverGuessesAnAmbiguousMatch(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.localProduct(cat, "A1", "Riz", "3")
	w.localProduct(cat, "A1", "Riz bis", "3")
	w.localProduct(cat, "", "Nems", "3")
	w.localProduct(cat, "", "nems", "3")
	w.category("A", "Plats")
	byCode := w.product("BY-CODE", "A", "Riz", "3")
	byCode.Code = "A1"
	w.product("BY-NAME", "A", "Nems", "3")

	d := w.plan(domain.SyncFull)

	require.Len(t, d.Conflicts, 2)
	assert.Equal(t, "BY-CODE", d.Conflicts[0].Key)
	assert.Len(t, d.Conflicts[0].Candidates, 2)
	assert.Equal(t, "BY-NAME", d.Conflicts[1].Key)
	assert.Nil(t, find(d, domain.EntityProduct, "BY-CODE"))
	assert.Nil(t, find(d, domain.EntityProduct, "BY-NAME"))
}

func TestPlanTwoEntriesCannotClaimOneRow(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.localProduct(cat, "A1", "Riz", "3")
	w.category("A", "Plats")
	first := w.product("ONE", "A", "Riz", "3")
	first.Code = "A1"
	second := w.product("TWO", "A", "Riz blanc", "3")
	second.Code = "A1"

	d := w.plan(domain.SyncFull)

	assert.Equal(t, domain.ActionLink, find(d, domain.EntityProduct, "ONE").Action)
	require.Len(t, d.Conflicts, 1)
	assert.Equal(t, "TWO", d.Conflicts[0].Key)
	assert.Contains(t, d.Conflicts[0].Candidates[0], "(taken)")
}

func TestPlanLeavesProductsOfAnAmbiguousCategoryAlone(t *testing.T) {
	w := newWorld()
	w.localCategory("Plats", 1)
	w.localCategory("plats", 2)
	w.category("A", "Plats")
	w.product("RIZ", "A", "Riz", "3")

	d := w.plan(domain.SyncFull)

	require.Len(t, d.Conflicts, 1)
	assert.Equal(t, domain.EntityCategory, d.Conflicts[0].Entity)
	assert.Empty(t, d.Changes, "neither the category nor its products are touched")
}

func TestPlanDoesNotAdoptByNameOutsideTheMatchedCategory(t *testing.T) {
	w := newWorld()
	other := w.localCategory("Desserts", 1)
	w.localProduct(other, "", "Riz", "3")
	w.category("A", "Plats")
	w.product("RIZ", "A", "Riz", "3")
	w.category("B", "")
	w.product("NAMELESS", "B", "", "3")

	d := w.plan(domain.SyncFull)

	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityProduct, "RIZ").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityProduct, "NAMELESS").Action, "an empty name never matches")
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityCategory, "B").Action)
}

func TestPlanLinkOnlyKeepsTheLinksAndNothingElse(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.localProduct(cat, "", "Riz", "3")
	gone := w.localProduct(cat, "", "Parti", "3")
	w.link(domain.EntityProduct, "GONE", gone.ID, "h")
	w.category("A", "Plats")
	w.product("RIZ", "A", "Riz", "4")
	w.product("NEW", "A", "Nouveau", "4")

	d := w.plan(domain.SyncLinkOnly)

	for _, c := range d.Changes {
		assert.Equal(t, domain.ActionLink, c.Action)
	}
	assert.Equal(t, 2, len(d.Changes), "the category and the product that exist already")
	assert.Zero(t, d.RetiringProducts)
	assert.False(t, d.RetireNeedsConfirm)
}

func TestPlanRetiresWhatHubRiseDroppedAndFlagsMassRetirement(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	for i := 0; i < 6; i++ {
		p := w.localProduct(cat, "", "P"+string(rune('a'+i)), "3")
		w.link(domain.EntityProduct, "P"+string(rune('a'+i)), p.ID, "h")
	}
	g := w.localGroup(w.local.Products[0].ID, "Sauce", 1, 1, 0)
	w.link(domain.EntityChoiceGroup, "Pa|S", g.ID, "h")
	c := w.localChoice(g.ID, w.local.Products[0].ID, "Soja", "0", 0)
	w.link(domain.EntityChoice, "Pa|S|X", c.ID, "h")
	lc := w.local.Categories[0].ID
	w.link(domain.EntityCategory, "OLD", lc, "h")
	// A retired link and a link whose row is gone are left alone.
	retired := w.link(domain.EntityProduct, "OLDER", w.local.Products[1].ID, "h")
	now := time.Now()
	retired.RetiredAt = &now
	w.link(domain.EntityProduct, "ORPHAN", uuid.New(), "h")
	w.category("A", "Plats")
	w.product("Pf", "A", "Pf", "3")

	d := w.plan(domain.SyncFull)

	assert.Equal(t, 5, d.RetiringProducts, "Pa..Pe, the present one and the retired one excluded")
	assert.Equal(t, 7, d.LinkedProducts, "the six, the orphan; the retired one is not counted")
	assert.True(t, d.RetireNeedsConfirm)
}

func TestPlanRetiresTheDroppedEntitiesOfEveryKind(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	p := w.localProduct(cat, "", "Riz", "3")
	g := w.localGroup(p.ID, "Sauce", 1, 1, 0)
	ch := w.localChoice(g.ID, p.ID, "Soja", "0", 0)
	w.link(domain.EntityProduct, "RIZ", p.ID, "h")
	w.link(domain.EntityChoiceGroup, "RIZ|S", g.ID, "h")
	w.link(domain.EntityChoice, "RIZ|S|X", ch.ID, "h")
	w.link(domain.EntityCategory, "A", cat, "h")
	w.category("B", "Autre")
	w.product("OTHER", "B", "Autre", "1")

	d := w.plan(domain.SyncFull)

	for _, e := range []struct {
		t   domain.EntityType
		key string
	}{{domain.EntityProduct, "RIZ"}, {domain.EntityChoiceGroup, "RIZ|S"}, {domain.EntityChoice, "RIZ|S|X"}, {domain.EntityCategory, "A"}} {
		c := find(d, e.t, e.key)
		require.NotNil(t, c, e.key)
		assert.Equal(t, domain.ActionRetire, c.Action)
		assert.Equal(t, domain.RetireBatch, c.Batch)
		assert.NotEmpty(t, c.Name)
	}
	assert.Equal(t, domain.RetireBatch, func() string { b := d.Batches(); return b[len(b)-1][0].Batch }(), "retirements come last")
}

func TestPlanCategoryStates(t *testing.T) {
	w := newWorld()
	same := w.localCategory("Plats", 1)
	renamed := w.localCategory("Ancien", 2)
	back := w.localCategory("Retour", 3)
	hubName := w.localCategory("Source", 4)
	a := w.category("SAME", "Plats")
	w.link(domain.EntityCategory, "SAME", same, a.Hash())
	w.category("REN", "Nouveau nom")
	w.link(domain.EntityCategory, "REN", renamed, "stale")
	b := w.category("BACK", "Retour")
	r := w.link(domain.EntityCategory, "BACK", back, b.Hash())
	now := time.Now()
	r.RetiredAt = &now
	w.category("HUB", "Nom v2")
	w.link(domain.EntityCategory, "HUB", hubName, "stale")
	w.local.Categories[3].Names = hubTxt("Nom v1")
	w.link(domain.EntityCategory, "NOROW", uuid.New(), "h")
	w.category("NOROW", "Sans ligne")

	d := w.plan(domain.SyncFull)

	assert.Nil(t, find(d, domain.EntityCategory, "SAME"))
	assert.Equal(t, 1, d.Unchanged)
	assert.Equal(t, domain.ActionUpdate, find(d, domain.EntityCategory, "REN").Action)
	assert.Empty(t, find(d, domain.EntityCategory, "REN").Fields, "a manual name is a reason to write nothing")
	assert.Equal(t, domain.ActionRevive, find(d, domain.EntityCategory, "BACK").Action)
	assert.Equal(t, []string{domain.FieldNames}, find(d, domain.EntityCategory, "HUB").Fields)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityCategory, "NOROW").Action, "a link to a vanished row is replaced")
}

func TestPlanProductStates(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	w.link(domain.EntityCategory, "A", cat, w.cat.Categories[0].Hash())

	// unchanged
	p1 := w.product("SAME", "A", "Riz", "3")
	l1 := w.localProduct(cat, "", "Riz", "3")
	l1.Texts = hubTxt("Riz")
	w.link(domain.EntityProduct, "SAME", l1.ID, p1.ProductHash())

	// price + category + vat + code
	p2 := w.product("DIFF", "A", "Nems", "5")
	p2.Code, p2.VatCategory = "N1", domain.VatBeverage
	l2 := w.localProduct(uuid.New(), "N0", "Nems", "4")
	w.link(domain.EntityProduct, "DIFF", l2.ID, "stale")

	// HubRise hides it: the local row is visible
	p3 := w.product("HIDE", "A", "Cache", "3")
	p3.Visible = false
	l3 := w.localProduct(cat, "", "Cache", "3")
	w.link(domain.EntityProduct, "HIDE", l3.ID, "stale:v1")

	// the hash does not carry the visibility yet (an old link), HubRise hides it
	p4 := w.product("HIDE-OLD", "A", "Ancien", "3")
	p4.Visible = false
	l4 := w.localProduct(cat, "", "Ancien", "3")
	w.link(domain.EntityProduct, "HIDE-OLD", l4.ID, "stale")

	// the shop hid it by hand, HubRise did not change its mind: stays
	w.product("MANUAL-HIDE", "A", "Manuel", "3")
	l5 := w.localProduct(cat, "", "Manuel", "3")
	l5.Visible = false
	w.link(domain.EntityProduct, "MANUAL-HIDE", l5.ID, "stale:v1")

	// HubRise hid it earlier and un-hides it now: the hidden row is shown again
	p6 := w.product("SHOW", "A", "Montre", "3")
	l6 := w.localProduct(cat, "", "Montre", "3")
	l6.Visible = false
	w.link(domain.EntityProduct, "SHOW", l6.ID, "stale:v0")
	_ = p6

	// HubRise un-hides it and the row is visible already: nothing to write
	p7 := w.product("SHOW2", "A", "Montre2", "3")
	l7 := w.localProduct(cat, "", "Montre2", "3")
	l7.Visible = true
	w.link(domain.EntityProduct, "SHOW2", l7.ID, "stale:v0")
	_ = p7

	// off sale: HubRise disables it, the local row is available
	p8 := w.product("OFF", "A", "Off", "3")
	p8.Disabled = true
	l8 := w.localProduct(cat, "", "Off", "3")
	w.link(domain.EntityProduct, "OFF", l8.ID, "stale")

	// back on sale: HubRise enables it, the link remembers it disabled, the row is unavailable
	p9 := w.product("ON", "A", "On", "3")
	l9 := w.localProduct(cat, "", "On", "3")
	l9.Available = false
	lk := w.link(domain.EntityProduct, "ON", l9.ID, "stale")
	lk.CatalogDisabled = true
	_ = p9

	// back on sale, but the inventory sold it out: stays unavailable
	pa := w.product("SOLD", "A", "Epuise", "3")
	la := w.localProduct(cat, "", "Epuise", "3")
	la.Available = false
	lk = w.link(domain.EntityProduct, "SOLD", la.ID, "stale")
	lk.CatalogDisabled, lk.InventorySoldOut = true, true
	_ = pa

	// only the content hash moved
	pb := w.product("CONTENT", "A", "Contenu", "3")
	pb.Halal = true
	lb := w.localProduct(cat, "", "Contenu", "3")
	w.link(domain.EntityProduct, "CONTENT", lb.ID, "stale:v1")
	_ = pb
	w.local.Products[len(w.local.Products)-1].Texts = hubTxt("Contenu")

	// the catalog flag differs from the link while the row already agrees
	pc := w.product("FLAG", "A", "Drapeau", "3")
	pc.Disabled = true
	lc := w.localProduct(cat, "", "Drapeau", "3")
	lc.Available = false
	w.link(domain.EntityProduct, "FLAG", lc.ID, pc.ProductHash())
	w.local.Products[len(w.local.Products)-1].Texts = hubTxt("Drapeau")

	// revive
	pd := w.product("REVIVE", "A", "Revient", "3")
	pd.Disabled = true
	ld := w.localProduct(cat, "", "Revient", "3")
	now := time.Now()
	w.link(domain.EntityProduct, "REVIVE", ld.ID, "stale").RetiredAt = &now

	d := w.plan(domain.SyncFull)

	assert.Nil(t, find(d, domain.EntityProduct, "SAME"))
	diff := find(d, domain.EntityProduct, "DIFF")
	assert.ElementsMatch(t, []string{domain.FieldPrice, domain.FieldCategory, domain.FieldVat, domain.FieldCode}, diff.Fields)
	assert.False(t, *find(d, domain.EntityProduct, "HIDE").SetVisible)
	assert.False(t, *find(d, domain.EntityProduct, "HIDE-OLD").SetVisible)
	assert.Nil(t, find(d, domain.EntityProduct, "MANUAL-HIDE").SetVisible)
	assert.True(t, *find(d, domain.EntityProduct, "SHOW").SetVisible, "HubRise hid it, HubRise shows it again")
	assert.Nil(t, find(d, domain.EntityProduct, "SHOW2").SetVisible, "the row is visible already")
	assert.False(t, *find(d, domain.EntityProduct, "OFF").SetAvailable)
	assert.True(t, *find(d, domain.EntityProduct, "ON").SetAvailable)
	assert.Nil(t, find(d, domain.EntityProduct, "SOLD").SetAvailable)
	assert.Equal(t, []string{domain.FieldContent}, find(d, domain.EntityProduct, "CONTENT").Fields)
	assert.Equal(t, []string{domain.FieldContent}, find(d, domain.EntityProduct, "FLAG").Fields, "bookkeeping only")
	revive := find(d, domain.EntityProduct, "REVIVE")
	assert.Equal(t, domain.ActionRevive, revive.Action)
	assert.False(t, *revive.SetAvailable)
	assert.True(t, *revive.SetVisible)
}

func TestPlanAdoptedProductsOnlyHideAndDisableWhatHubRiseHides(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	hidden := w.product("H", "A", "Cache", "3")
	hidden.Visible, hidden.Disabled = false, true
	w.localProduct(cat, "", "Cache", "3")
	shown := w.product("S", "A", "Montre", "3")
	_ = shown
	w.localProduct(cat, "", "Montre", "3").Visible = false

	d := w.plan(domain.SyncFull)

	h := find(d, domain.EntityProduct, "H")
	assert.False(t, *h.SetVisible)
	assert.False(t, *h.SetAvailable)
	assert.Contains(t, h.Fields, domain.FieldVisibility)
	assert.Contains(t, h.Fields, domain.FieldAvailable)
	s := find(d, domain.EntityProduct, "S")
	assert.Nil(t, s.SetVisible, "a hidden shop product is never shown by an adoption")
	assert.Nil(t, s.SetAvailable)
}

func TestPlanGroupsAndChoices(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	w.link(domain.EntityCategory, "A", cat, w.cat.Categories[0].Hash())
	p := w.product("RIZ", "A", "Riz", "3")
	lp := w.localProduct(cat, "", "Riz", "3")
	lp.Texts = hubTxt("Riz")
	w.link(domain.EntityProduct, "RIZ", lp.ID, p.ProductHash())

	// an adoptable group with an adoptable choice, an adoptable disabled choice
	soy := choice("RIZ|S|SOJA", "Soja", "0.50")
	hot := choice("RIZ|S|HOT", "Piment", "0")
	hot.Disabled = true
	// a linked choice that changed price, one that is unchanged, one revived, one switched off, one back on
	changed := choice("RIZ|S|CH", "Change", "1")
	same := choice("RIZ|S|SAME", "Pareil", "1")
	back := choice("RIZ|S|BACK", "Retour", "1")
	off := choice("RIZ|S|OFF", "Eteint", "1")
	off.Disabled = true
	on := choice("RIZ|S|ON", "Rallume", "1")
	sold := choice("RIZ|S|SOLD", "Epuise", "1")
	g := group("RIZ|S", "Sauce", 1, 2, soy, hot, changed, same, back, off, on, sold)
	g.Sort = 3
	p.Groups = []domain.CatalogGroup{g}

	lg := w.localGroup(lp.ID, "sauce", 0, 1, 0)
	w.localChoice(lg.ID, lp.ID, "SOJA", "0", 0)
	w.localChoice(lg.ID, lp.ID, "Piment", "0", 0)
	lchg := w.localChoice(lg.ID, lp.ID, "Change", "0", 0)
	lsame := w.localChoice(lg.ID, lp.ID, "Pareil", "1", 0)
	lsame.Names = hubTxt("Pareil")
	lback := w.localChoice(lg.ID, lp.ID, "Retour", "1", 0)
	loff := w.localChoice(lg.ID, lp.ID, "Eteint", "1", 0)
	lon := w.localChoice(lg.ID, lp.ID, "Rallume", "1", 0)
	lon.Unavailable = true
	lsold := w.localChoice(lg.ID, lp.ID, "Epuise", "1", 0)
	lsold.Unavailable = true
	w.link(domain.EntityChoice, "RIZ|S|CH", lchg.ID, "stale")
	w.link(domain.EntityChoice, "RIZ|S|SAME", lsame.ID, same.Hash())
	now := time.Now()
	w.link(domain.EntityChoice, "RIZ|S|BACK", lback.ID, "stale").RetiredAt = &now
	w.link(domain.EntityChoice, "RIZ|S|OFF", loff.ID, "stale")
	lk := w.link(domain.EntityChoice, "RIZ|S|ON", lon.ID, "stale")
	lk.CatalogDisabled = true
	lk = w.link(domain.EntityChoice, "RIZ|S|SOLD", lsold.ID, "stale")
	lk.CatalogDisabled, lk.InventorySoldOut = true, true

	d := w.plan(domain.SyncFull)

	gc := find(d, domain.EntityChoiceGroup, "RIZ|S")
	assert.Equal(t, domain.ActionLink, gc.Action)
	assert.Equal(t, domain.OriginAdopted, gc.Origin)
	assert.ElementsMatch(t, []string{domain.FieldLimits, domain.FieldOrder}, gc.Fields)
	adopted := find(d, domain.EntityChoice, "RIZ|S|SOJA")
	assert.Equal(t, domain.ActionLink, adopted.Action)
	assert.Contains(t, adopted.Fields, domain.FieldPrice)
	adoptedOff := find(d, domain.EntityChoice, "RIZ|S|HOT")
	assert.False(t, *adoptedOff.SetAvailable)
	assert.Equal(t, domain.ActionUpdate, find(d, domain.EntityChoice, "RIZ|S|CH").Action)
	assert.Nil(t, find(d, domain.EntityChoice, "RIZ|S|SAME"))
	revived := find(d, domain.EntityChoice, "RIZ|S|BACK")
	assert.Equal(t, domain.ActionRevive, revived.Action)
	assert.True(t, *revived.SetAvailable)
	assert.False(t, *find(d, domain.EntityChoice, "RIZ|S|OFF").SetAvailable)
	assert.True(t, *find(d, domain.EntityChoice, "RIZ|S|ON").SetAvailable)
	assert.Nil(t, find(d, domain.EntityChoice, "RIZ|S|SOLD").SetAvailable)
}

func TestPlanGroupStates(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	w.link(domain.EntityCategory, "A", cat, w.cat.Categories[0].Hash())
	p := w.product("RIZ", "A", "Riz", "3")
	lp := w.localProduct(cat, "", "Riz", "3")
	lp.Texts = hubTxt("Riz")
	w.link(domain.EntityProduct, "RIZ", lp.ID, p.ProductHash())

	same := group("RIZ|SAME", "Pareil", 0, 1)
	hashOnly := group("RIZ|HASH", "Hash", 0, 1)
	back := group("RIZ|BACK", "Retour", 0, 1)
	amb := group("RIZ|AMB", "Double", 0, 1)
	fresh := group("RIZ|NEW", "", 0, 1, choice("RIZ|NEW|X", "Choix", "0"))
	p.Groups = []domain.CatalogGroup{same, hashOnly, back, amb, fresh}

	lsame := w.localGroup(lp.ID, "Pareil", 0, 1, 0)
	lsame.Names = hubTxt("Pareil")
	lhash := w.localGroup(lp.ID, "Hash", 0, 1, 0)
	lhash.Names = hubTxt("Hash")
	lback := w.localGroup(lp.ID, "Retour", 0, 1, 0)
	w.localGroup(lp.ID, "Double", 0, 1, 0)
	w.localGroup(lp.ID, "double", 0, 1, 0)
	w.link(domain.EntityChoiceGroup, "RIZ|SAME", lsame.ID, same.Hash())
	w.link(domain.EntityChoiceGroup, "RIZ|HASH", lhash.ID, "stale")
	now := time.Now()
	w.link(domain.EntityChoiceGroup, "RIZ|BACK", lback.ID, "stale").RetiredAt = &now

	d := w.plan(domain.SyncFull)

	assert.Nil(t, find(d, domain.EntityChoiceGroup, "RIZ|SAME"))
	assert.Equal(t, []string{domain.FieldContent}, find(d, domain.EntityChoiceGroup, "RIZ|HASH").Fields)
	assert.Equal(t, domain.ActionRevive, find(d, domain.EntityChoiceGroup, "RIZ|BACK").Action)
	require.Len(t, d.Conflicts, 1)
	assert.Equal(t, "RIZ|AMB", d.Conflicts[0].Key)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoiceGroup, "RIZ|NEW").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoice, "RIZ|NEW|X").Action)
}

func TestPlanAmbiguousChoiceIsReported(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	p := w.product("RIZ", "A", "Riz", "3")
	lp := w.localProduct(cat, "", "Riz", "3")
	p.Groups = []domain.CatalogGroup{group("RIZ|S", "Sauce", 0, 1, choice("RIZ|S|X", "Soja", "0"), choice("RIZ|S|Y", "", "0"))}
	lg := w.localGroup(lp.ID, "Sauce", 0, 1, 0)
	w.localChoice(lg.ID, lp.ID, "Soja", "0", 0)
	w.localChoice(lg.ID, lp.ID, "soja", "0", 0)

	d := w.plan(domain.SyncFull)

	require.Len(t, d.Conflicts, 1)
	assert.Equal(t, domain.EntityChoice, d.Conflicts[0].Entity)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoice, "RIZ|S|Y").Action, "an unnamed choice never matches")
}

func TestPlanFallsBackToFrenchNamesAndReportsMapperWarnings(t *testing.T) {
	w := newWorld()
	w.cat.Language = "en"
	w.cat.Warnings = []domain.Warning{{Code: domain.WarnVATUnmapped, Key: "X"}}
	cat := w.localCategory("Plats", 1)
	w.local.Categories[0].Names = map[string]domain.LocalText{"fr": {Name: "Plats"}}
	w.cat.Categories = []domain.CatalogCategory{{Key: "A", Names: map[string]string{"en": "Plats"}}}
	_ = cat

	d := w.plan(domain.SyncFull)

	assert.Equal(t, domain.ActionLink, find(d, domain.EntityCategory, "A").Action, "the French name stands in for a missing English one")
	assert.Len(t, d.Warnings, 1)
}

func TestPlanThresholdOfMassRetirement(t *testing.T) {
	assert.False(t, domain.TripsMassRetire(4, 4), "below the minimum")
	assert.False(t, domain.TripsMassRetire(5, 20), "5 of 20 is 25 %")
	assert.True(t, domain.TripsMassRetire(7, 20))
	assert.True(t, domain.TripsMassRetire(5, 5))
}

func TestPlanRefreshesTextsTheImportWroteEarlier(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	w.link(domain.EntityCategory, "A", cat, w.cat.Categories[0].Hash())

	// An adoption and a revival whose local name came from an earlier import and changed in HubRise.
	adopt := w.product("ADOPT", "A", "Nouveau nom", "3")
	la := w.localProduct(cat, "", "Nouveau nom", "3")
	la.Texts = map[string]domain.LocalText{"fr": {Name: "Nouveau nom"}, "en": {Name: "Old", Source: domain.SourceHubrise}}
	_ = adopt
	adopt.Texts["en"] = domain.Text{Name: "New"}
	adopt.Texts["nl"] = domain.Text{} // an empty name is nothing to write
	revive := w.product("REVIVE", "A", "Retour", "3")
	lr := w.localProduct(cat, "", "Retour", "3")
	lr.Texts = hubTxt("Ancien")
	now := time.Now()
	w.link(domain.EntityProduct, "REVIVE", lr.ID, "stale").RetiredAt = &now
	_ = revive

	// A linked group and choice with import names that moved, and a choice that changed its position.
	gp := w.product("RIZ", "A", "Riz", "3")
	lp := w.localProduct(cat, "", "Riz", "3")
	lp.Texts = hubTxt("Riz")
	w.link(domain.EntityProduct, "RIZ", lp.ID, gp.ProductHash())
	ch := choice("RIZ|S|X", "Soja v2", "0")
	ch.Sort = 4
	gp.Groups = []domain.CatalogGroup{group("RIZ|S", "Sauce v2", 0, 1, ch)}
	lg := w.localGroup(lp.ID, "Sauce v1", 0, 1, 0)
	lg.Names = hubTxt("Sauce v1")
	lc := w.localChoice(lg.ID, lp.ID, "Soja v1", "0", 0)
	lc.Names = hubTxt("Soja v1")
	w.link(domain.EntityChoiceGroup, "RIZ|S", lg.ID, "stale")
	w.link(domain.EntityChoice, "RIZ|S|X", lc.ID, "stale")

	d := w.plan(domain.SyncFull)

	assert.Contains(t, find(d, domain.EntityProduct, "ADOPT").Fields, domain.FieldNames)
	assert.Contains(t, find(d, domain.EntityProduct, "REVIVE").Fields, domain.FieldNames)
	assert.Contains(t, find(d, domain.EntityChoiceGroup, "RIZ|S").Fields, domain.FieldNames)
	c := find(d, domain.EntityChoice, "RIZ|S|X")
	assert.ElementsMatch(t, []string{domain.FieldOrder, domain.FieldNames}, c.Fields)
}

func TestPlanReplacesLinksWhoseLocalRowIsGone(t *testing.T) {
	w := newWorld()
	cat := w.localCategory("Plats", 1)
	w.category("A", "Plats")
	w.link(domain.EntityCategory, "A", cat, w.cat.Categories[0].Hash())
	p := w.product("RIZ", "A", "Riz", "3")
	ghost := w.product("GHOST", "A", "Fantome", "3")
	ghost.Groups = []domain.CatalogGroup{group("GHOST|S", "Sauce", 0, 1, choice("GHOST|S|X", "Soja", "0"))}
	w.link(domain.EntityProduct, "GHOST", uuid.New(), "h")
	w.link(domain.EntityChoiceGroup, "GHOST|S", uuid.New(), "h")
	w.link(domain.EntityChoice, "GHOST|S|X", uuid.New(), "h")
	lp := w.localProduct(cat, "", "Riz", "3")
	w.link(domain.EntityProduct, "RIZ", lp.ID, p.ProductHash())
	p.Groups = []domain.CatalogGroup{group("RIZ|S", "Sauce", 0, 1, choice("RIZ|S|X", "Soja", "0"))}
	w.link(domain.EntityChoiceGroup, "RIZ|S", uuid.New(), "h")
	w.link(domain.EntityChoice, "RIZ|S|X", uuid.New(), "h")

	d := w.plan(domain.SyncFull)

	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityProduct, "GHOST").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoiceGroup, "GHOST|S").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoice, "GHOST|S|X").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoiceGroup, "RIZ|S").Action)
	assert.Equal(t, domain.ActionCreate, find(d, domain.EntityChoice, "RIZ|S|X").Action)
}
