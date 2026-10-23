package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func offSwitch() *bool { v := false; return &v }

// localProductRow inserts a product that exists before the integration.
func (e *cenv) localProduct(cat uuid.UUID, code, frName, price string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	var c any
	if code != "" {
		c = code
	}
	e.exec(`INSERT INTO products (id, category_id, price, code, slug, vat_category, is_visible, is_available) VALUES ($1, $2, $3, $4, $5, 'food', true, true)`,
		id, cat, price, c, "local-"+id.String()[:8])
	e.exec(`INSERT INTO product_translations (product_id, language, name) VALUES ($1, 'fr', $2)`, id, frName)
	return id
}

func (e *cenv) localGroup(product uuid.UUID, name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO product_choice_groups (id, product_id, min_selections, max_selections, sort_order) VALUES ($1, $2, 0, 1, 0)`, id, product)
	e.exec(`INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name) VALUES ($1, 'fr', $2)`, id, name)
	return id
}

func (e *cenv) localChoice(group, product uuid.UUID, name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier, sort_order) VALUES ($1, $2, $3, 0, 0)`, id, product, group)
	e.exec(`INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, 'fr', $2)`, id, name)
	return id
}

func TestFullImportAdoptsExistingRowsInsteadOfDuplicatingThem(t *testing.T) {
	e := newCatEnv(t)
	cat := newLocalCategory(t, e, 3, "Makis")
	byCode := e.localProduct(cat, "m1", "Maki saumon maison", "5")
	drinks := newLocalCategory(t, e, 4, "Boissons")
	byName := e.localProduct(drinks, "", "Cola", "2.50")
	sauce := e.localGroup(byCode, "sauce")
	soja := e.localChoice(sauce, byCode, "Soja")
	e.publish(sushiMenu())

	res := e.fullImport()

	assert.Equal(t, 2, res.Result.Count(domain.EntityCategory, domain.ActionLink))
	assert.Equal(t, byName, e.product("COLA-33").ID, "linked by its name inside the matched category")
	assert.Equal(t, byCode, e.product("SAUMON-6").ID, "linked by code, case-insensitively")
	assert.Equal(t, "5.50", e.product("SAUMON-6").Price, "the fields HubRise owns follow it")
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`), "only the 12 piece SKU was created")
	assert.Equal(t, 2, res.Result.Count(domain.EntityProduct, domain.ActionLink))
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM product_categories`))
	link, err := e.links.FindByKey(t.Context(), domain.EntityChoiceGroup, "SAUMON-6|SAUCE")
	require.NoError(t, err)
	assert.Equal(t, sauce, link.LocalID())
	assert.Equal(t, domain.OriginAdopted, link.Origin)
	choice, err := e.links.FindByKey(t.Context(), domain.EntityChoice, "SAUMON-6|SAUCE|SOJA")
	require.NoError(t, err)
	assert.Equal(t, soja, choice.LocalID())
	// The manual French name of the adopted product is kept; HubRise adds nothing in French.
	assert.Equal(t, textRow{Language: "fr", Name: "Maki saumon maison", Source: "manual"}, e.productTexts(byCode)["fr"])
}

func TestLinkOnlyRecordsLinksAndTouchesNoProductData(t *testing.T) {
	e := newCatEnv(t)
	cat := newLocalCategory(t, e, 3, "Makis")
	id := e.localProduct(cat, "M1", "Maki saumon", "5")
	e.publish(sushiMenu())

	res := e.sync(domain.SyncLinkOnly)

	assert.True(t, res.Applied)
	assert.Equal(t, 1, res.Result.Count(domain.EntityProduct, domain.ActionLink))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product'`))
	assert.Equal(t, "5.00", e.productByID(id).Price, "the price is the shop's until a FULL import")
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM products`), "nothing created")
	link, err := e.links.FindByKey(t.Context(), domain.EntityProduct, "SAUMON-6")
	require.NoError(t, err)
	assert.Empty(t, link.ContentHash, "a later FULL import still has its work to do")
	assert.False(t, link.CatalogDisabled)
	assert.Nil(t, e.connection().CatalogHash, "only a FULL import records the catalog as imported")

	// The FULL import that follows applies everything the link import left.
	e.fullImport()
	assert.Equal(t, "5.50", e.productByID(id).Price)
}

func TestDryRunWritesNothingAndRecordsThePreview(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.setting(domain.SettingsPatch{Enabled: offSwitch()}) // a preview needs a connection, not the switch

	res := e.sync(domain.SyncDryRun)

	assert.False(t, res.Applied)
	assert.Nil(t, res.Result)
	assert.Equal(t, 3, res.Diff.Count(domain.EntityProduct, domain.ActionCreate))
	assert.Zero(t, e.count(`SELECT count(*) FROM products`))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_catalog_links`))
	conn := e.connection()
	require.NotNil(t, conn.CatalogPreviewHash)
	assert.Equal(t, res.Diff.CatalogHash, *conn.CatalogPreviewHash)
	assert.True(t, conn.CatalogPreviewAt.Equal(e.clock.Now()))
	assert.Empty(t, e.notes.all())
}

func TestFullAndLinkOnlyNeedTheIntegrationSwitchedOn(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.setting(domain.SettingsPatch{Enabled: offSwitch()})
	for _, mode := range []domain.SyncMode{domain.SyncFull, domain.SyncLinkOnly} {
		_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: mode})
		assert.ErrorIs(t, err, domain.ErrNotEnabled)
	}
	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: "BOGUS"})
	assert.ErrorContains(t, err, "unknown sync mode")
}

func TestSyncWithoutAConnection(t *testing.T) {
	e := newCatEnv(t)
	e.exec(`DELETE FROM hubrise_connection`)
	ctx := t.Context()

	_, err := e.svc.Sync(ctx, application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	_, err = e.svc.CheckDrift(ctx)
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	assert.ErrorIs(t, e.svc.CheckEnable(ctx), domain.ErrNotConnected)
	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrNotConnected)
	n, err := e.svc.ForgetLinks(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	// A row without a token is a disconnected one.
	e.exec(`INSERT INTO hubrise_connection (id, enabled) VALUES (TRUE, FALSE)`)
	_, err = e.svc.Sync(ctx, application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, domain.ErrNotConnected)
}

func TestSyncWithoutACatalogOrClient(t *testing.T) {
	e := newCatEnv(t)
	e.exec(`UPDATE hubrise_connection SET catalog_id = NULL`)
	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, domain.ErrNotConnected)

	e.exec(`UPDATE hubrise_connection SET catalog_id = $1`, hubrisetest.CatalogID)
	boom := errors.New("no client")
	e.svc.NewClient = func(*domain.Connection) (application.CatalogAPI, error) { return nil, boom }
	_, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, boom)
	assert.False(t, e.leaseHeld(), "the lease is released on every path")
}

func (e *cenv) leaseHeld() bool {
	ok, err := e.leases.Acquire(e.t.Context(), "catalog_import", "someone-else", e.clock.Now(), time.Minute)
	require.NoError(e.t, err)
	if ok {
		require.NoError(e.t, e.leases.Release(e.t.Context(), "catalog_import", "someone-else"))
	}
	return !ok
}

func TestAnEmptyCatalogIsRejectedAndRetiresNothing(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()

	e.publish(newMenu().category("MAKI", "Makis"))
	for _, mode := range []domain.SyncMode{domain.SyncDryRun, domain.SyncFull} {
		_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: mode})
		assert.ErrorIs(t, err, domain.ErrEmptyCatalog)
	}
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products WHERE is_visible`))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_catalog_links WHERE retired_at IS NOT NULL`))
}

func TestARevokedTokenPausesThePipeline(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs", Status: 401})

	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})

	require.Error(t, err)
	assert.ErrorIs(t, err, hubrise.KindUnauthorized)
	assert.NotNil(t, e.connection().AuthErrorAt)
}

func TestAServerErrorIsReturnedWithoutPausing(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs", Status: 404})
	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})
	require.Error(t, err)
	assert.Nil(t, e.connection().AuthErrorAt)
}

func TestTwoImportsDoNotRunAtTheSameTime(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	ok, err := e.leases.Acquire(t.Context(), "catalog_import", "other-instance", e.clock.Now(), time.Hour)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull})

	assert.ErrorIs(t, err, domain.ErrImportInProgress)
	assert.Zero(t, e.count(`SELECT count(*) FROM products`))
}

// bigMenu is a menu of n single-SKU products.
func bigMenu(n int) *menu {
	m := newMenu().category("C", "Carte")
	for i := 0; i < n; i++ {
		m.product(fmt.Sprintf("P%d", i), "C", fmt.Sprintf("Produit %d", i), sku(fmt.Sprintf("S%d", i), "3"))
	}
	return m
}

func TestAMassRetirementWaitsForAConfirmation(t *testing.T) {
	e := newCatEnv(t)
	e.publish(bigMenu(8))
	e.fullImport()
	e.publish(bigMenu(2)) // 6 of 8 disappear

	// A manual import stops and says what it would retire.
	res, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull})
	var confirm *domain.RetireConfirmError
	require.ErrorAs(t, err, &confirm)
	assert.ErrorIs(t, err, domain.ErrRetireConfirmRequired)
	assert.Equal(t, 6, confirm.Retiring)
	assert.Equal(t, 8, confirm.Linked)
	assert.False(t, res.Applied)
	assert.Equal(t, 8, e.count(`SELECT count(*) FROM products WHERE is_visible`))

	// A scheduled import never applies it: it stores the situation for the dashboard.
	res, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull, Scheduled: true})
	require.NoError(t, err)
	assert.True(t, res.PendingReview)
	assert.False(t, res.Applied)
	assert.Equal(t, 8, e.count(`SELECT count(*) FROM products WHERE is_visible`))
	assert.True(t, e.connection().CatalogPendingReview())
	assert.Contains(t, *e.connection().LastError, domain.PendingReviewPrefix)

	// A scheduled import with the confirmation flag still does not carry it: only a human does.
	res, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull, Scheduled: true, ConfirmRetire: true})
	require.NoError(t, err)
	assert.True(t, res.PendingReview)
	assert.False(t, res.Applied)
	assert.Equal(t, 8, e.count(`SELECT count(*) FROM products WHERE is_visible`))
	assert.True(t, e.connection().CatalogPendingReview())

	// The confirmed manual import applies it and clears the pending review.
	res, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull, ConfirmRetire: true})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM products WHERE is_visible`))
	assert.Equal(t, 8, e.count(`SELECT count(*) FROM products`), "retired, not deleted")
	conn := e.connection()
	assert.False(t, conn.CatalogPendingReview())
	assert.Nil(t, conn.LastError)
}

func TestAFewRetirementsNeedNoConfirmation(t *testing.T) {
	e := newCatEnv(t)
	e.publish(bigMenu(8))
	e.fullImport()
	e.publish(bigMenu(5)) // 3 of 8: under the minimum
	res, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull, Scheduled: true})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, 5, e.count(`SELECT count(*) FROM products WHERE is_visible`))
}

func TestCatalogSyncMayOnlyBeEnabledAfterAFreshPreviewOfTheCurrentCatalog(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	ctx := t.Context()

	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrPreviewRequired, "never previewed")

	e.sync(domain.SyncDryRun)
	assert.NoError(t, e.svc.CheckEnableSyncCatalog(ctx))

	e.clock.Advance(23 * time.Hour)
	assert.NoError(t, e.svc.CheckEnableSyncCatalog(ctx))
	e.clock.Advance(2 * time.Hour)
	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrPreviewRequired, "the preview is older than 24 hours")

	e.sync(domain.SyncDryRun)
	assert.NoError(t, e.svc.CheckEnableSyncCatalog(ctx))
	changed := sushiMenu()
	changed.d.Products[1].Skus[0].Price = money("3.50")
	e.publish(changed)
	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrPreviewRequired, "the catalog changed since the preview")

	e.publish(newMenu().category("MAKI", "Makis"))
	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrEmptyCatalog)
}

func TestReconnectingToAnotherLocationRequiresARelink(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	ctx := t.Context()
	before := e.connection()

	// The OAuth callback stored a connection to another location.
	e.exec(`UPDATE hubrise_connection SET location_id = 'other-location'`)
	required, err := e.svc.AfterConnect(ctx, before)
	require.NoError(t, err)
	assert.True(t, required)

	conn := e.connection()
	assert.False(t, conn.Enabled)
	assert.False(t, conn.SyncCatalog)
	assert.False(t, conn.SyncInventory)
	assert.NotNil(t, conn.RelinkRequiredAt)
	assert.True(t, conn.SyncStatus || conn.PushOrders, "the other settings stay")

	assert.ErrorIs(t, e.svc.CheckEnable(ctx), domain.ErrRelinkRequired)
	assert.ErrorIs(t, e.svc.CheckEnableSyncCatalog(ctx), domain.ErrRelinkRequired)
	_, err = e.svc.Sync(ctx, application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, domain.ErrRelinkRequired)

	// Forgetting the links lifts the requirement, the products stay.
	n, err := e.svc.ForgetLinks(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(11), n)
	assert.Nil(t, e.connection().RelinkRequiredAt)
	assert.NoError(t, e.svc.CheckEnable(ctx))
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`))
}

func TestReconnectingToAnotherCatalogAlsoRequiresARelink(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	before := e.connection()
	e.exec(`UPDATE hubrise_connection SET catalog_id = 'other-catalog'`)
	required, err := e.svc.AfterConnect(t.Context(), before)
	require.NoError(t, err)
	assert.True(t, required)
}

func TestReconnectingToTheSameLocationOrWithoutLinksChangesNothing(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()

	required, err := e.svc.AfterConnect(ctx, nil)
	require.NoError(t, err)
	assert.False(t, required, "a first connection")

	before := e.connection()
	required, err = e.svc.AfterConnect(ctx, before)
	require.NoError(t, err)
	assert.False(t, required, "same location and catalog")

	e.exec(`UPDATE hubrise_connection SET location_id = 'other-location'`)
	required, err = e.svc.AfterConnect(ctx, before)
	require.NoError(t, err)
	assert.False(t, required, "another location but nothing linked yet")
	assert.True(t, e.connection().Enabled)

	// An already pending requirement stays reported when the same location reconnects.
	e.publish(sushiMenu())
	e.fullImport()
	e.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
	same := e.connection()
	required, err = e.svc.AfterConnect(ctx, same)
	require.NoError(t, err)
	assert.True(t, required)
}

func TestAStaleRelinkFlagWithoutLinksIsLifted(t *testing.T) {
	e := newCatEnv(t)
	e.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
	require.NoError(t, e.svc.CheckEnable(t.Context()))
	assert.Nil(t, e.connection().RelinkRequiredAt)
}

func TestManualLinks(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()
	cat := newLocalCategory(t, e, 1, "Plats")
	a := e.localProduct(cat, "", "Riz", "3")
	b := e.localProduct(cat, "", "Nems", "3")
	ref := func(s string) *string { return &s }

	require.NoError(t, e.svc.LinkProduct(ctx, a, ref(" SKU-A ")))
	link, err := e.links.FindByKey(ctx, domain.EntityProduct, "SKU-A")
	require.NoError(t, err)
	assert.Equal(t, domain.OriginManual, link.Origin)
	assert.Equal(t, a, link.LocalID())
	require.NoError(t, e.svc.LinkProduct(ctx, a, ref("SKU-A")), "linking again is a no-op")

	assert.ErrorContains(t, e.svc.LinkProduct(ctx, b, ref("SKU-A")), "already linked")

	require.NoError(t, e.svc.LinkProduct(ctx, a, ref("SKU-B")), "re-pointing replaces the old link")
	_, err = e.links.FindByKey(ctx, domain.EntityProduct, "SKU-A")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	require.NoError(t, e.svc.LinkProduct(ctx, a, nil))
	_, err = e.links.FindByKey(ctx, domain.EntityProduct, "SKU-B")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, e.svc.LinkProduct(ctx, a, ref("  ")), "an empty ref unlinks, nothing to remove")
	require.NoError(t, e.svc.LinkProduct(ctx, b, nil))

	// A manual link is what the next import adopts first.
	require.NoError(t, e.svc.LinkProduct(ctx, b, ref("SAUMON-6")))
	e.publish(sushiMenu())
	e.fullImport()
	assert.Equal(t, b, e.product("SAUMON-6").ID)
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products WHERE slug NOT LIKE 'local-%'`)+1, "only the other two SKUs were created")
}

func TestAFailedImageIsAWarningNotAnError(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{ImportImages: func() *bool { v := true; return &v }()})
	png := []byte("\x89PNG-not-really")
	e.srv.AddImage("img-ok", "image/png", png)
	m := sushiMenu()
	m.d.Products[0].ImageIDs = []string{"img-ok"}
	m.d.Products[1].ImageIDs = []string{"img-missing"}
	e.publish(m)

	res := e.fullImport()

	assert.Equal(t, png, e.images.uploaded[e.product("SAUMON-6").ID])
	assert.Contains(t, e.images.names[e.product("SAUMON-6").ID], ".png")
	var imageWarnings []domain.Warning
	for _, w := range res.Warnings {
		if w.Code == domain.WarnImage {
			imageWarnings = append(imageWarnings, w)
		}
	}
	require.Len(t, imageWarnings, 1)
	assert.Equal(t, "COLA-33", imageWarnings[0].Key)
	assert.NotNil(t, e.connection().CatalogHash, "the menu is imported")

	// The checksum is remembered: the second round downloads nothing.
	e.srv.ClearRequests()
	e.images.uploaded = nil
	m.d.Products[0].Skus[0].Price = money("6")
	e.publish(m)
	e.fullImport()
	assert.Empty(t, e.images.uploaded)
	for _, r := range e.srv.Requests() {
		assert.NotContains(t, r.Path, "/data", "no image body was downloaded")
	}
}

func TestImageProblemsAreReportedPerProduct(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{ImportImages: func() *bool { v := true; return &v }()})
	e.srv.AddImage("img", "image/jpeg", []byte("jpeg"))
	m := newMenu().category("C", "Carte")
	s := sku("S1", "3")
	m.product("P1", "C", "Un", s)
	m.d.Products[0].ImageIDs = []string{"img"}
	e.publish(m)

	// The file service refuses the image.
	e.images.err = errors.New("file service down")
	res := e.fullImport()
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0].Message, "file service down")

	// HubRise's checksum disagrees with the bytes it serves: nothing is stored.
	e.images.err = nil
	e.srv.AddImage("img", "image/jpeg", []byte("jpeg"))
	e.exec(`UPDATE hubrise_catalog_links SET image_md5 = NULL`)
	m.d.Products[0].Skus[0].Price = money("4")
	e.publish(m)
	e.fullImport()
	assert.Equal(t, []byte("jpeg"), e.images.uploaded[e.product("S1").ID])
}

func TestImagesAreSkippedWhenTheSettingIsOff(t *testing.T) {
	e := newCatEnv(t)
	e.srv.AddImage("img", "image/png", []byte("png"))
	m := newMenu().category("C", "Carte").product("P1", "C", "Un", sku("S1", "3"))
	m.d.Products[0].ImageIDs = []string{"img"}
	e.publish(m)
	e.fullImport()
	assert.Empty(t, e.images.uploaded)

	e.svc.Images = nil
	e.setting(domain.SettingsPatch{ImportImages: func() *bool { v := true; return &v }()})
	m.d.Products[0].Skus[0].Price = money("4")
	e.publish(m)
	e.fullImport()
	assert.Empty(t, e.images.uploaded, "no uploader configured")
}

// gatedCatalog holds a GetCatalog call until the test lets it go.
type gatedCatalog struct {
	application.CatalogAPI
	entered chan struct{}
	release chan struct{}
}

func (g gatedCatalog) GetCatalog(ctx context.Context, id string) (*hubrise.Catalog, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-time.After(10 * time.Second): // a failing test must not hang
	}
	return g.CatalogAPI.GetCatalog(ctx, id)
}

// The lease treats its own holder as a renewal, so two imports of one process (a double click, a click
// and the drift job) must not share a holder: the second one is turned away while the first runs.
func TestTwoImportsOfOneProcessDoNotRunAtTheSameTime(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	gate := gatedCatalog{entered: make(chan struct{}, 2), release: make(chan struct{})}
	inner := e.svc.NewClient
	e.svc.NewClient = func(c *domain.Connection) (application.CatalogAPI, error) {
		api, err := inner(c)
		gate.CatalogAPI = api
		return gate, err
	}

	first := make(chan error, 1)
	go func() {
		_, err := e.svc.Sync(context.WithoutCancel(t.Context()), application.SyncRequest{Mode: domain.SyncFull})
		first <- err
	}()
	<-gate.entered // the first import holds the lease and waits in the catalog read

	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull})
	assert.ErrorIs(t, err, domain.ErrImportInProgress)

	close(gate.release)
	require.NoError(t, <-first)
	products := e.count(`SELECT count(*) FROM products`)
	assert.Positive(t, products)

	// The lease is free again, and a second import changes nothing.
	e.fullImport()
	assert.Equal(t, products, e.count(`SELECT count(*) FROM products`))
}
