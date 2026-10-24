package graphql_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/hubrisewire"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// publishMenuWith publishes the menu of publishMenu with another price of the gyoza and, when
// extra is not empty, one more product.
func (r *e2eRig) publishMenuWith(gyozaPrice, extra string) {
	r.t.Helper()
	product := func(ref, name, sku, price, code string) pkghubrise.Product {
		return pkghubrise.Product{
			Ref: ref, CategoryRef: "SUSHI", Name: name, TaxRate: &pkghubrise.TaxRate{Delivery: "6", Collection: "6", EatIn: "12"},
			Skus: []pkghubrise.Sku{{Ref: sku, Price: pkghubrise.Money(price + " EUR"), CustomFields: json.RawMessage(`{"code":"` + code + `"}`)}},
		}
	}
	products := []pkghubrise.Product{
		product("P-SAUMON", "Sushi au Saumon", "SKU-SAUMON", "12.50", "SUSHI-SALMON"),
		product("P-GYOZA", "Gyoza", "SKU-GYOZA", gyozaPrice, "GYOZA"),
	}
	if extra != "" {
		products = append(products, product("P-EDAMAME", "Edamame", "SKU-EDAMAME", extra, "EDAMAME"))
	}
	require.NoError(r.t, r.srv.ReplaceCatalog(pkghubrise.CatalogData{
		Categories: []pkghubrise.Category{{Ref: "SUSHI", Name: "Sushi"}}, Products: products,
	}))
}

func (r *e2eRig) priceOfLink(ref string) string {
	r.t.Helper()
	var id uuid.UUID
	require.NoError(r.t, r.DB.DB.GetContext(r.t.Context(), &id, `SELECT product_id FROM hubrise_catalog_links WHERE hubrise_ref = $1`, ref))
	price, _ := r.productRow(id)
	return price
}

// The catalog changes at HubRise. The notification travels the whole chain with nothing replaced by
// a fake: HubRise calls the signed callback endpoint, the event is stored, the debounced drift job
// of the running workers picks it up, the real catalog service compares the catalog with the last
// import and either only raises the update-available banner (manual import) or imports it
// (automatic import).
func TestHubriseACatalogUpdateCallbackReachesTheDriftCheckAndTheImport(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	r := env.withHubriseInstalled(t, srv, func(o *hubrisewire.Options) {
		o.Inventory.Catalog.Tick = 20 * time.Millisecond
		o.Inventory.Catalog.Debounce = 100 * time.Millisecond
	})
	r.run()
	r.connect()
	r.publishMenu()
	r.settings(`enabled: true`)
	r.mustAsk(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`, nil)
	r.settings(`syncCatalog: true`)
	r.mustAsk(`mutation { syncHubriseCatalog(mode: FULL) { applied } }`, nil)
	require.Equal(t, "6.50", r.priceOfLink("SKU-GYOZA"))
	// The notifications of the setup itself (the first publication) are delivered and settled first,
	// so that the next ones are the ones under test.
	for _, s := range r.eposEvents() {
		require.Equal(t, http.StatusOK, s)
	}
	catalogEvents := func(where string) int {
		return countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_events WHERE resource_type = 'catalog'`+where)
	}
	require.Eventually(t, func() bool { return catalogEvents(` AND state = 'pending'`) == 0 }, 20*time.Second, 20*time.Millisecond)
	require.False(t, r.status().CatalogUpdateAvailable, "the setup's own notification found nothing new")
	setupEvents := catalogEvents("")
	catalogFetches := func() int { return r.remoteRequests(http.MethodGet, "/v1/catalogs/") }

	t.Run("manual import: the callback raises the banner and imports nothing", func(t *testing.T) {
		before := catalogFetches()
		r.publishMenuWith("7.00", "")
		for _, s := range r.eposEvents() {
			require.Equal(t, http.StatusOK, s)
		}
		assert.Equal(t, setupEvents+1, catalogEvents(""), "the callback stored the catalog event")

		require.Eventually(t, func() bool { return r.status().CatalogUpdateAvailable }, 20*time.Second, 20*time.Millisecond,
			"the drift check noticed the new catalog")
		assert.Greater(t, catalogFetches(), before, "the check read the catalog")
		assert.Equal(t, "6.50", r.priceOfLink("SKU-GYOZA"), "manual import mode: nothing is imported behind the admin's back")
		// The banner is raised before the event row is settled.
		assert.Eventually(t, func() bool { return catalogEvents(` AND state = 'pending'`) == 0 }, 20*time.Second, 20*time.Millisecond,
			"the event was consumed by the check")
	})

	t.Run("automatic import: the callback ends in the imported change", func(t *testing.T) {
		assert.True(t, r.settings(`catalogAutoImport: true`).CatalogAutoImport)
		r.publishMenuWith("7.00", "4.20") // two edits in a row make one check
		r.publishMenuWith("7.50", "4.20")
		for _, s := range r.eposEvents() {
			require.Equal(t, http.StatusOK, s)
		}

		require.Eventually(t, func() bool { return r.priceOfLink("SKU-GYOZA") == "7.50" }, 20*time.Second, 20*time.Millisecond,
			"the catalog was imported by the drift check")
		assert.Equal(t, 3, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product'`),
			"the new product was created")
		require.Eventually(t, func() bool { return !r.status().CatalogUpdateAvailable }, 20*time.Second, 20*time.Millisecond,
			"the banner is cleared by the import")
	})
}
