package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func puid(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("%08d-0000-0000-0000-000000000000", n)) }

func ptexts(fr, en string) map[string]domain.LocalText {
	m := map[string]domain.LocalText{"fr": {Name: fr}}
	if en != "" {
		m["en"] = domain.LocalText{Name: en}
	}
	return m
}

func pdec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// pubWorld is a local shop, its links, and a mock HubRise: publish() plays what the publisher does around
// the planner (store the refs, PUT, remember what was published).
type pubWorld struct {
	t         *testing.T
	local     *domain.LocalSnapshot
	links     []domain.CatalogLink
	published []domain.PublishedItem
	srv       *hubrisetest.Server
	client    *hubrise.Client
	settings  application.PublishSettings
	nextLink  int64
}

func newPubWorld(t *testing.T) *pubWorld {
	srv := hubrisetest.New(t)
	// An empty HubRise catalog: the seed menu is somebody else's business in these tests.
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))
	w := &pubWorld{t: t, srv: srv, client: srv.NewClient(t), local: &domain.LocalSnapshot{}, settings: application.PublishSettings{Language: "fr"}}
	return w
}

func (w *pubWorld) category(n int, slug string, order int, fr, en string) uuid.UUID {
	w.local.Categories = append(w.local.Categories, domain.LocalCategory{ID: puid(n), Slug: slug, Order: order, Names: ptexts(fr, en)})
	return puid(n)
}

func (w *pubWorld) product(n int, cat uuid.UUID, code, price, vat string, visible bool, fr, en string) uuid.UUID {
	w.local.Products = append(w.local.Products, domain.LocalProduct{ID: puid(n), CategoryID: cat, Code: code, Price: pdec(price), VatCategory: vat, Visible: visible, Available: true, Texts: ptexts(fr, en)})
	return puid(n)
}

func (w *pubWorld) group(n int, product uuid.UUID, min, max, sort int, fr string) uuid.UUID {
	w.local.Groups = append(w.local.Groups, domain.LocalGroup{ID: puid(n), ProductID: product, Min: min, Max: max, Sort: sort, Names: ptexts(fr, "")})
	return puid(n)
}

func (w *pubWorld) choice(n int, product, group uuid.UUID, price string, sort int, fr string) uuid.UUID {
	w.local.Choices = append(w.local.Choices, domain.LocalChoice{ID: puid(n), ProductID: product, GroupID: group, PriceModifier: pdec(price), Sort: sort, Names: ptexts(fr, "")})
	return puid(n)
}

func (w *pubWorld) base() json.RawMessage {
	got, err := w.client.GetCatalogRaw(context.Background(), hubrisetest.CatalogID)
	require.NoError(w.t, err)
	return got.Data
}

func (w *pubWorld) input() application.PublishInput {
	return application.PublishInput{Local: w.local, Links: w.links, Published: w.published, Settings: w.settings, Base: w.base()}
}

func (w *pubWorld) plan() *application.PublishPlan {
	p, err := application.PlanPublish(w.input())
	require.NoError(w.t, err)
	return p
}

// publish plans and, when the plan is publishable, stores the refs, PUTs and records the published items.
func (w *pubWorld) publish() *application.PublishPlan {
	w.t.Helper()
	p := w.plan()
	require.Empty(w.t, p.Blockers, "blockers: %+v", p.Blockers)
	w.apply(p)
	return p
}

func (w *pubWorld) apply(p *application.PublishPlan) {
	w.t.Helper()
	for _, id := range p.Unretire {
		for i := range w.links {
			if w.links[i].ID == id {
				w.links[i].RetiredAt = nil
			}
		}
	}
	for _, nl := range p.NewLinks {
		l := domain.CatalogLink{ID: w.nextLink + 1000, EntityType: nl.Entity, HubriseKey: nl.Key, HubriseRef: nl.Ref, Origin: domain.OriginPublished}
		w.nextLink++
		l.SetLocalID(nl.LocalID)
		w.links = append(w.links, l)
	}
	for _, rk := range p.Rekeys {
		for i := range w.links {
			if w.links[i].ID == rk.LinkID {
				w.links[i].HubriseKey, w.links[i].HubriseRef = rk.NewKey, rk.NewRef
			}
		}
	}
	if p.NeedsPut {
		_, err := w.client.ReplaceCatalog(context.Background(), hubrisetest.CatalogID, p.Data)
		require.NoError(w.t, err)
	}
	w.published = nil
	for _, d := range p.Desired {
		w.published = append(w.published, domain.PublishedItem{EntityType: d.Entity, HubriseKey: d.Key, HubriseRef: d.Ref, ContentHash: d.Hash})
	}
}

// remote reads the mock catalog as generic JSON.
func (w *pubWorld) remote() map[string]any {
	var out map[string]any
	require.NoError(w.t, json.Unmarshal(w.srv.CatalogData(), &out))
	return out
}

func (w *pubWorld) typed() *hubrise.Catalog {
	c, err := w.client.GetCatalog(context.Background(), hubrisetest.CatalogID)
	require.NoError(w.t, err)
	return c
}

// roundTrip imports the published catalog and checks it gives the local menu back.
func (w *pubWorld) roundTrip() {
	w.t.Helper()
	neutral := application.MapCatalog(w.typed(), application.MapOptions{Language: w.settings.Language, VariantRef: w.settings.VariantRef})
	byKey := map[string]domain.CatalogProduct{}
	for _, p := range neutral.Products {
		byKey[p.Key] = p
	}
	for i := range w.local.Products {
		lp := &w.local.Products[i]
		var link *domain.CatalogLink
		for j := range w.links {
			if w.links[j].EntityType == domain.EntityProduct && w.links[j].LocalID() == lp.ID {
				link = &w.links[j]
			}
		}
		require.NotNil(w.t, link, "product %s has a link", lp.ID)
		cp, ok := byKey[link.HubriseKey]
		require.True(w.t, ok, "the importer finds SKU %s", link.HubriseKey)
		assert.True(w.t, lp.Price.Equal(cp.Price), "price of %s", link.HubriseKey)
		assert.Equal(w.t, lp.Code, cp.Code, "code of %s", link.HubriseKey)
		assert.Equal(w.t, lp.VatCategory, cp.VatCategory, "vat of %s", link.HubriseKey)
		assert.Equal(w.t, lp.Visible, cp.Visible, "visibility of %s", link.HubriseKey)
		for lang, t := range lp.Texts {
			if t.Name == "" {
				continue
			}
			assert.Equal(w.t, t.Name, cp.Texts[lang].Name, "%s name in %s", link.HubriseKey, lang)
			assert.Equal(w.t, t.Description, cp.Texts[lang].Description, "%s description in %s", link.HubriseKey, lang)
		}
		var groups []domain.LocalGroup
		for _, g := range w.local.Groups {
			if g.ProductID == lp.ID {
				groups = append(groups, g)
			}
		}
		require.Len(w.t, cp.Groups, len(groups), "groups of %s", link.HubriseKey)
		for gi, g := range groups {
			cg := cp.Groups[gi]
			assert.Equal(w.t, g.Min, cg.Min)
			assert.Equal(w.t, g.Max, cg.Max)
			assert.Equal(w.t, g.Names["fr"].Name, cg.Names["fr"])
			var chs []domain.LocalChoice
			for _, c := range w.local.Choices {
				if c.GroupID == g.ID {
					chs = append(chs, c)
				}
			}
			require.Len(w.t, cg.Choices, len(chs))
			for ci, c := range chs {
				assert.Equal(w.t, c.Names["fr"].Name, cg.Choices[ci].Names["fr"])
				assert.True(w.t, c.PriceModifier.Equal(cg.Choices[ci].PriceModifier))
			}
		}
	}
	// Categories with their translations.
	cats := map[string]domain.CatalogCategory{}
	for _, c := range neutral.Categories {
		cats[c.Key] = c
	}
	for _, lc := range w.local.Categories {
		for _, l := range w.links {
			if l.EntityType == domain.EntityCategory && l.LocalID() == lc.ID {
				if len(lc.Names) == 0 {
					continue
				}
				if c, ok := cats[l.HubriseKey]; ok {
					for lang, t := range lc.Names {
						assert.Equal(w.t, t.Name, c.Names[lang], "category %s in %s", l.HubriseKey, lang)
					}
				}
			}
		}
	}
}

func (w *pubWorld) skuRef(id uuid.UUID) string {
	for _, l := range w.links {
		if l.EntityType == domain.EntityProduct && l.LocalID() == id {
			return l.HubriseRef
		}
	}
	return ""
}

// menu builds the shop used by most tests: two categories, four products, a sauce group.
func (w *pubWorld) menu() (makis, drinks, salmon, cola, hidden, withSauce uuid.UUID) {
	makis = w.category(1, "makis", 1, "Makis", "Makis EN")
	drinks = w.category(2, "boissons", 2, "Boissons", "")
	salmon = w.product(10, makis, "M1", "8.90", "food", true, "Maki saumon", "Salmon maki")
	cola = w.product(11, drinks, "", "2.50", "beverage", true, "Cola", "")
	hidden = w.product(12, makis, "H9", "5.00", "food", false, "Maki secret", "")
	withSauce = w.product(13, makis, "S1", "7.00", "food", true, "Maki sauce", "")
	g := w.group(20, withSauce, 0, 1, 0, "Sauce")
	w.choice(30, withSauce, g, "0.00", 0, "Soja")
	w.choice(31, withSauce, g, "0.50", 1, "Soja sucree")
	return
}

func TestPlanRefusesAnEmptyMenu(t *testing.T) {
	_, err := application.PlanPublish(application.PublishInput{Local: &domain.LocalSnapshot{}, Base: json.RawMessage(`{}`)})
	assert.ErrorIs(t, err, domain.ErrEmptyMenu)
	_, err = application.PlanPublish(application.PublishInput{})
	assert.ErrorIs(t, err, domain.ErrEmptyMenu)
	w := newPubWorld(t)
	w.category(1, "c", 1, "C", "")
	_, err = application.PlanPublish(w.input())
	assert.ErrorIs(t, err, domain.ErrEmptyMenu, "categories alone are not a menu")
}

func TestPlanRefusesAnUnreadableBase(t *testing.T) {
	w := newPubWorld(t)
	w.menu()
	in := w.input()
	in.Base = json.RawMessage(`[1,2]`)
	_, err := application.PlanPublish(in)
	assert.Error(t, err)
	in.Base = json.RawMessage(`{`)
	_, err = application.PlanPublish(in)
	assert.Error(t, err)
	in.Base = nil
	p, err := application.PlanPublish(in)
	require.NoError(t, err, "an empty base is an empty catalog")
	assert.True(t, p.NeedsPut)
}

func TestFirstPublishWritesTheWholeMenuAndTheImporterReadsItBack(t *testing.T) {
	w := newPubWorld(t)
	_, _, salmon, cola, hidden, sauce := w.menu()
	p := w.publish()

	assert.Equal(t, 4+2+1+2, p.Count(application.ActionPublishAdd), "2 categories, 4 products, 1 group, 2 choices")
	assert.Zero(t, p.Count(application.ActionPublishRemove))
	assert.Zero(t, p.Count(application.ActionPublishUpdate))
	assert.True(t, p.NeedsPut)
	assert.NotEmpty(t, p.PlanHash)
	assert.NotEmpty(t, p.LocalMenuHash)

	// Refs: the code when set, p-<id> otherwise.
	assert.Equal(t, "M1", w.skuRef(salmon))
	assert.Equal(t, "p-"+puid(11).String()[:8], w.skuRef(cola))
	assert.Equal(t, "H9", w.skuRef(hidden))
	assert.Equal(t, "S1", w.skuRef(sauce))
	assert.Equal(t, p.SkuRefs[salmon], "M1")

	// The EPOS reads custom_fields.code.
	rem := w.remote()
	var found bool
	for _, pr := range rem["products"].([]any) {
		for _, s := range pr.(map[string]any)["skus"].([]any) {
			sk := s.(map[string]any)
			if sk["ref"] == "M1" {
				found = true
				assert.Equal(t, "M1", sk["custom_fields"].(map[string]any)["code"])
				assert.Equal(t, "8.90 EUR", sk["price"])
			}
		}
	}
	assert.True(t, found)
	w.roundTrip()
}

func TestPublishIsIdempotent(t *testing.T) {
	w := newPubWorld(t)
	w.menu()
	w.publish()
	again := w.plan()
	assert.False(t, again.NeedsPut, "a second plan finds nothing to write")
	assert.Empty(t, again.Changes)
	assert.Equal(t, 4+2+1+2, again.Unchanged)
	assert.Empty(t, again.NewLinks)
	assert.Empty(t, again.Rekeys)
	// And again after HubRise gave every element new ids.
	w.apply(again)
	_, err := w.client.ReplaceCatalog(context.Background(), hubrisetest.CatalogID, w.plan().Data)
	require.NoError(t, err)
	assert.False(t, w.plan().NeedsPut)
	assert.Equal(t, again.RemoteCatalogHash, w.plan().RemoteCatalogHash, "ids do not move the remote hash")
	assert.Equal(t, again.LocalMenuHash, w.plan().LocalMenuHash)
}

func TestEditsAreReportedFieldByField(t *testing.T) {
	w := newPubWorld(t)
	_, drinks, salmon, cola, hidden, sauce := w.menu()
	w.publish()

	for i := range w.local.Products {
		switch w.local.Products[i].ID {
		case salmon:
			w.local.Products[i].Price = pdec("9.50")
			w.local.Products[i].Texts["fr"] = domain.LocalText{Name: "Maki saumon XL"}
		case hidden:
			w.local.Products[i].Visible = true
		case cola:
			w.local.Products[i].Code = "C1"
			w.local.Products[i].CategoryID = drinks
			w.local.Products[i].VatCategory = "zero_rated"
		case sauce:
			w.local.Products[i].Texts["en"] = domain.LocalText{Name: "Sauce maki", Description: "tasty"}
		}
	}
	w.local.Choices[0].Names["fr"] = domain.LocalText{Name: "Soja salé"}
	w.local.Choices[1].PriceModifier = pdec("0.80")

	p := w.plan()
	byKey := map[string]application.PublishChange{}
	for _, c := range p.Changes {
		byKey[string(c.Entity)+":"+c.Key] = c
	}
	assert.ElementsMatch(t, []string{"name", "price", "translations"}, byKey["product:M1"].Fields)
	assert.Equal(t, []string{"visibility"}, byKey["product:H9"].Fields)
	assert.ElementsMatch(t, []string{"code", "vatCategory"}, byKey["product:"+w.skuRef(cola)].Fields)
	assert.Equal(t, []string{"translations"}, byKey["product:S1"].Fields, "only the French text is the base text")
	assert.Equal(t, []string{"name"}, byKey["choice:S1|g-"+puid(20).String()[:8]+"|o-"+puid(30).String()[:8]].Fields)
	assert.Equal(t, []string{"price"}, byKey["choice:S1|g-"+puid(20).String()[:8]+"|o-"+puid(31).String()[:8]].Fields)
	w.apply(p)
	w.roundTrip()
	assert.False(t, w.plan().NeedsPut)
}

func TestRefsAreStableAndCollisionFree(t *testing.T) {
	w := newPubWorld(t)
	makis := w.category(1, "makis", 1, "Makis", "")
	a := w.product(10, makis, "DUP", "1.00", "food", true, "A", "")
	b := w.product(11, makis, "dup", "1.00", "food", true, "B", "")
	c := w.product(12, makis, "bad code!", "1.00", "food", true, "C", "")
	d := w.product(13, makis, "", "1.00", "food", true, "D", "")
	w.publish()
	assert.Equal(t, "DUP", w.skuRef(a))
	assert.Equal(t, "p-"+puid(11).String()[:8], w.skuRef(b), "a code that differs only by case is a collision")
	assert.Equal(t, "p-"+puid(12).String()[:8], w.skuRef(c), "a code that is not a safe ref falls back to the id")
	assert.Equal(t, "p-"+puid(13).String()[:8], w.skuRef(d))

	// The ref never changes afterwards, even when the code does; the code travels in the custom field.
	w.local.Products[0].Code = "OTHER"
	w.publish()
	assert.Equal(t, "DUP", w.skuRef(a))
	for _, pr := range w.remote()["products"].([]any) {
		for _, s := range pr.(map[string]any)["skus"].([]any) {
			if sk := s.(map[string]any); sk["ref"] == "DUP" {
				assert.Equal(t, "OTHER", sk["custom_fields"].(map[string]any)["code"])
			}
		}
	}

	// A new code that is taken by a foreign SKU of HubRise gets the id ref.
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(`{"categories":[{"ref":"makis","name":"Makis"}],"products":[{"ref":"TAKEN","category_ref":"makis","name":"Foreign","skus":[{"ref":"TAKEN","price":"1.00 EUR"}]}]}`)))
	e := w.product(14, makis, "TAKEN", "1.00", "food", true, "E", "")
	w.links = nil
	w.published = nil
	w.publish()
	assert.Equal(t, "p-"+puid(14).String()[:8], w.skuRef(e))
	// A category slug that collides with a foreign category ref.
	assert.Equal(t, "c-"+puid(1).String()[:8], w.catRef(makis), "the slug 'makis' is taken by a foreign category")
}

func (w *pubWorld) catRef(id uuid.UUID) string {
	for _, l := range w.links {
		if l.EntityType == domain.EntityCategory && l.LocalID() == id {
			return l.HubriseRef
		}
	}
	return ""
}

func TestDeletedLocalItemsAreRemovedAndForeignOnesKept(t *testing.T) {
	w := newPubWorld(t)
	_, _, salmon, cola, _, _ := w.menu()
	w.publish()

	// Somebody adds a product in the EPOS menu editor.
	data := w.remote()
	_ = data
	require.NoError(t, w.srv.ReplaceCatalogRaw(addForeignProduct(t, w.srv.CatalogData())))

	// The shop deletes two products (their links cascade away, the published items remain).
	w.local.Products = slices.DeleteFunc(w.local.Products, func(p domain.LocalProduct) bool { return p.ID == salmon || p.ID == cola })
	w.links = slices.DeleteFunc(w.links, func(l domain.CatalogLink) bool {
		return l.EntityType == domain.EntityProduct && (l.LocalID() == salmon || l.LocalID() == cola)
	})
	p := w.plan()
	removed := 0
	for _, c := range p.Changes {
		if c.Action == application.ActionPublishRemove && c.Entity == domain.EntityProduct {
			removed++
		}
	}
	assert.Equal(t, 2, removed)
	assert.Equal(t, 2, p.RemovedSkus)
	require.Len(t, p.Foreign, 1)
	assert.Equal(t, "EPOS-ONLY", p.Foreign[0].Ref)
	assert.False(t, p.RemovalsNeedConfirm, "2 removals are below the threshold")
	w.apply(p)
	skus := remoteSkus(w.remote())
	assert.NotContains(t, skus, "M1")
	assert.Contains(t, skus, "EPOS-ONLY", "a foreign product is left alone")
	assert.Contains(t, skus, "H9")

	// removeForeign takes it away too.
	in := w.input()
	in.RemoveForeign = true
	p2, err := application.PlanPublish(in)
	require.NoError(t, err)
	assert.Equal(t, 1, p2.RemovedSkus)
	assert.True(t, p2.NeedsPut)
}

func addForeignProduct(t *testing.T, data json.RawMessage) json.RawMessage {
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	cats := m["categories"].([]any)
	catRef := cats[0].(map[string]any)["id"]
	_ = catRef
	m["products"] = append(m["products"].([]any), map[string]any{
		"category_id": cats[0].(map[string]any)["id"], "name": "EPOS only", "ref": "EPOS-ONLY",
		"skus": []any{map[string]any{"ref": "EPOS-ONLY", "price": "3.00 EUR"}},
	})
	out, err := json.Marshal(m)
	require.NoError(t, err)
	// The control API takes the upload form: turn ids back into refs.
	var up map[string]any
	require.NoError(t, json.Unmarshal(out, &up))
	idToRef := map[string]string{}
	for _, c := range up["categories"].([]any) {
		cm := c.(map[string]any)
		idToRef[cm["id"].(string)] = cm["ref"].(string)
	}
	listID := map[string]string{}
	if lists, ok := up["option_lists"].([]any); ok {
		for _, l := range lists {
			lm := l.(map[string]any)
			listID[lm["id"].(string)] = lm["ref"].(string)
		}
	}
	for _, c := range up["categories"].([]any) {
		delete(c.(map[string]any), "parent_id")
	}
	for _, pr := range up["products"].([]any) {
		pm := pr.(map[string]any)
		pm["category_ref"] = idToRef[pm["category_id"].(string)]
		delete(pm, "category_id")
		for _, s := range pm["skus"].([]any) {
			sm := s.(map[string]any)
			if ids, ok := sm["option_list_ids"].([]any); ok {
				var refs []any
				for _, id := range ids {
					refs = append(refs, listID[id.(string)])
				}
				sm["option_list_refs"] = refs
				delete(sm, "option_list_ids")
			}
		}
	}
	for _, l := range asList(up["option_lists"]) {
		delete(l, "type")
	}
	b, err := json.Marshal(up)
	require.NoError(t, err)
	return b
}

func asList(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

func remoteSkus(m map[string]any) []string {
	var out []string
	for _, pr := range asList(m["products"]) {
		for _, s := range asList(pr["skus"]) {
			out = append(out, s["ref"].(string))
		}
	}
	return out
}

const richBase = `{
  "x_top": {"keep": [1, 2.50, "a"]},
  "categories": [
    {"ref": "makis", "name": "Makis", "x_cat": "kept"},
    {"ref": "epos", "name": "EPOS only"}
  ],
  "option_lists": [
    {"ref": "sauces", "name": "Sauces", "min_selections": 0, "max_selections": 1, "x_list": {"a": 1},
     "options": [{"ref": "soja", "name": "Soja", "price": "0.00 EUR", "x_opt": true}]}
  ],
  "products": [
    {"ref": "M1", "category_ref": "makis", "name": "Old name", "x_prod": {"deep": [true]},
     "skus": [{"ref": "M1", "price": "1.00 EUR", "x_sku": 7, "option_list_refs": ["sauces"],
               "custom_fields": {"foreign": "yes", "code": "OLD"}}]},
    {"ref": "EPOS-ONLY", "category_ref": "epos", "name": "Epos", "skus": [{"ref": "EPOS-ONLY", "price": "3.00 EUR"}]}
  ],
  "deals": [{"ref": "d1", "name": "Menu",
             "lines": [{"label": "Maki", "pricing_effect": "fixed_price", "pricing_value": "9.00 EUR", "skus": [{"ref": "M1"}]}]}],
  "discounts": [{"ref": "disc", "name": "Promo", "pricing_effect": "price_off", "pricing_value": "1.00 EUR"}],
  "charges": [{"ref": "ch", "name": "Pack", "type": "other", "price": "0.20 EUR"}]
}`

func TestUnmodeledFieldsSurviveThePublish(t *testing.T) {
	w := newPubWorld(t)
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(richBase)))
	makis := w.category(1, "makis", 1, "Makis", "")
	p := w.product(10, makis, "M1", "8.90", "food", true, "New name", "")
	// Link the local entities to the refs HubRise already has, as an earlier import would have.
	cl := domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory, HubriseKey: "makis", HubriseRef: "makis", Origin: domain.OriginImport}
	cl.SetLocalID(makis)
	pl := domain.CatalogLink{ID: 2, EntityType: domain.EntityProduct, HubriseKey: "M1", HubriseRef: "M1", Origin: domain.OriginImport}
	pl.SetLocalID(p)
	w.links = []domain.CatalogLink{cl, pl}
	plan := w.publish()
	assert.Equal(t, 1, plan.Count(application.ActionPublishUpdate)+0*plan.Count(application.ActionPublishAdd), "only the product moved")

	got := w.remote()
	assert.Equal(t, map[string]any{"keep": []any{float64(1), 2.5, "a"}}, normNums(got["x_top"]))
	assert.NotEmpty(t, got["deals"])
	assert.NotEmpty(t, got["discounts"])
	assert.NotEmpty(t, got["charges"])
	prod := asList(got["products"])[0]
	assert.Equal(t, "New name", prod["name"])
	assert.NotNil(t, prod["x_prod"], "unknown product key kept")
	sku := asList(prod["skus"])[0]
	assert.Equal(t, "8.90 EUR", sku["price"])
	assert.NotNil(t, sku["x_sku"])
	cf := sku["custom_fields"].(map[string]any)
	assert.Equal(t, "yes", cf["foreign"], "a foreign custom field is not ours to drop")
	assert.Equal(t, "M1", cf["code"])
	assert.Equal(t, "kept", asList(got["categories"])[0]["x_cat"])
	l := asList(got["option_lists"])[0]
	assert.NotNil(t, l["x_list"])
	assert.NotNil(t, asList(l["options"])[0]["x_opt"])
	assert.Contains(t, remoteSkus(got), "EPOS-ONLY", "a foreign product stays")
	assert.Len(t, plan.Foreign, 3, "a category, an option list and a product are not ours")
	assert.Len(t, asList(got["categories"]), 2, "a foreign category stays")
}

func normNums(v any) any { return v }

func TestAProductInADealIsRetainedWhenItsLocalTwinIsDeleted(t *testing.T) {
	w := newPubWorld(t)
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(richBase)))
	makis := w.category(1, "makis", 1, "Makis", "")
	keep := w.product(11, makis, "K1", "2.00", "food", true, "Keep", "")
	gone := w.product(10, makis, "M1", "8.90", "food", true, "Gone", "")
	cl := domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory, HubriseKey: "makis", HubriseRef: "makis", Origin: domain.OriginImport}
	cl.SetLocalID(makis)
	pl := domain.CatalogLink{ID: 2, EntityType: domain.EntityProduct, HubriseKey: "M1", HubriseRef: "M1", Origin: domain.OriginImport}
	pl.SetLocalID(gone)
	w.links = []domain.CatalogLink{cl, pl}
	w.publish()
	// Delete it locally: the deal still uses M1.
	w.local.Products = slices.DeleteFunc(w.local.Products, func(p domain.LocalProduct) bool { return p.ID == gone })
	w.links = slices.DeleteFunc(w.links, func(l domain.CatalogLink) bool { return l.EntityType == domain.EntityProduct && l.LocalID() == gone })
	_ = keep
	plan := w.plan()
	var warned bool
	for _, wr := range plan.Warnings {
		if wr.Code == domain.WarnRetainedByDeal {
			warned = true
		}
	}
	assert.True(t, warned, "warnings: %+v", plan.Warnings)
	w.apply(plan)
	assert.Contains(t, remoteSkus(w.remote()), "M1", "the deal needs it")
	assert.NotEmpty(t, w.remote()["deals"])
}

func TestBlockersStopAnInvalidCatalog(t *testing.T) {
	w2 := newPubWorld(t)
	m2 := w2.category(1, "makis", 1, "Makis", "")
	prod := w2.product(10, m2, "", "1.00", "food", true, "P", "")
	g := w2.group(20, prod, 0, 1, 0, "G")
	_ = g
	p2 := w2.plan()
	var c2 []domain.WarningCode
	for _, b := range p2.Blockers {
		c2 = append(c2, b.Code)
	}
	assert.Contains(t, c2, domain.BlockEmptyOptionList, "a group without choices cannot be uploaded")
}

func TestHashesAreStableAndSensitive(t *testing.T) {
	w := newPubWorld(t)
	w.menu()
	a := w.plan()
	b := w.plan()
	assert.Equal(t, a.PlanHash, b.PlanHash)
	assert.Equal(t, a.LocalMenuHash, b.LocalMenuHash)

	before := application.LocalMenuHash(w.local, w.settings)
	w.local.Products[0].Available = false
	assert.Equal(t, before, application.LocalMenuHash(w.local, w.settings), "availability is not published")
	w.local.Products[0].Price = pdec("9.99")
	assert.NotEqual(t, before, application.LocalMenuHash(w.local, w.settings))
	w.local.Products[0].Price = pdec("8.90")
	assert.Equal(t, before, application.LocalMenuHash(w.local, w.settings))
	s := w.settings
	s.Language = "en"
	assert.NotEqual(t, before, application.LocalMenuHash(w.local, s), "the language setting changes the output")

	w.local.Products = w.local.Products[:2]
	assert.NotEqual(t, a.PlanHash, w.plan().PlanHash, "another set of items is another plan")
}

func TestRemoteHashIgnoresIdsAndNulls(t *testing.T) {
	h1 := application.RemoteCatalogHash(json.RawMessage(`{"categories":[{"id":"abc","ref":"c","name":"C","parent_id":null}]}`))
	h2 := application.RemoteCatalogHash(json.RawMessage(`{"categories":[{"id":"zzz","ref":"c","name":"C"}]}`))
	h3 := application.RemoteCatalogHash(json.RawMessage(`{"categories":[{"id":"zzz","ref":"c","name":"D"}]}`))
	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h2, h3)
}

func TestPriceOverrideTheImporterReadsIsPublished(t *testing.T) {
	w := newPubWorld(t)
	w.settings.VariantRef = "delivery"
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(`{
	  "variants":[{"ref":"delivery","name":"Delivery"}],
	  "categories":[{"ref":"makis","name":"Makis"}],
	  "products":[{"ref":"M1","category_ref":"makis","name":"Maki","skus":[{"ref":"M1","price":"5.00 EUR",
	    "price_overrides":[
	      {"price":"4.00 EUR","dow":"1"},
	      {"price":"6.00 EUR","variant_refs":["delivery"]}]}]}]}`)))
	makis := w.category(1, "makis", 1, "Makis", "")
	id := w.product(10, makis, "M1", "8.90", "food", true, "Maki", "")
	cl := domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory, HubriseKey: "makis", HubriseRef: "makis", Origin: domain.OriginImport}
	cl.SetLocalID(makis)
	pl := domain.CatalogLink{ID: 2, EntityType: domain.EntityProduct, HubriseKey: "M1", HubriseRef: "M1", Origin: domain.OriginImport}
	pl.SetLocalID(id)
	w.links = []domain.CatalogLink{cl, pl}
	w.publish()
	sku := asList(asList(w.remote()["products"])[0]["skus"])[0]
	assert.Equal(t, "5.00 EUR", sku["price"], "the base price is not the effective one")
	ov := asList(sku["price_overrides"])
	require.Len(t, ov, 2)
	assert.Equal(t, "4.00 EUR", ov[0]["price"], "a time rule is left alone")
	assert.Equal(t, "8.90 EUR", ov[1]["price"])
	w.roundTrip()
	assert.False(t, w.plan().NeedsPut)
}

func TestMultiSkuProductsSplitTheName(t *testing.T) {
	w := newPubWorld(t)
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(`{
	  "categories":[{"ref":"drinks","name":"Drinks"}],
	  "products":[{"ref":"COLA","category_ref":"drinks","name":"Cola","skus":[
	     {"ref":"COLA-33","name":"33cl","price":"2.00 EUR"},
	     {"ref":"COLA-50","name":"50cl","price":"3.00 EUR"}]}]}`)))
	drinks := w.category(1, "drinks", 1, "Drinks", "")
	a := w.product(10, drinks, "COLA-33", "2.20", "beverage", true, "Cola 33cl", "")
	b := w.product(11, drinks, "COLA-50", "3.00", "beverage", true, "Cola 50cl", "")
	cl := domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory, HubriseKey: "drinks", HubriseRef: "drinks", Origin: domain.OriginImport}
	cl.SetLocalID(drinks)
	la := domain.CatalogLink{ID: 2, EntityType: domain.EntityProduct, HubriseKey: "COLA-33", HubriseRef: "COLA-33", Origin: domain.OriginImport}
	la.SetLocalID(a)
	lb := domain.CatalogLink{ID: 3, EntityType: domain.EntityProduct, HubriseKey: "COLA-50", HubriseRef: "COLA-50", Origin: domain.OriginImport}
	lb.SetLocalID(b)
	w.links = []domain.CatalogLink{cl, la, lb}
	w.publish()
	prod := asList(w.remote()["products"])[0]
	assert.Equal(t, "Cola", prod["name"])
	skus := asList(prod["skus"])
	assert.Equal(t, "33cl", skus[0]["name"])
	assert.Equal(t, "2.20 EUR", skus[0]["price"])
	assert.Equal(t, "50cl", skus[1]["name"])
	w.roundTrip()
	assert.False(t, w.plan().NeedsPut)
}

func TestACommonOptionListIsSplitPerProduct(t *testing.T) {
	w := newPubWorld(t)
	require.NoError(t, w.srv.ReplaceCatalogRaw(json.RawMessage(`{
	  "categories":[{"ref":"makis","name":"Makis"}],
	  "option_lists":[{"ref":"sauces","name":"Sauce","min_selections":0,"max_selections":1,
	     "options":[{"ref":"soja","name":"Soja","price":"0.00 EUR"}]}],
	  "products":[
	   {"ref":"A","category_ref":"makis","name":"A","skus":[{"ref":"A","price":"1.00 EUR","option_list_refs":["sauces"]}]},
	   {"ref":"B","category_ref":"makis","name":"B","skus":[{"ref":"B","price":"1.00 EUR","option_list_refs":["sauces"]}]}]}`)))
	makis := w.category(1, "makis", 1, "Makis", "")
	a := w.product(10, makis, "A", "1.00", "food", true, "A", "")
	b := w.product(11, makis, "B", "1.00", "food", true, "B", "")
	ga := w.group(20, a, 0, 1, 0, "Sauce")
	gb := w.group(21, b, 0, 1, 0, "Sauce")
	w.choice(30, a, ga, "0.00", 0, "Soja")
	w.choice(31, b, gb, "0.00", 0, "Soja")
	w.choice(32, b, gb, "0.30", 1, "Wasabi")
	cl := domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory, HubriseKey: "makis", HubriseRef: "makis", Origin: domain.OriginImport}
	cl.SetLocalID(makis)
	la := domain.CatalogLink{ID: 2, EntityType: domain.EntityProduct, HubriseKey: "A", HubriseRef: "A", Origin: domain.OriginImport}
	la.SetLocalID(a)
	lb := domain.CatalogLink{ID: 3, EntityType: domain.EntityProduct, HubriseKey: "B", HubriseRef: "B", Origin: domain.OriginImport}
	lb.SetLocalID(b)
	// The importer links a shared list to each product through the composite key.
	lga := domain.CatalogLink{ID: 4, EntityType: domain.EntityChoiceGroup, HubriseKey: "A|sauces", HubriseRef: "sauces", Origin: domain.OriginImport}
	lga.SetLocalID(ga)
	lgb := domain.CatalogLink{ID: 5, EntityType: domain.EntityChoiceGroup, HubriseKey: "B|sauces", HubriseRef: "sauces", Origin: domain.OriginImport}
	lgb.SetLocalID(gb)
	chs := []domain.CatalogLink{
		{ID: 6, EntityType: domain.EntityChoice, HubriseKey: "A|sauces|soja", HubriseRef: "soja", Origin: domain.OriginImport},
		{ID: 7, EntityType: domain.EntityChoice, HubriseKey: "B|sauces|soja", HubriseRef: "soja", Origin: domain.OriginImport},
	}
	chs[0].SetLocalID(puid(30))
	chs[1].SetLocalID(puid(31))
	w.links = append([]domain.CatalogLink{cl, la, lb, lga, lgb}, chs...)
	plan := w.publish()
	assert.NotEmpty(t, plan.Rekeys, "one product keeps the shared list, the other gets its own")
	lists := asList(w.remote()["option_lists"])
	assert.Len(t, lists, 2)
	w.roundTrip()
	assert.False(t, w.plan().NeedsPut)
}
