package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/hubrisewire"
	"tsb-service/internal/modules/hubrise/application"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/db"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/pubsub"
)

// What the restaurant already keeps in HubRise before the dashboard takes over the menu: a product
// the EPOS owns (with a price rule and a field of the EPOS), a deal that sells it, and a key of the
// catalog the shop has never heard of.
const eposCatalog = `{
  "x_top": {"keep": [1, 2]},
  "categories": [{"ref": "epos-cat", "name": "EPOS only"}],
  "products": [{"ref": "EPOS-ONLY", "category_ref": "epos-cat", "name": "Epos special",
    "skus": [{"ref": "EPOS-ONLY", "price": "3.00 EUR", "x_sku": 7,
      "price_overrides": [{"price": "2.50 EUR", "dow": "1"}],
      "custom_fields": {"foreign": "yes"}}]}],
  "deals": [{"ref": "d1", "name": "Menu",
    "lines": [{"label": "Special", "pricing_effect": "fixed_price", "pricing_value": "9.00 EUR", "skus": [{"ref": "EPOS-ONLY"}]}]}]
}`

const catalogPath = "/v1/catalogs/"

func (r *e2eRig) pubDirection(direction string) publishStatusDoc {
	r.t.Helper()
	return decode[publishStatusDoc](r.t, r.mustAsk(directionDoc, map[string]any{"d": direction})["setHubriseCatalogDirection"])
}

func (r *e2eRig) pubStatus() publishStatusDoc {
	r.t.Helper()
	return decode[publishStatusDoc](r.t, r.mustAsk(`{ hubriseStatus { `+publishStatusFields+` } }`, nil)["hubriseStatus"])
}

func (r *e2eRig) pubPreview() previewDocT {
	r.t.Helper()
	return decode[previewDocT](r.t, r.mustAsk(previewDoc, map[string]any{"removeForeign": false})["previewHubriseCatalogPublish"])
}

func (r *e2eRig) pubPublish(confirmRemovals bool, planHash string) publishResultDoc {
	r.t.Helper()
	return decode[publishResultDoc](r.t, r.mustAsk(publishDoc, map[string]any{"c": confirmRemovals, "f": false, "h": planHash})["publishHubriseCatalog"])
}

func (r *e2eRig) pubExec(query string, args ...any) {
	r.t.Helper()
	_, err := r.DB.DB.ExecContext(r.t.Context(), query, args...)
	require.NoError(r.t, err)
}

// pubDue plays the passing of the debounce: the claim only looks at next_attempt_at.
func (r *e2eRig) pubDue() {
	r.t.Helper()
	r.pubExec(`UPDATE hubrise_catalog_publish SET next_attempt_at = now() - interval '1 second', last_started_at = NULL`)
}

func (r *e2eRig) puts() int { return r.srv.CatalogPuts() }
func (r *e2eRig) gets() int { return len(r.srv.RequestsTo(http.MethodGet, catalogPath)) }

// pubRuns lets the due publish run (the workers of the rig tick every 5 s, this does not wait for
// them) and waits until the lease is released.
func (r *e2eRig) pubRuns(wantPuts int) {
	r.t.Helper()
	r.pubDue()
	require.Eventually(r.t, func() bool {
		_, err := r.rt.Catalog.Publisher.Tick(r.t.Context())
		require.NoError(r.t, err)
		return r.puts() >= wantPuts
	}, 20*time.Second, 25*time.Millisecond, "the publish never ran")
	r.pubSettled()
	assert.Equal(r.t, wantPuts, r.puts())
}

func (r *e2eRig) pubSettled() {
	r.t.Helper()
	require.Eventually(r.t, func() bool {
		var idle bool
		require.NoError(r.t, r.DB.DB.GetContext(r.t.Context(), &idle, `SELECT lease_holder IS NULL AND dirty_since IS NULL FROM hubrise_catalog_publish`))
		return idle
	}, 20*time.Second, 25*time.Millisecond, "the publisher never went idle")
}

func (r *e2eRig) remoteCatalog() map[string]any {
	r.t.Helper()
	var out map[string]any
	require.NoError(r.t, json.Unmarshal(r.srv.CatalogData(), &out))
	return out
}

func asObjects(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// skuOf is the SKU of the catalog with that ref (nil when absent) and the product it belongs to.
func skuOf(catalog map[string]any, ref string) (sku, product map[string]any) {
	for _, p := range asObjects(catalog["products"]) {
		for _, s := range asObjects(p["skus"]) {
			if s["ref"] == ref {
				return s, p
			}
		}
	}
	return nil, nil
}

// uploadable turns a catalog as HubRise serves it (ids) back into what an EPOS would upload (refs).
func uploadable(served map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range served {
		out[k] = v
	}
	refs := map[string]string{}
	var cats []any
	for _, c := range asObjects(served["categories"]) {
		refs[c["id"].(string)] = c["ref"].(string)
		cats = append(cats, c)
	}
	for _, c := range asObjects(served["categories"]) {
		delete(c, "id")
		if parent, ok := c["parent_id"].(string); ok {
			c["parent_ref"] = refs[parent]
		}
		delete(c, "parent_id")
	}
	out["categories"] = cats
	var products []any
	for _, p := range asObjects(served["products"]) {
		delete(p, "id")
		if id, ok := p["category_id"].(string); ok {
			p["category_ref"] = refs[id]
		}
		delete(p, "category_id")
		for _, s := range asObjects(p["skus"]) {
			delete(s, "id")
		}
		products = append(products, p)
	}
	out["products"] = products
	var original map[string]any
	_ = json.Unmarshal([]byte(eposCatalog), &original)
	out["deals"] = original["deals"]
	return out
}

func (r *e2eRig) createProduct(code, name, price string) uuid.UUID {
	r.t.Helper()
	data := r.mustAsk(`mutation ($i: CreateProductInput!) { createProduct(input: $i) { id } }`, map[string]any{"i": map[string]any{
		"categoryId": r.Fixtures.SushiCategory.ID.String(), "code": code, "isAvailable": true, "isVisible": true, "isDiscountable": true,
		"isHalal": false, "isLunchOnly": false, "isSpicy": false, "isVegetarian": false, "price": price, "vatCategory": "food",
		"translations": []map[string]any{{"language": "fr", "name": name}, {"language": "en", "name": name}, {"language": "zh", "name": name}},
	}})
	return decode[struct{ ID uuid.UUID }](r.t, data["createProduct"]).ID
}

func (r *e2eRig) editProduct(id uuid.UUID, input string) {
	r.t.Helper()
	r.mustAsk(`mutation ($id: ID!) { updateProduct(id: $id, input: `+input+`) { id } }`, map[string]any{"id": id.String()})
}

func (r *e2eRig) linkRef(id uuid.UUID) string {
	r.t.Helper()
	var ref string
	require.NoError(r.t, r.DB.DB.GetContext(r.t.Context(), &ref,
		`SELECT hubrise_ref FROM hubrise_catalog_links WHERE entity_type = 'product' AND product_id = $1`, id))
	return ref
}

// The restaurant lets the dashboard own the menu. Everything goes through the real hooks, the real
// GraphQL API, the real publisher and its workers; only HubRise is the mock.
func TestHubriseTheDashboardOwnsTheMenu(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	// The 10 reads a minute of HubRise are the subject of the unit tests; this scenario does more
	// than ten reads in a few seconds of wall clock.
	srv.SetLimits(hubrisetest.Limits{PerMinute: 500, PerHour: 2500, PerDay: 10000, HeavyPerMinute: 1000})
	newFileService(t)
	r := env.withHubriseInstalled(t, srv, func(o *hubrisewire.Options) {
		o.Inventory.Catalog.Tick = 20 * time.Millisecond
		o.Inventory.Catalog.Debounce = 100 * time.Millisecond
	})
	r.run()
	salmon := env.Fixtures.SalmonSushi.ID

	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(eposCatalog)))
	loc := r.connect()
	require.NotContains(t, loc.RawQuery, "error")
	require.True(t, r.settings(`enabled: true`).Enabled)
	st := r.pubStatus()
	assert.Equal(t, "NONE", st.CatalogDirection)
	assert.False(t, st.Publish.NeedsReconnectForWrite, "the connection asked for catalog.write")

	t.Run("switching to PUBLISH writes nothing until the first publish is confirmed", func(t *testing.T) {
		st := r.pubDirection("PUBLISH")
		assert.Equal(t, "PUBLISH", st.CatalogDirection)
		assert.True(t, st.Publish.AwaitingFirstPublish)

		srv.ClearRequests()
		r.editProduct(salmon, `{ price: "13.00" }`)
		assert.True(t, r.pubStatus().Publish.Pending, "the edit is remembered")
		r.pubDue()
		ran, err := r.rt.Catalog.Publisher.Tick(t.Context())
		require.NoError(t, err)
		assert.False(t, ran, "a background run needs the confirmation")
		r.quiet(150 * time.Millisecond)
		assert.Zero(t, r.puts())
		assert.Zero(t, r.gets(), "not even a read")
	})

	t.Run("the preview shows the first publish and the first publish keeps what the shop does not model", func(t *testing.T) {
		prev := r.pubPreview()
		assert.True(t, prev.FirstPublish)
		assert.Empty(t, prev.Blockers)
		products := 0
		for _, c := range prev.Counts {
			if c.Entity == "PRODUCT" || c.Entity == "product" {
				products = c.Added
			}
		}
		assert.Equal(t, 4, products, "the four products of the shop: %+v", prev.Counts)
		assert.GreaterOrEqual(t, prev.Foreign, 2, "the category and the product of the EPOS")
		assert.Zero(t, r.puts(), "a preview writes nothing")

		out := r.pubPublish(false, prev.PlanHash)
		assert.True(t, out.Published)
		assert.Equal(t, 1, r.puts())
		r.pubSettled()

		got := r.remoteCatalog()
		assert.NotEmpty(t, got["deals"], "the deal stays")
		assert.NotNil(t, got["x_top"], "an unknown key of the catalog stays")
		epos, _ := skuOf(got, "EPOS-ONLY")
		require.NotNil(t, epos, "the product of the EPOS stays")
		assert.NotNil(t, epos["x_sku"])
		assert.Len(t, asObjects(epos["price_overrides"]), 1, "the price rule stays")
		assert.Equal(t, "yes", epos["custom_fields"].(map[string]any)["foreign"])

		sku, product := skuOf(got, "SUSHI-SALMON")
		require.NotNil(t, sku, "the code of the product is its SKU ref")
		assert.Equal(t, "13.00 EUR", sku["price"], "the edit made before the confirmation is in")
		assert.Equal(t, "Sushi au Saumon", product["name"])
		assert.Equal(t, "SUSHI-SALMON", sku["custom_fields"].(map[string]any)["code"])
		assert.Equal(t, "SUSHI-SALMON", r.linkRef(salmon))
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product' AND product_id = $1 AND origin = 'published'`, salmon))
		assert.Zero(t, r.pubStatus().Publish.PendingProducts)
	})

	t.Run("an edit is debounced into one read and one write that keep the foreign fields", func(t *testing.T) {
		// The EPOS adds a rule and a field to a product of the shop, long after the last publish.
		r.pubExec(`UPDATE hubrise_catalog_publish SET last_published_at = last_published_at - interval '1 hour'`)
		edited := r.remoteCatalog()
		sku, _ := skuOf(edited, "SUSHI-SALMON")
		sku["price_overrides"] = []any{map[string]any{"price": "1.00 EUR", "dow": "1"}}
		sku["custom_fields"].(map[string]any)["foreign"] = "from the EPOS"
		raw, err := json.Marshal(uploadable(edited))
		require.NoError(t, err)
		require.NoError(t, srv.ReplaceCatalogRaw(raw))

		// Callback, stored event, debounced drift job: a warning, and not a single HubRise call.
		srv.ClearRequests()
		for _, status := range r.eposEvents() {
			require.Equal(t, http.StatusOK, status)
		}
		require.Eventually(t, func() bool { return r.pubStatus().Publish.ExternalChangeAt != nil }, 10*time.Second, 20*time.Millisecond)
		assert.Zero(t, r.gets(), "an external change costs no read")
		assert.Equal(t, 1, r.puts(), "and no write")
		assert.Equal(t, "PUBLISH", r.pubStatus().CatalogDirection)
		assert.False(t, r.pubStatus().CatalogUpdateAvailable, "nothing is imported")

		// Two edits of one burst, the second one before the debounce is over.
		srv.ClearRequests()
		r.editProduct(salmon, `{ price: "14.00" }`)
		r.editProduct(salmon, `{ translations: [{ language: "fr", name: "Saumon royal" }, { language: "en", name: "Royal salmon" }] }`)
		pend := r.pubStatus().Publish
		assert.True(t, pend.Pending)
		require.NotNil(t, pend.NextPublishAt)
		next, err := time.Parse(time.RFC3339, *pend.NextPublishAt)
		require.NoError(t, err)
		assert.True(t, next.After(time.Now().Add(5*time.Second)), "the debounce is not over: %s", next)
		ran, err := r.rt.Catalog.Publisher.Tick(t.Context())
		require.NoError(t, err)
		assert.False(t, ran)
		assert.Zero(t, r.gets()+len(srv.RequestsTo(http.MethodPut, catalogPath)), "nothing before the debounce")

		r.pubRuns(2)
		assert.Equal(t, 1, r.gets(), "one read")
		assert.Len(t, srv.RequestsTo(http.MethodPut, catalogPath), 1, "one write")
		got := r.remoteCatalog()
		sku, product := skuOf(got, "SUSHI-SALMON")
		require.NotNil(t, sku)
		assert.Equal(t, "14.00 EUR", sku["price"])
		assert.Equal(t, "Saumon royal", product["name"])
		assert.Len(t, asObjects(sku["price_overrides"]), 1, "the rule added by the EPOS survives")
		assert.Equal(t, "1.00 EUR", asObjects(sku["price_overrides"])[0]["price"])
		assert.Equal(t, "from the EPOS", sku["custom_fields"].(map[string]any)["foreign"])
		assert.NotEmpty(t, got["deals"])
		assert.Nil(t, r.pubStatus().Publish.ExternalChangeAt, "the publish merged the external edit")
	})

	var tempura uuid.UUID
	var tempuraRef string
	t.Run("a new product gets a stable ref and its code", func(t *testing.T) {
		tempura = r.createProduct("TEMPURA", "Tempura", "7,50")
		r.pubRuns(3)
		sku, product := skuOf(r.remoteCatalog(), "TEMPURA")
		require.NotNil(t, sku)
		assert.Equal(t, "Tempura", product["name"])
		assert.Equal(t, "7.50 EUR", sku["price"])
		assert.Equal(t, "TEMPURA", sku["custom_fields"].(map[string]any)["code"])
		tempuraRef = r.linkRef(tempura)
		assert.Equal(t, "TEMPURA", tempuraRef)

		r.editProduct(tempura, `{ translations: [{ language: "fr", name: "Tempura de crevettes" }] }`)
		r.pubRuns(4)
		sku, product = skuOf(r.remoteCatalog(), tempuraRef)
		require.NotNil(t, sku, "the ref did not move")
		assert.Equal(t, "Tempura de crevettes", product["name"])
		assert.Equal(t, tempuraRef, r.linkRef(tempura))
		pubInfo := r.mustAsk(`query ($id: ID!) { product(id: $id) { hubrisePublish { state ref } } }`, map[string]any{"id": tempura.String()})
		assert.Contains(t, string(pubInfo["product"]), tempuraRef)
	})

	t.Run("a deleted product leaves the catalog", func(t *testing.T) {
		// Nothing in the dashboard deletes a product; a script or a migration does, and the safety
		// net of the publisher notices.
		r.pubExec(`DELETE FROM products WHERE id = $1`, tempura)
		require.NoError(t, r.rt.Catalog.Publisher.Scan(t.Context()))
		assert.True(t, r.pubStatus().Publish.Pending, "the scan found the difference")
		r.pubRuns(5)
		sku, _ := skuOf(r.remoteCatalog(), tempuraRef)
		assert.Nil(t, sku)
		epos, _ := skuOf(r.remoteCatalog(), "EPOS-ONLY")
		assert.NotNil(t, epos, "only what the shop published is removed")
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_published_items WHERE hubrise_ref = $1`, tempuraRef))
	})

	t.Run("removals above the threshold wait for a confirmation", func(t *testing.T) {
		var bulk []uuid.UUID
		for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
			bulk = append(bulk, r.createProduct("BULK-"+n, "Bulk "+n, "5.00"))
		}
		r.pubRuns(6)
		for _, id := range bulk[:6] {
			r.pubExec(`DELETE FROM products WHERE id = $1`, id)
		}
		require.NoError(t, r.rt.Catalog.Publisher.Scan(t.Context()))
		r.pubDue()
		require.Eventually(t, func() bool {
			_, err := r.rt.Catalog.Publisher.Tick(t.Context())
			require.NoError(t, err)
			return r.pubStatus().Publish.LastErrorCode != nil
		}, 20*time.Second, 25*time.Millisecond)
		st := r.pubStatus().Publish
		assert.Equal(t, "REMOVALS_NEED_CONFIRM", *st.LastErrorCode)
		assert.Equal(t, 6, r.puts(), "nothing was removed")
		for i := range bulk {
			sku, _ := skuOf(r.remoteCatalog(), "BULK-"+string(rune('A'+i)))
			assert.NotNil(t, sku, "still published")
		}

		prev := r.pubPreview()
		assert.True(t, prev.RemovalsNeedConfirm)
		assert.Equal(t, 6, prev.Removed)
		assert.Equal(t, "HUBRISE_PUBLISH_CONFIRM_REQUIRED", r.refuses(publishDoc, map[string]any{"c": false, "f": false, "h": prev.PlanHash}))
		assert.Equal(t, 6, r.puts())

		out := r.pubPublish(true, prev.PlanHash)
		assert.True(t, out.Published)
		assert.Equal(t, 7, r.puts())
		assert.Nil(t, r.pubStatus().Publish.LastErrorCode)
		for i := range bulk {
			sku, _ := skuOf(r.remoteCatalog(), "BULK-"+string(rune('A'+i)))
			if i < 6 {
				assert.Nil(t, sku, "removed %d", i)
			} else {
				assert.NotNil(t, sku, "kept %d", i)
			}
		}
	})
}

// Production runs two replicas on one database: a due publish happens once, whichever replica sees
// it first, and the edits of a run are never lost.
func TestHubriseTwoReplicasPublishOnce(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
		hubtest.WithPublishCatalog())
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))

	gin.SetMode(gin.TestMode)
	routerB := gin.New()
	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	resB := env.wire(t, &db.DBPool{Customer: env.DB.DB, Admin: env.DB.DB}, broker)
	r := env.withHubriseInstalled(t, srv)
	_, rtB := env.installHubrise(t, resB, srv, routerB, r.web.URL+"/api/v1")
	require.NotSame(t, r.rt, rtB)

	prev := r.pubPreview()
	require.True(t, prev.FirstPublish)
	require.True(t, r.pubPublish(false, prev.PlanHash).Published)
	r.pubSettled()
	require.Equal(t, 1, r.puts())

	r.editProduct(env.Fixtures.SalmonSushi.ID, `{ price: "15.00" }`)
	srv.ClearRequests()
	r.pubDue()
	var wg sync.WaitGroup
	for _, rt := range []*hubrisewire.Runtime{r.rt, rtB, r.rt, rtB} {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			_, err := rt.Catalog.Publisher.Tick(ctx)
			assert.NoError(t, err)
		}(t.Context())
	}
	wg.Wait()
	r.pubSettled()
	assert.Equal(t, 2, r.puts(), "one more write, not two")
	assert.Equal(t, 1, r.gets())
	sku, _ := skuOf(r.remoteCatalog(), "SUSHI-SALMON")
	require.NotNil(t, sku)
	assert.Equal(t, "15.00 EUR", sku["price"])
}

// A connection that was never given catalog.write cannot publish, and one that loses the right is
// flagged for a reconnection instead of failing silently.
func TestHubriseWithoutCatalogWriteTheAdminIsAskedToReconnect(t *testing.T) {
	t.Run("the switch is refused", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		srv := hubrisetest.New(t)
		hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID))
		r := env.withHubriseInstalled(t, srv)
		assert.True(t, r.pubStatus().Publish.NeedsReconnectForWrite)
		assert.Equal(t, "HUBRISE_RECONNECT_REQUIRED", r.refuses(directionDoc, map[string]any{"d": "PUBLISH"}))
		assert.Equal(t, "NONE", r.pubStatus().CatalogDirection)
		assert.Zero(t, r.puts())
	})

	t.Run("the right is lost at HubRise", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		srv := hubrisetest.New(t)
		hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
			hubtest.WithPublishCatalog())
		require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))
		r := env.withHubriseInstalled(t, srv)
		prev := r.pubPreview()
		srv.SetTokenScopes(hubrisetest.Token, "catalog.read")
		code := r.refuses(publishDoc, map[string]any{"c": false, "f": false, "h": prev.PlanHash})
		assert.Equal(t, "HUBRISE_RECONNECT_REQUIRED", code)
		assert.Zero(t, r.puts())
		pub := r.pubStatus().Publish
		require.NotNil(t, pub.LastErrorCode)
		assert.Equal(t, "WRITE_SCOPE_MISSING", *pub.LastErrorCode)
		assert.True(t, pub.NeedsReconnectForWrite, "the dashboard asks for a reconnection")
	})
}

// Without HubRise nothing changes for the shop: a menu edit calls nobody and leaves no trace.
func TestHubriseStandaloneMenuEditsMakeNoCall(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	newFileService(t)
	r := env.withHubriseInstalled(t, srv)
	r.run()

	r.editProduct(env.Fixtures.SalmonSushi.ID, `{ price: "13.00" }`)
	r.createProduct("STANDALONE", "Standalone", "4.00")
	require.NoError(t, r.rt.Catalog.Publisher.Scan(t.Context()))
	ran, err := r.rt.Catalog.Publisher.Tick(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)
	r.quiet(200 * time.Millisecond)

	assert.Empty(t, srv.Requests(), "no HTTP call at all")
	assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_publish WHERE dirty_since IS NOT NULL`))
	assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_published_items`))
	assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))
}

// shopImages is the product bucket of the scenario: the salmon sushi has a picture, nothing else has.
type shopImages struct{ product uuid.UUID }

func (s shopImages) ProductImage(_ context.Context, id uuid.UUID, _ string) (*application.SourceImage, error) {
	if id != s.product {
		return nil, nil
	}
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return &application.SourceImage{Data: buf.Bytes(), Version: `"v1"`}, nil
}

func TestHubriseThePublishedMenuCarriesTheProductImages(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	srv.SetLimits(hubrisetest.Limits{PerMinute: 500, PerHour: 2500, PerDay: 10000, HeavyPerMinute: 1000})
	newFileService(t)
	salmon := env.Fixtures.SalmonSushi.ID
	r := env.withHubriseInstalled(t, srv, func(o *hubrisewire.Options) {
		o.Catalog.ProductImages = shopImages{product: salmon}
	})
	r.run()
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))
	require.NotContains(t, r.connect().RawQuery, "error")
	require.True(t, r.settings(`enabled: true`).Enabled)
	r.settings(`publishImages: true`)
	r.pubDirection("PUBLISH")

	prev := r.pubPreview()
	assert.Empty(t, srv.Images(), "a preview uploads nothing")
	out := r.pubPublish(false, prev.PlanHash)
	require.True(t, out.Published)
	r.pubSettled()

	imgs := srv.Images()
	require.Len(t, imgs, 1, "one picture for the one product that has one")
	assert.Equal(t, "tsb-product-"+salmon.String()+"-"+imgs[0].MD5[:12], imgs[0].PrivateRef)
	sku, product := skuOf(r.remoteCatalog(), "SUSHI-SALMON")
	require.NotNil(t, sku)
	assert.Equal(t, []any{imgs[0].ID}, product["image_ids"])
	_, other := skuOf(r.remoteCatalog(), "SUSHI-TUNA")
	if other != nil {
		assert.Empty(t, other["image_ids"])
	}

	// A later edit does not upload the picture again.
	r.editProduct(salmon, `{ price: "14.00" }`)
	r.pubRuns(2)
	assert.Len(t, srv.Images(), 1)
}
