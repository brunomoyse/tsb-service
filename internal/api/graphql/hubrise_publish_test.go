package graphql_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/testhelpers"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/hubrise/hubrisetest"
)

const publishStatusFields = `catalogDirection features { syncCatalog publishCatalog } catalogUpdateAvailable
	publish { enabled active pending pendingSince nextPublishAt publishing lastPublishedAt firstPublishConfirmedAt
		awaitingFirstPublish lastError lastErrorCode lastErrorAt externalChangeAt needsReconnectForWrite
		accountCatalogDetected publishImages publishedProducts pendingProducts }`

type publishStatusDoc struct {
	CatalogDirection string `json:"catalogDirection"`
	Features         struct {
		SyncCatalog    bool `json:"syncCatalog"`
		PublishCatalog bool `json:"publishCatalog"`
	} `json:"features"`
	CatalogUpdateAvailable bool `json:"catalogUpdateAvailable"`
	Publish                struct {
		Enabled                 bool    `json:"enabled"`
		Active                  bool    `json:"active"`
		Pending                 bool    `json:"pending"`
		PendingSince            *string `json:"pendingSince"`
		NextPublishAt           *string `json:"nextPublishAt"`
		Publishing              bool    `json:"publishing"`
		LastPublishedAt         *string `json:"lastPublishedAt"`
		FirstPublishConfirmedAt *string `json:"firstPublishConfirmedAt"`
		AwaitingFirstPublish    bool    `json:"awaitingFirstPublish"`
		LastError               *string `json:"lastError"`
		LastErrorCode           *string `json:"lastErrorCode"`
		LastErrorAt             *string `json:"lastErrorAt"`
		ExternalChangeAt        *string `json:"externalChangeAt"`
		NeedsReconnectForWrite  bool    `json:"needsReconnectForWrite"`
		AccountCatalogDetected  bool    `json:"accountCatalogDetected"`
		PublishImages           bool    `json:"publishImages"`
		PublishedProducts       int     `json:"publishedProducts"`
		PendingProducts         int     `json:"pendingProducts"`
	} `json:"publish"`
}

func (r *hubriseAdminRig) publishStatus() publishStatusDoc {
	r.t.Helper()
	data := r.run(`{ hubriseStatus { `+publishStatusFields+` } }`, nil)
	return decode[publishStatusDoc](r.t, data["hubriseStatus"])
}

func newPublishGQLRig(t *testing.T) *hubriseAdminRig {
	t.Helper()
	return newHubriseAdminRig(t, true, hubtest.WithPublishCatalog())
}

const previewDoc = `query Preview($removeForeign: Boolean) { previewHubriseCatalogPublish(removeForeign: $removeForeign) {
	planHash firstPublish added updated removed unchanged foreign
	counts { entity added updated removed }
	changes { entity action key ref name fields }
	truncated foreignItems { entity ref name } preserved
	warnings { code key message } blockers { code key message }
	removalsNeedConfirm generatedAt } }`

type previewDocT struct {
	PlanHash     string `json:"planHash"`
	FirstPublish bool   `json:"firstPublish"`
	Added        int    `json:"added"`
	Updated      int    `json:"updated"`
	Removed      int    `json:"removed"`
	Unchanged    int    `json:"unchanged"`
	Foreign      int    `json:"foreign"`
	Counts       []struct {
		Entity                  string
		Added, Updated, Removed int
	} `json:"counts"`
	Changes []struct {
		Entity, Action, Key, Ref, Name string
		Fields                         []string
	} `json:"changes"`
	Truncated           bool `json:"truncated"`
	ForeignItems        []struct{ Entity, Ref, Name string }
	Preserved           []string
	Warnings            []struct{ Code, Message string }
	Blockers            []struct{ Code, Message string }
	RemovalsNeedConfirm bool   `json:"removalsNeedConfirm"`
	GeneratedAt         string `json:"generatedAt"`
}

func (r *hubriseAdminRig) preview(removeForeign bool) previewDocT {
	r.t.Helper()
	data := r.run(previewDoc, map[string]any{"removeForeign": removeForeign})
	return decode[previewDocT](r.t, data["previewHubriseCatalogPublish"])
}

const publishDoc = `mutation Publish($c: Boolean, $f: Boolean, $h: String) { publishHubriseCatalog(confirmRemovals: $c, removeForeign: $f, planHash: $h) {
	published queued status { enabled active pending awaitingFirstPublish lastPublishedAt publishedProducts } } }`

type publishResultDoc struct {
	Published bool `json:"published"`
	Queued    bool `json:"queued"`
	Status    struct {
		Enabled, Active, Pending, AwaitingFirstPublish bool
		LastPublishedAt                                *string
		PublishedProducts                              int
	} `json:"status"`
}

func TestPublishFieldsOfTheStatus(t *testing.T) {
	r := newPublishGQLRig(t)
	st := r.publishStatus()
	assert.Equal(t, "PUBLISH", st.CatalogDirection)
	assert.True(t, st.Features.PublishCatalog)
	assert.False(t, st.Features.SyncCatalog)
	assert.False(t, st.CatalogUpdateAvailable)
	assert.True(t, st.Publish.Enabled)
	assert.True(t, st.Publish.AwaitingFirstPublish, "never confirmed")
	assert.False(t, st.Publish.Active)
	assert.False(t, st.Publish.NeedsReconnectForWrite)
	assert.Nil(t, st.Publish.LastErrorCode)

	t.Run("a stale 'update available' stamp is hidden while the dashboard owns the menu", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET catalog_update_available_at = now()`)
		assert.False(t, r.publishStatus().CatalogUpdateAvailable)
	})

	t.Run("outside PUBLISH", func(t *testing.T) {
		plain := newHubriseAdminRig(t, true)
		st := plain.publishStatus()
		assert.Equal(t, "NONE", st.CatalogDirection)
		assert.False(t, st.Features.PublishCatalog)
		assert.False(t, st.Publish.Enabled)
		assert.True(t, st.Publish.NeedsReconnectForWrite, "the stored scope has no catalog.write")
	})

	t.Run("without HubRise at all", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		tok, err := testhelpers.GenerateTestAccessToken(env.Fixtures.AdminUser.ID.String(), true)
		require.NoError(t, err)
		resp := gqlAs(t, env.TestContext, tok, "en", `{ hubriseStatus { available `+publishStatusFields+` } }`, nil)
		require.Empty(t, resp.Errors)
		var data struct {
			S struct {
				Available bool `json:"available"`
				publishStatusDoc
			} `json:"hubriseStatus"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.False(t, data.S.Available)
		assert.Equal(t, "NONE", data.S.CatalogDirection)
		assert.False(t, data.S.Publish.Enabled)
	})
}

const directionDoc = `mutation($d: HubriseCatalogDirection!, $ack: Boolean) { setHubriseCatalogDirection(direction: $d, acknowledgeLocalOverwrite: $ack) { ` + publishStatusFields + ` } }`

func TestSwitchingTheCatalogDirection(t *testing.T) {
	t.Run("to PUBLISH", func(t *testing.T) {
		r := newHubriseAdminRig(t, true, hubtest.WithScope(hubtest.WriteScope), hubtest.WithFeatures(true, true, true, true, false))
		st := decode[publishStatusDoc](t, r.run(directionDoc, map[string]any{"d": "PUBLISH"})["setHubriseCatalogDirection"])
		assert.Equal(t, "PUBLISH", st.CatalogDirection)
		assert.False(t, st.Features.SyncCatalog, "the import is off")
		assert.True(t, st.Features.PublishCatalog)
		assert.True(t, st.Publish.AwaitingFirstPublish)
	})
	t.Run("without catalog.write", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		code, _ := r.fail(directionDoc, map[string]any{"d": "PUBLISH"})
		assert.Equal(t, "HUBRISE_RECONNECT_REQUIRED", code)
		assert.Equal(t, "NONE", r.publishStatus().CatalogDirection)
	})
	t.Run("leaving with unpublished edits", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`UPDATE hubrise_catalog_publish SET dirty_since = now(), change_seq = change_seq + 1`)
		code, _ := r.fail(directionDoc, map[string]any{"d": "NONE"})
		assert.Equal(t, "HUBRISE_PUBLISH_PENDING_CHANGES", code)
		st := decode[publishStatusDoc](t, r.run(directionDoc, map[string]any{"d": "NONE", "ack": true})["setHubriseCatalogDirection"])
		assert.Equal(t, "NONE", st.CatalogDirection)
		assert.False(t, st.Publish.Pending)
	})
	t.Run("to IMPORT needs the dry run", func(t *testing.T) {
		r := newPublishGQLRig(t)
		code, _ := r.fail(directionDoc, map[string]any{"d": "IMPORT"})
		assert.Equal(t, "HUBRISE_PREVIEW_REQUIRED", code)
	})
	t.Run("no connection", func(t *testing.T) {
		r := newHubriseAdminRig(t, false)
		code, _ := r.fail(directionDoc, map[string]any{"d": "PUBLISH"})
		assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
	})
}

func TestOnlyTheDirectionCanTurnTheImportOn(t *testing.T) {
	r := newPublishGQLRig(t)
	code, _ := r.fail(`mutation { updateHubriseSettings(input: { syncCatalog: true }) { available } }`, nil)
	assert.Equal(t, "HUBRISE_DIRECTION_CONFLICT", code)
	code, _ = r.fail(`mutation { syncHubriseCatalog(mode: FULL) { applied } }`, nil)
	assert.Equal(t, "HUBRISE_PUBLISH_MODE", code)
	code, _ = r.fail(`mutation { previewHubriseCatalogSync(mode: FULL) { catalogHash } }`, nil)
	assert.Equal(t, "HUBRISE_PUBLISH_MODE", code)
	r.run(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`, nil)
	r.run(`mutation { syncHubriseCatalog(mode: LINK_ONLY) { applied } }`, nil)

	data := r.run(`mutation { updateHubriseSettings(input: { publishImages: true }) { `+publishStatusFields+` } }`, nil)
	assert.True(t, decode[publishStatusDoc](t, data["updateHubriseSettings"]).Publish.PublishImages)
	old := newHubriseAdminRig(t, true)
	code, _ = old.fail(`mutation { updateHubriseSettings(input: { publishImages: true }) { available } }`, nil)
	assert.Equal(t, "HUBRISE_RECONNECT_REQUIRED", code)
}

func TestPreviewThenPublishTheMenu(t *testing.T) {
	r := newPublishGQLRig(t)
	prev := r.preview(false)
	assert.True(t, prev.FirstPublish)
	assert.NotEmpty(t, prev.PlanHash)
	assert.Positive(t, prev.Added)
	assert.Equal(t, prev.Added+prev.Updated+prev.Removed > 0, true)
	assert.NotEmpty(t, prev.Counts)
	assert.NotEmpty(t, prev.Changes)
	assert.False(t, prev.Truncated)
	assert.Empty(t, prev.Blockers)
	assert.NotEmpty(t, prev.GeneratedAt)
	assert.Zero(t, r.srv.CatalogPuts(), "a preview writes nothing")
	assert.Positive(t, prev.Foreign, "the mock catalog holds products of the EPOS")
	assert.Len(t, prev.ForeignItems, prev.Foreign)
	for _, c := range prev.Changes {
		assert.Equal(t, "ADD", c.Action)
		assert.NotEmpty(t, c.Key)
		assert.NotEmpty(t, c.Name)
	}

	t.Run("the confirmation is required and bound to the plan", func(t *testing.T) {
		code, _ := r.fail(publishDoc, nil)
		assert.Equal(t, "HUBRISE_PUBLISH_CONFIRM_REQUIRED", code)
		code, _ = r.fail(publishDoc, map[string]any{"h": "stale"})
		assert.Equal(t, "HUBRISE_PREVIEW_STALE", code)
		assert.Zero(t, r.srv.CatalogPuts())
	})

	t.Run("published", func(t *testing.T) {
		res := decode[publishResultDoc](t, r.run(publishDoc, map[string]any{"h": prev.PlanHash})["publishHubriseCatalog"])
		assert.True(t, res.Published)
		assert.False(t, res.Queued)
		assert.True(t, res.Status.Active)
		assert.False(t, res.Status.AwaitingFirstPublish)
		assert.False(t, res.Status.Pending)
		assert.NotNil(t, res.Status.LastPublishedAt)
		assert.Positive(t, res.Status.PublishedProducts)
		assert.Equal(t, 1, r.srv.CatalogPuts())
	})

	t.Run("a second preview finds nothing to do", func(t *testing.T) {
		again := r.preview(false)
		assert.False(t, again.FirstPublish)
		assert.Zero(t, again.Added+again.Updated+again.Removed)
		assert.Positive(t, again.Unchanged)
		assert.NotEmpty(t, again.Preserved, "the sections of HubRise that are carried over")
	})

	t.Run("an edit shows as an update", func(t *testing.T) {
		r.exec(`UPDATE products SET price = price + 1 WHERE id = $1`, r.Fixtures.SalmonSushi.ID)
		prev := r.preview(false)
		require.Equal(t, 1, prev.Updated)
		require.Len(t, prev.Changes, 1)
		assert.Equal(t, "UPDATE", prev.Changes[0].Action)
		assert.Equal(t, "PRODUCT", prev.Changes[0].Entity)
		assert.Contains(t, prev.Changes[0].Fields, "price")
		res := decode[publishResultDoc](t, r.run(publishDoc, map[string]any{"h": prev.PlanHash})["publishHubriseCatalog"])
		assert.True(t, res.Published)
		assert.Equal(t, 2, r.srv.CatalogPuts())
	})
}

func TestPreviewListsForeignItemsAndRemoveForeignChangesThePlan(t *testing.T) {
	r := newPublishGQLRig(t)
	keep := r.preview(false)
	drop := r.preview(true)
	assert.Positive(t, keep.Foreign)
	assert.Equal(t, keep.Foreign, drop.Foreign)
	assert.Positive(t, drop.Removed, "the foreign products are removed")
	assert.Zero(t, keep.Removed)
	assert.NotEqual(t, keep.PlanHash, drop.PlanHash)
	code, _ := r.fail(publishDoc, map[string]any{"f": true, "h": keep.PlanHash})
	assert.Equal(t, "HUBRISE_PREVIEW_STALE", code)
}

func TestPublishNeedsConfirmationForMassRemovals(t *testing.T) {
	r := newPublishGQLRig(t)
	first := r.preview(false)
	r.run(publishDoc, map[string]any{"h": first.PlanHash})
	drop := r.preview(true)
	require.True(t, drop.RemovalsNeedConfirm, "the foreign products are most of the published SKUs: %+v", drop.Removed)
	code, _ := r.fail(publishDoc, map[string]any{"f": true, "h": drop.PlanHash})
	assert.Equal(t, "HUBRISE_PUBLISH_CONFIRM_REQUIRED", code)
	res := decode[publishResultDoc](t, r.run(publishDoc, map[string]any{"f": true, "c": true, "h": drop.PlanHash})["publishHubriseCatalog"])
	assert.True(t, res.Published)
}

func TestPublishErrors(t *testing.T) {
	t.Run("not in PUBLISH mode", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		code, _ := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_NOT_PUBLISH_MODE", code)
		code, _ = r.fail(publishDoc, nil)
		assert.Equal(t, "HUBRISE_NOT_PUBLISH_MODE", code)
	})
	t.Run("no write scope", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`UPDATE hubrise_connection SET scope = $1`, hubtest.DefaultScope)
		code, _ := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_RECONNECT_REQUIRED", code)
	})
	t.Run("not enabled", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`UPDATE hubrise_connection SET enabled = false`)
		code, _ := r.fail(publishDoc, nil)
		assert.Equal(t, "HUBRISE_NOT_ENABLED", code)
	})
	t.Run("relink required", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
		code, _ := r.fail(publishDoc, nil)
		assert.Equal(t, "HUBRISE_RELINK_REQUIRED", code)
	})
	t.Run("account catalog", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.srv.SetCatalogLevel(hubrisetest.LevelAccount)
		code, _ := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_ACCOUNT_CATALOG", code)
	})
	t.Run("rate limited", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/", Status: 429, Times: 1, RetryAfter: 7 * time.Second})
		code, ext := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_RATE_LIMITED", code)
		assert.EqualValues(t, 7, ext["retryAfterSeconds"])
	})
	t.Run("HubRise down", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/", Status: 500, Times: 1})
		code, ext := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_UPSTREAM", code)
		assert.Equal(t, "server", ext["kind"])
	})
	t.Run("a menu with blockers", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`INSERT INTO product_choice_groups (id, product_id, min_selections, max_selections, sort_order) VALUES (gen_random_uuid(), $1, 0, 1, 0)`, r.Fixtures.SalmonSushi.ID)
		prev := r.preview(false)
		require.NotEmpty(t, prev.Blockers)
		assert.Equal(t, "EMPTY_OPTION_LIST", prev.Blockers[0].Code)
		code, ext := r.fail(publishDoc, map[string]any{"h": prev.PlanHash})
		assert.Equal(t, "HUBRISE_PUBLISH_BLOCKED", code)
		assert.Contains(t, ext["blockers"], "EMPTY_OPTION_LIST")
	})
	t.Run("an empty menu", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`DELETE FROM order_product`)
		r.exec(`DELETE FROM products`)
		code, _ := r.fail(previewDoc, nil)
		assert.Equal(t, "HUBRISE_EMPTY_MENU", code)
		code, _ = r.fail(publishDoc, map[string]any{"h": "x"})
		assert.Equal(t, "HUBRISE_EMPTY_MENU", code)
	})
	t.Run("another replica is publishing", func(t *testing.T) {
		r := newPublishGQLRig(t)
		prev := r.preview(false)
		r.run(publishDoc, map[string]any{"h": prev.PlanHash})
		_, ok, err := r.mod.PublishState.ClaimNow(t.Context(), "replica-b:x", time.Now())
		require.NoError(t, err)
		require.True(t, ok)
		res := decode[publishResultDoc](t, r.run(publishDoc, nil)["publishHubriseCatalog"])
		assert.True(t, res.Queued)
		assert.False(t, res.Published)
		assert.True(t, res.Status.Pending)
	})
}

func TestAcknowledgingAnExternalChange(t *testing.T) {
	r := newPublishGQLRig(t)
	require.NoError(t, r.mod.PublishState.SetExternalChange(t.Context(), ptrTime(time.Now())))
	assert.NotNil(t, r.publishStatus().Publish.ExternalChangeAt)
	data := r.run(`mutation { acknowledgeHubriseExternalChange { publish { externalChangeAt } } }`, nil)
	assert.JSONEq(t, `{"publish":{"externalChangeAt":null}}`, string(data["acknowledgeHubriseExternalChange"]))
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestStatusReportsAPublishError(t *testing.T) {
	r := newPublishGQLRig(t)
	require.NoError(t, r.mod.PublishState.Block(t.Context(), "", hubDomain.PublishRejected, "HubRise refused the catalog", time.Now()))
	st := r.publishStatus()
	require.NotNil(t, st.Publish.LastErrorCode)
	assert.Equal(t, "REJECTED", *st.Publish.LastErrorCode)
	assert.Equal(t, "HubRise refused the catalog", *st.Publish.LastError)
	assert.NotNil(t, st.Publish.LastErrorAt)
	require.NoError(t, r.mod.PublishState.Block(t.Context(), "", hubDomain.PublishRemovalsNeedConfirm, "confirm", time.Now()))
	st = r.publishStatus()
	assert.Equal(t, "REMOVALS_NEED_CONFIRM", *st.Publish.LastErrorCode)
}

func TestProductPublishInfo(t *testing.T) {
	q := fmt.Sprintf(`{ product(id: "%s") { hubrisePublish { state publishedAt ref } } }`, "%s")
	r := newPublishGQLRig(t)
	doc := fmt.Sprintf(q, r.Fixtures.SalmonSushi.ID)

	data := r.run(doc, nil)
	var before struct {
		Info *struct {
			State, Ref  string
			PublishedAt *string
		} `json:"hubrisePublish"`
	}
	require.NoError(t, json.Unmarshal(data["product"], &before))
	require.NotNil(t, before.Info)
	assert.Equal(t, "NOT_PUBLISHED", before.Info.State)
	assert.Empty(t, before.Info.Ref)

	prev := r.preview(false)
	r.run(publishDoc, map[string]any{"h": prev.PlanHash})
	var after struct {
		Info *struct {
			State, Ref  string
			PublishedAt *string
		} `json:"hubrisePublish"`
	}
	require.NoError(t, json.Unmarshal(r.run(doc, nil)["product"], &after))
	assert.Equal(t, "PUBLISHED", after.Info.State)
	assert.NotEmpty(t, after.Info.Ref)
	assert.NotNil(t, after.Info.PublishedAt)

	r.exec(`UPDATE products SET price = price + 1 WHERE id = $1`, r.Fixtures.SalmonSushi.ID)
	require.NoError(t, json.Unmarshal(r.run(doc, nil)["product"], &after))
	assert.Equal(t, "PENDING", after.Info.State)

	t.Run("a list of products costs one read", func(t *testing.T) {
		list := r.run(`{ products { id hubrisePublish { state } } }`, nil)
		var products []struct {
			ID   string                  `json:"id"`
			Info *struct{ State string } `json:"hubrisePublish"`
		}
		require.NoError(t, json.Unmarshal(list["products"], &products))
		require.Greater(t, len(products), 1)
		for _, p := range products {
			require.NotNil(t, p.Info, p.ID)
		}
	})

	t.Run("null outside PUBLISH, and for a customer", func(t *testing.T) {
		plain := newHubriseAdminRig(t, true)
		var none struct {
			Info *struct{ State string } `json:"hubrisePublish"`
		}
		require.NoError(t, json.Unmarshal(plain.run(fmt.Sprintf(q, plain.Fixtures.SalmonSushi.ID), nil)["product"], &none))
		assert.Nil(t, none.Info)
		resp := plain.ask(plain.custTok, fmt.Sprintf(q, plain.Fixtures.SalmonSushi.ID), nil)
		require.NotEmpty(t, resp.Errors)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
	})
}

func TestPreviewListsAtMostFiveHundredChanges(t *testing.T) {
	r := newPublishGQLRig(t)
	r.exec(`INSERT INTO products (id, category_id, price, code, slug, vat_category, is_visible, is_available)
		SELECT gen_random_uuid(), category_id, 1, 'BULK' || g, 'bulk-' || g, 'food', true, true
		FROM generate_series(1, 520) g, (SELECT category_id FROM products LIMIT 1) c`)
	prev := r.preview(false)
	assert.True(t, prev.Truncated)
	assert.Len(t, prev.Changes, 500)
	assert.Greater(t, prev.Added, 500)
}

// The field resolver also works outside a request with its DataLoader, and every failure of the
// stores behind the publish API is an error of the request, not a crash.
func TestPublishAPIReportsWhatItCannotRead(t *testing.T) {
	t.Run("the product field without a loader", func(t *testing.T) {
		r := newPublishGQLRig(t)
		ctx := r.ctxFor(r.Fixtures.AdminUser.ID.String(), true, "en")
		info, err := r.Resolver.Product().HubrisePublish(ctx, &model.Product{ID: r.Fixtures.SalmonSushi.ID})
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, model.HubrisePublishStateNotPublished, info.State)

		unknown, err := r.Resolver.Product().HubrisePublish(ctx, &model.Product{ID: uuid.New()})
		require.NoError(t, err)
		assert.Nil(t, unknown, "a product the menu does not have has no state")
	})

	t.Run("without the integration the field is null", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		info, err := env.Resolver.Product().HubrisePublish(env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en"), &model.Product{ID: env.Fixtures.SalmonSushi.ID})
		require.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("the menu cannot be read", func(t *testing.T) {
		r := newPublishGQLRig(t)
		ctx := r.ctxFor(r.Fixtures.AdminUser.ID.String(), true, "en")
		r.exec(`ALTER TABLE hubrise_catalog_links RENAME TO hubrise_catalog_links_away`)
		_, err := r.Resolver.Product().HubrisePublish(ctx, &model.Product{ID: r.Fixtures.SalmonSushi.ID})
		assert.Error(t, err, "no loader")
		resp := r.ask(r.adminTok, fmt.Sprintf(`{ product(id: "%s") { hubrisePublish { state } } }`, r.Fixtures.SalmonSushi.ID), nil)
		assert.NotEmpty(t, resp.Errors, "with the loader")
	})

	t.Run("the publisher row cannot be written", func(t *testing.T) {
		r := newPublishGQLRig(t)
		r.exec(`ALTER TABLE hubrise_catalog_publish RENAME TO hubrise_catalog_publish_away`)
		resp := r.ask(r.adminTok, `mutation { acknowledgeHubriseExternalChange { connected } }`, nil)
		assert.NotEmpty(t, resp.Errors)
	})
}
