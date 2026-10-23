package graphql_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise"
	hubApplication "tsb-service/internal/modules/hubrise/application"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/utils"
)

// hubriseAdminRig is the dashboard's HubRise API wired the way the composition root does it: the
// admin service on the module, the mock HubRise behind it, the resolver holding the service.
type hubriseAdminRig struct {
	*covEnv
	t        *testing.T
	srv      *hubrisetest.Server
	mod      *hubrise.Module
	admin    *hubApplication.Admin
	adminTok string
	custTok  string
}

const hubriseAPIBase = "https://api.example.test/api/v1"

func (e *covEnv) withHubriseAdmin(t *testing.T, connect bool, seed ...hubtest.SeedOption) *hubriseAdminRig {
	t.Helper()
	srv := hubrisetest.New(t)
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: hubDomain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, e.DB.DB, nil)
	require.NoError(t, err)
	if connect {
		opts := append([]hubtest.SeedOption{
			hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
			hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)), hubtest.WithCustomerList("", ""),
			hubtest.WithFeatures(true, true, true, false, false),
		}, seed...)
		hubtest.SeedConnection(t, e.DB.DB, opts...)
	}
	clients := mockClients{c: srv.NewClient(t)}
	push := mod.NewPush(hubrise.PushOptions{Clients: clients})
	catalog := mod.NewCatalog(push, hubrise.CatalogOptions{Clients: clients, Notifier: e.Resolver})
	inventory := mod.NewInventory(push, hubrise.InventoryOptions{Clients: clients, Notifier: e.Resolver, Drift: catalog.Drift})
	admin, err := mod.NewAdmin(push, catalog, hubrise.AdminOptions{
		Clients: clients, OAuthHTTP: srv.HTTPClient(), APIBaseURL: hubriseAPIBase, Inventory: inventory.Service,
	})
	require.NoError(t, err)
	e.Resolver.HubriseAdmin = admin

	adminTok, err := testhelpers.GenerateTestAccessToken(e.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	_, custTok := testhelpers.SeedCustomer(t, e.DB.DB, "hubadmin")
	return &hubriseAdminRig{covEnv: e, t: t, srv: srv, mod: mod, admin: admin, adminTok: adminTok, custTok: custTok}
}

func newHubriseAdminRig(t *testing.T, connect bool, seed ...hubtest.SeedOption) *hubriseAdminRig {
	t.Helper()
	return setupCovEnv(t, covOptions{}).withHubriseAdmin(t, connect, seed...)
}

func (r *hubriseAdminRig) ask(token, query string, vars map[string]any) graphqlResponseWithExtensions {
	r.t.Helper()
	return gqlAs(r.t, r.TestContext, token, "en", query, vars)
}

// run asks as the admin and fails on any error.
func (r *hubriseAdminRig) run(query string, vars map[string]any) map[string]json.RawMessage {
	r.t.Helper()
	resp := r.ask(r.adminTok, query, vars)
	require.Empty(r.t, resp.Errors, "%s: %+v", query, resp.Errors)
	var data map[string]json.RawMessage
	require.NoError(r.t, json.Unmarshal(resp.Data, &data))
	return data
}

// fail asks as the admin, expects exactly one error and returns its code and extensions.
func (r *hubriseAdminRig) fail(query string, vars map[string]any) (string, map[string]any) {
	r.t.Helper()
	resp := r.ask(r.adminTok, query, vars)
	require.Len(r.t, resp.Errors, 1, "%s: %+v", query, resp.Errors)
	code, _ := resp.Errors[0].Extensions["code"].(string)
	return code, resp.Errors[0].Extensions
}

func (r *hubriseAdminRig) exec(q string, args ...any) {
	r.t.Helper()
	_, err := r.DB.DB.ExecContext(r.t.Context(), q, args...)
	require.NoError(r.t, err, q)
}

// insertOrder inserts a minimal order of the user and returns its id.
func insertOrder(t *testing.T, tc *TestContext, user uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, tc.DB.DB.GetContext(t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
		VALUES ($1, 'CONFIRMED', 'PICKUP', false, 10, 'fr') RETURNING id`, user))
	return id
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

const statusFields = `available connected enabled accountId accountName locationId locationName catalogId catalogName
	customerListId customerListName
	features { pushOrders syncStatus syncInventory syncCatalog }
	syncCustomers catalogAutoImport catalogUpdateAvailable catalogPendingReview autoRefundOnCancel
	catalogLanguage variantRef channelName deliveryChargeRef paymentFeeChargeRef importImages
	lastCatalogSyncAt lastEventAt authErrorAt lastError relinkRequired
	backlog { pending parked } unlinkedProducts attention`

type statusDoc struct {
	Available              bool    `json:"available"`
	Connected              bool    `json:"connected"`
	Enabled                bool    `json:"enabled"`
	AccountID              *string `json:"accountId"`
	LocationID             *string `json:"locationId"`
	LocationName           *string `json:"locationName"`
	CatalogID              *string `json:"catalogId"`
	CatalogName            *string `json:"catalogName"`
	CustomerListID         *string `json:"customerListId"`
	SyncCustomers          bool    `json:"syncCustomers"`
	CatalogAutoImport      bool    `json:"catalogAutoImport"`
	CatalogUpdateAvailable bool    `json:"catalogUpdateAvailable"`
	CatalogPendingReview   bool    `json:"catalogPendingReview"`
	AutoRefundOnCancel     bool    `json:"autoRefundOnCancel"`
	CatalogLanguage        string  `json:"catalogLanguage"`
	VariantRef             *string `json:"variantRef"`
	ChannelName            *string `json:"channelName"`
	ImportImages           bool    `json:"importImages"`
	AuthErrorAt            *string `json:"authErrorAt"`
	LastError              *string `json:"lastError"`
	RelinkRequired         bool    `json:"relinkRequired"`
	Features               struct {
		PushOrders, SyncStatus, SyncInventory, SyncCatalog bool
	} `json:"features"`
	Backlog          struct{ Pending, Parked int } `json:"backlog"`
	UnlinkedProducts int                           `json:"unlinkedProducts"`
	Attention        int                           `json:"attention"`
}

func (r *hubriseAdminRig) status() statusDoc {
	r.t.Helper()
	data := r.run(`{ hubriseStatus { `+statusFields+` } }`, nil)
	return decode[statusDoc](r.t, data["hubriseStatus"])
}

// ---- availability ----

const allOperations = `
	query Status { hubriseStatus { available } }
	query Outbox { hubriseOutbox { id } }
	mutation Start { startHubriseConnection }
	mutation Disconnect { disconnectHubrise(forgetLinks: true) { available } }
	mutation Settings { updateHubriseSettings(input: { enabled: true }) { available } }
	mutation Preview { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }
	mutation Sync { syncHubriseCatalog(mode: FULL) { applied } }
	mutation Forget { forgetHubriseLinks }
	mutation Inventory { syncHubriseInventory }
	mutation Link { linkHubriseProduct(productId: "%s", skuRef: "X") }
	mutation Retry { retryHubriseOutbox }
`

// operations are the eleven operations of the HubRise admin API, one document each.
func operations(productID uuid.UUID) map[string]string {
	out := map[string]string{}
	for _, block := range strings.Split(strings.TrimSpace(fmt.Sprintf(allOperations, productID)), "\n") {
		block = strings.TrimSpace(block)
		fields := strings.Fields(block)
		out[fields[1]] = block
	}
	return out
}

func TestHubriseIsUnavailableOnAServerWithoutIt(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	tok, err := testhelpers.GenerateTestAccessToken(env.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	require.Nil(t, env.Resolver.HubriseAdmin)

	resp := gqlAs(t, env.TestContext, tok, "en", `{ hubriseStatus { `+statusFields+` } }`, nil)
	require.Empty(t, resp.Errors)
	var data struct {
		Status statusDoc `json:"hubriseStatus"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.False(t, data.Status.Available)
	assert.False(t, data.Status.Connected)
	assert.False(t, data.Status.Enabled)
	assert.Equal(t, "fr", data.Status.CatalogLanguage)
	assert.Zero(t, data.Status.Backlog)

	for name, doc := range operations(env.Fixtures.SalmonSushi.ID) {
		if name == "Status" {
			continue
		}
		resp := gqlAs(t, env.TestContext, tok, "en", doc, nil)
		require.Len(t, resp.Errors, 1, name)
		assert.Equal(t, string(apperr.CodeHubriseNotConfigured), resp.Errors[0].Extensions["code"], name)
	}

	t.Run("the HubRise fields of products and orders are empty", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, tok, "en", fmt.Sprintf(
			`{ product(id: "%s") { hubriseManaged hubriseLockedFields } order(id: "%s") { hubrise { pushState } } }`,
			env.Fixtures.SalmonSushi.ID, insertOrder(t, env.TestContext, env.Fixtures.RegularUser.ID)), nil)
		require.Empty(t, resp.Errors)
		assert.JSONEq(t, `{"product":{"hubriseManaged":false,"hubriseLockedFields":[]},"order":{"hubrise":null}}`, string(resp.Data))
	})
}

func TestEveryHubriseOperationIsAdminOnly(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	expired, err := testhelpers.GenerateExpiredToken(r.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)

	for name, doc := range operations(r.Fixtures.SalmonSushi.ID) {
		for who, c := range map[string]struct{ token, code string }{
			"anonymous":     {"", "UNAUTHENTICATED"},
			"customer":      {r.custTok, "FORBIDDEN"},
			"expired admin": {expired, "UNAUTHENTICATED"},
		} {
			t.Run(name+" as "+who, func(t *testing.T) {
				resp := r.ask(c.token, doc, nil)
				require.NotEmpty(t, resp.Errors)
				assert.Equal(t, c.code, resp.Errors[0].Extensions["code"])
			})
		}
	}
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_oauth_states`), "nothing ran")
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))
	assert.True(t, r.status().Connected, "and the connection is untouched")
}

func TestTheHubriseFieldsOfProductsAndOrdersAreProtected(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	customer, token := testhelpers.SeedCustomer(t, r.DB.DB, "ownorder")
	orderID := insertOrder(t, r.TestContext, customer)

	product := fmt.Sprintf(`{ product(id: "%s") { id hubriseManaged } }`, r.Fixtures.SalmonSushi.ID)
	assert.Equal(t, "UNAUTHENTICATED", r.ask("", product, nil).Errors[0].Extensions["code"])
	assert.Equal(t, "FORBIDDEN", r.ask(token, product, nil).Errors[0].Extensions["code"])
	locked := fmt.Sprintf(`{ product(id: "%s") { hubriseLockedFields } }`, r.Fixtures.SalmonSushi.ID)
	assert.Equal(t, "FORBIDDEN", r.ask(token, locked, nil).Errors[0].Extensions["code"])

	// The menu itself stays public: asking for none of the HubRise fields changes nothing.
	menu := r.ask("", `{ products { id name price isAvailable choices { isAvailable } } }`, nil)
	require.Empty(t, menu.Errors)

	// A customer reads their own order but not its HubRise side.
	own := fmt.Sprintf(`{ myOrder(id: "%s") { id hubrise { pushState } } }`, orderID)
	resp := r.ask(token, own, nil)
	require.NotEmpty(t, resp.Errors)
	assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
	assert.Contains(t, resp.Errors[0].Path, "hubrise")
	plain := r.ask(token, fmt.Sprintf(`{ myOrder(id: "%s") { id } }`, orderID), nil)
	require.Empty(t, plain.Errors)
}

// ---- status ----

func TestHubriseStatusInEveryState(t *testing.T) {
	r := newHubriseAdminRig(t, false)

	t.Run("never connected", func(t *testing.T) {
		st := r.status()
		assert.True(t, st.Available)
		assert.False(t, st.Connected)
		assert.False(t, st.Enabled)
		assert.Nil(t, st.LocationID)
		assert.Equal(t, "fr", st.CatalogLanguage)
		assert.Zero(t, st.UnlinkedProducts, "nothing to link to")
	})

	hubtest.SeedConnection(t, r.DB.DB, hubtest.WithToken(hubrisetest.Token),
		hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID), hubtest.WithCustomerList("cl-1", "Clients"),
		hubtest.WithFeatures(true, false, true, false, true))

	t.Run("connected and enabled", func(t *testing.T) {
		st := r.status()
		assert.True(t, st.Connected)
		assert.True(t, st.Enabled)
		assert.Equal(t, hubrisetest.LocationID, *st.LocationID)
		assert.Equal(t, hubrisetest.CatalogID, *st.CatalogID)
		assert.NotNil(t, st.AccountID)
		assert.Equal(t, "cl-1", *st.CustomerListID)
		assert.True(t, st.Features.PushOrders)
		assert.False(t, st.Features.SyncStatus)
		assert.True(t, st.Features.SyncInventory)
		assert.False(t, st.Features.SyncCatalog)
		assert.True(t, st.SyncCustomers)
		assert.False(t, st.AutoRefundOnCancel, "off by default")
		assert.Positive(t, st.UnlinkedProducts, "the fixtures' products have no SKU yet")
	})

	t.Run("paused", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET enabled = FALSE`)
		st := r.status()
		assert.True(t, st.Connected)
		assert.False(t, st.Enabled)
		r.exec(`UPDATE hubrise_connection SET enabled = TRUE`)
	})

	t.Run("refused by HubRise", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET auth_error_at = now(), last_error = 'unauthorized'`)
		st := r.status()
		assert.NotNil(t, st.AuthErrorAt)
		assert.Equal(t, "unauthorized", *st.LastError)
		r.exec(`UPDATE hubrise_connection SET auth_error_at = NULL, last_error = NULL`)
	})

	t.Run("a relink is needed", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
		assert.True(t, r.status().RelinkRequired)
		r.exec(`UPDATE hubrise_connection SET relink_required_at = NULL`)
	})

	t.Run("an import waits for a review", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET last_error = $1`, hubDomain.PendingReviewPrefix+"6 of 8 products would be retired")
		st := r.status()
		assert.True(t, st.CatalogPendingReview)
		assert.Equal(t, "6 of 8 products would be retired", *st.LastError, "the marker is not shown")
		r.exec(`UPDATE hubrise_connection SET last_error = NULL`)
	})

	t.Run("HubRise has a newer catalog", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET catalog_update_available_at = now()`)
		assert.True(t, r.status().CatalogUpdateAvailable)
	})

	t.Run("work in progress", func(t *testing.T) {
		r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state) VALUES
			('inventory', 'a', '{}', 'pending'), ('inventory', 'b', '{}', 'parked'), ('inventory', 'c', '{}', 'parked')`)
		r.exec(`INSERT INTO hubrise_orders (order_id, push_state, needs_attention) VALUES ($1, 'sent', 'TOTAL_MISMATCH')`, insertOrder(t, r.TestContext, r.Fixtures.RegularUser.ID))
		st := r.status()
		assert.Equal(t, 1, st.Backlog.Pending)
		assert.Equal(t, 2, st.Backlog.Parked)
		assert.Equal(t, 1, st.Attention)
	})

	t.Run("disconnected", func(t *testing.T) {
		r.run(`mutation { disconnectHubrise { connected enabled } }`, nil)
		st := r.status()
		assert.False(t, st.Connected)
		assert.False(t, st.Enabled)
		assert.Zero(t, st.UnlinkedProducts)
		assert.Equal(t, 2, st.Backlog.Parked, "what was waiting is still there")
	})
}

func TestNoSecretIsEverReadableThroughTheAPI(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	schema := r.run(`{ __type(name: "HubriseStatus") { fields { name } } }`, nil)
	names := decode[struct{ Fields []struct{ Name string } }](t, schema["__type"])
	for _, f := range names.Fields {
		assert.NotContains(t, strings.ToLower(f.Name), "token")
		assert.NotContains(t, strings.ToLower(f.Name), "secret")
	}

	resp := r.ask(r.adminTok, `{ hubriseStatus { `+statusFields+` } hubriseOutbox { id lastError } }`, nil)
	assert.NotContains(t, string(resp.Data), hubrisetest.Token)
	assert.NotContains(t, string(resp.Data), hubrisetest.ClientSecret)
	// The row holds the token only sealed.
	var sealed []byte
	require.NoError(t, r.DB.DB.GetContext(t.Context(), &sealed, `SELECT access_token_enc FROM hubrise_connection`))
	assert.NotContains(t, string(sealed), hubrisetest.Token)
}

// ---- connection ----

func browse(t *testing.T, srv *hubrisetest.Server, authURL string) (state, code string) {
	t.Helper()
	client := *srv.HTTPClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(authURL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return loc.Query().Get("state"), loc.Query().Get("code")
}

func TestStartingAConnectionGivesTheAuthorizationURLOfTheAdmin(t *testing.T) {
	r := newHubriseAdminRig(t, false)

	data := r.run(`mutation { startHubriseConnection }`, nil)
	authURL := decode[string](t, data["startHubriseConnection"])

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.ClientID, u.Query().Get("client_id"))
	assert.Equal(t, hubriseAPIBase+"/hubrise/oauth/callback", u.Query().Get("redirect_uri"))
	for _, scope := range []string{"orders.write", "customer_list.write", "catalog.read", "inventory.read", "inventory.write"} {
		assert.Contains(t, u.Query().Get("scope"), scope)
	}
	var admin uuid.UUID
	require.NoError(t, r.DB.DB.GetContext(t.Context(), &admin, `SELECT admin_user_id FROM hubrise_oauth_states WHERE state = $1`, u.Query().Get("state")))
	assert.Equal(t, r.Fixtures.AdminUser.ID, admin, "the state is bound to the admin who asked")
}

func TestDisconnectThenReconnectLosesNothing(t *testing.T) {
	r := newHubriseAdminRig(t, true, hubtest.WithFeatures(true, true, true, false, false))
	r.exec(`UPDATE hubrise_connection SET channel_name = 'Web', catalog_language = 'nl'`)
	r.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'MAKI-SAUMON-6', 'MAKI-SAUMON-6', 'manual')`, r.Fixtures.SalmonSushi.ID)

	off := decode[statusDoc](t, r.run(`mutation { disconnectHubrise { `+statusFields+` } }`, nil)["disconnectHubrise"])
	assert.False(t, off.Connected)
	assert.False(t, off.Enabled)
	assert.True(t, r.srv.TokenRevoked(hubrisetest.Token), "HubRise no longer honours the token")
	assert.Equal(t, 1, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`), "the links stay")
	var sealed *[]byte
	require.NoError(t, r.DB.DB.GetContext(t.Context(), &sealed, `SELECT access_token_enc FROM hubrise_connection`))
	assert.Nil(t, sealed, "the token is gone")

	// The admin authorizes again.
	authURL := decode[string](t, r.run(`mutation { startHubriseConnection }`, nil)["startHubriseConnection"])
	state, code := browse(t, r.srv, authURL)
	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.False(t, res.RelinkRequired, "same location and catalog")

	on := r.status()
	assert.True(t, on.Connected)
	assert.Equal(t, "Web", *on.ChannelName, "the settings were kept")
	assert.Equal(t, "nl", on.CatalogLanguage)
	assert.Equal(t, 1, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))
	assert.NotNil(t, r.srv.Callback(hubrisetest.Token), "the callback is registered again")
}

func TestDisconnectCanForgetTheLinksAndNeedsAConnectionRow(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	r.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'K', 'K', 'manual')`, r.Fixtures.SalmonSushi.ID)

	r.run(`mutation { disconnectHubrise(forgetLinks: true) { connected } }`, nil)
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))

	r.exec(`DELETE FROM hubrise_connection`)
	code, _ := r.fail(`mutation { disconnectHubrise { connected } }`, nil)
	assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
}

// ---- settings ----

func TestUpdatingTheSettings(t *testing.T) {
	r := newHubriseAdminRig(t, true)

	st := decode[statusDoc](t, r.run(`mutation { updateHubriseSettings(input: {
		syncStatus: false, autoRefundOnCancel: true, importImages: true, catalogLanguage: " NL ",
		channelName: "  Web shop ", variantRef: "size", pushOrders: false, syncCustomers: true
	}) { `+statusFields+` } }`, nil)["updateHubriseSettings"])

	assert.False(t, st.Features.SyncStatus)
	assert.False(t, st.Features.PushOrders)
	assert.True(t, st.AutoRefundOnCancel)
	assert.True(t, st.ImportImages)
	assert.True(t, st.SyncCustomers)
	assert.Equal(t, "nl", st.CatalogLanguage)
	assert.Equal(t, "Web shop", *st.ChannelName)
	assert.Equal(t, "size", *st.VariantRef)

	st = decode[statusDoc](t, r.run(`mutation { updateHubriseSettings(input: { channelName: "" }) { `+statusFields+` } }`, nil)["updateHubriseSettings"])
	assert.Nil(t, st.ChannelName, "an empty text clears the option")
	assert.Equal(t, "size", *st.VariantRef, "and leaves the others")
}

func TestPushingOrdersBecomingEffectiveOnlyPushesLaterOrders(t *testing.T) {
	r := newHubriseAdminRig(t, true, hubtest.WithFeatures(false, true, false, false, false))
	r.exec(`UPDATE hubrise_connection SET push_orders_since = now() - interval '30 days'`)

	r.run(`mutation { updateHubriseSettings(input: { pushOrders: true }) { enabled } }`, nil)

	var since time.Time
	require.NoError(t, r.DB.DB.GetContext(t.Context(), &since, `SELECT push_orders_since FROM hubrise_connection`))
	assert.WithinDuration(t, time.Now(), since, time.Minute, "the history is not replayed")
}

func TestSettingsAreValidated(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	for field, input := range map[string]string{
		"catalogLanguage":     `catalogLanguage: "de"`,
		"variantRef":          `variantRef: "` + strings.Repeat("x", 101) + `"`,
		"channelName":         `channelName: "a\u0007b"`,
		"deliveryChargeRef":   `deliveryChargeRef: "` + strings.Repeat("y", 200) + `"`,
		"paymentFeeChargeRef": `paymentFeeChargeRef: "` + strings.Repeat("z", 101) + `"`,
	} {
		code, ext := r.fail(`mutation { updateHubriseSettings(input: { `+input+` }) { enabled } }`, nil)
		assert.Equal(t, "USER_ERROR", code, field)
		assert.Equal(t, field, ext["field"], field)
	}
}

func TestSettingsNeedAConnection(t *testing.T) {
	r := newHubriseAdminRig(t, false)
	code, _ := r.fail(`mutation { updateHubriseSettings(input: { syncStatus: true }) { enabled } }`, nil)
	assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
}

func TestEnablingCatalogSyncNeedsAFreshPreview(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	enable := `mutation { updateHubriseSettings(input: { syncCatalog: true }) { features { syncCatalog } } }`

	code, _ := r.fail(enable, nil)
	assert.Equal(t, "HUBRISE_PREVIEW_REQUIRED", code, "no preview yet")

	diff := decode[struct {
		CatalogHash string `json:"catalogHash"`
	}](t, r.run(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`, nil)["previewHubriseCatalogSync"])
	assert.NotEmpty(t, diff.CatalogHash)

	got := decode[statusDoc](t, r.run(`mutation { updateHubriseSettings(input: { syncCatalog: true }) { `+statusFields+` } }`, nil)["updateHubriseSettings"])
	assert.True(t, got.Features.SyncCatalog)

	t.Run("a catalog that changed since is not the one previewed", func(t *testing.T) {
		r.run(`mutation { updateHubriseSettings(input: { syncCatalog: false }) { enabled } }`, nil)
		require.NoError(t, r.srv.ReplaceCatalog(menuOf(3)))
		code, _ := r.fail(enable, nil)
		assert.Equal(t, "HUBRISE_PREVIEW_REQUIRED", code)
	})

	t.Run("a link-only preview does not count", func(t *testing.T) {
		r.run(`mutation { previewHubriseCatalogSync(mode: LINK_ONLY) { catalogHash } }`, nil)
		code, _ := r.fail(enable, nil)
		assert.Equal(t, "HUBRISE_PREVIEW_REQUIRED", code)
	})
}

func TestSwitchingOnWhileARelinkIsPendingIsRefusedUntilTheLinksAreForgotten(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	r.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'OLD', 'OLD', 'manual')`, r.Fixtures.SalmonSushi.ID)
	r.exec(`UPDATE hubrise_connection SET enabled = FALSE, relink_required_at = now()`)
	enable := `mutation { updateHubriseSettings(input: { enabled: true }) { enabled relinkRequired } }`

	code, _ := r.fail(enable, nil)
	assert.Equal(t, "HUBRISE_RELINK_REQUIRED", code)

	n := decode[int](t, r.run(`mutation { forgetHubriseLinks }`, nil)["forgetHubriseLinks"])
	assert.Equal(t, 1, n)

	st := decode[struct{ Enabled, RelinkRequired bool }](t, r.run(enable, nil)["updateHubriseSettings"])
	assert.True(t, st.Enabled)
	assert.False(t, st.RelinkRequired)
}

// ---- catalog ----

// menuOf is a HubRise menu of n single-SKU products in one category.
func menuOf(n int) pkghubrise.CatalogData {
	d := pkghubrise.CatalogData{Categories: []pkghubrise.Category{{Ref: "C", Name: "Carte"}}}
	for i := range n {
		d.Products = append(d.Products, pkghubrise.Product{
			Ref: fmt.Sprintf("P%d", i), CategoryRef: "C", Name: fmt.Sprintf("Produit %d", i),
			TaxRate: &pkghubrise.TaxRate{Collection: "6", Delivery: "6", EatIn: "12"},
			Skus:    []pkghubrise.Sku{{Ref: fmt.Sprintf("S%d", i), Price: pkghubrise.Money("3 EUR")}},
		})
	}
	return d
}

const diffFields = `catalogHash unchanged retiringProducts linkedProducts retireNeedsConfirm
	changes { entity action key name fields } warnings { code key message } conflicts { code key message }`

type diffDoc struct {
	CatalogHash        string `json:"catalogHash"`
	Unchanged          int    `json:"unchanged"`
	RetiringProducts   int    `json:"retiringProducts"`
	LinkedProducts     int    `json:"linkedProducts"`
	RetireNeedsConfirm bool   `json:"retireNeedsConfirm"`
	Changes            []struct {
		Entity, Action, Key, Name string
		Fields                    []string
	} `json:"changes"`
	Warnings  []struct{ Code, Message string } `json:"warnings"`
	Conflicts []struct{ Code, Message string } `json:"conflicts"`
}

func TestPreviewingAndImportingTheCatalog(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	require.NoError(t, r.srv.ReplaceCatalog(menuOf(3)))

	preview := decode[diffDoc](t, r.run(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { `+diffFields+` } }`, nil)["previewHubriseCatalogSync"])
	creates := 0
	for _, c := range preview.Changes {
		if c.Entity == "PRODUCT" && c.Action == "CREATE" {
			creates++
		}
	}
	assert.Equal(t, 3, creates)
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`), "a preview writes nothing")
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM products WHERE slug LIKE 'produit-%'`))

	res := decode[struct {
		Mode          string
		Applied       bool
		PendingReview bool
		FailedBatches []string
		Diff          diffDoc
	}](t, r.run(`mutation { syncHubriseCatalog(mode: FULL) { mode applied pendingReview failedBatches diff { `+diffFields+` } } }`, nil)["syncHubriseCatalog"])
	assert.Equal(t, "FULL", res.Mode)
	assert.True(t, res.Applied)
	assert.False(t, res.PendingReview)
	assert.Empty(t, res.FailedBatches)
	assert.Equal(t, 3, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product'`))

	linkOnly := decode[diffDoc](t, r.run(`mutation { previewHubriseCatalogSync(mode: LINK_ONLY) { `+diffFields+` } }`, nil)["previewHubriseCatalogSync"])
	assert.NotEmpty(t, linkOnly.CatalogHash)
}

func TestARetirementOfMostOfTheMenuNeedsConfirmation(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	require.NoError(t, r.srv.ReplaceCatalog(menuOf(8)))
	r.run(`mutation { syncHubriseCatalog(mode: FULL) { applied } }`, nil)
	require.NoError(t, r.srv.ReplaceCatalog(menuOf(2)))

	preview := decode[diffDoc](t, r.run(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { `+diffFields+` } }`, nil)["previewHubriseCatalogSync"])
	assert.True(t, preview.RetireNeedsConfirm)
	assert.Equal(t, 6, preview.RetiringProducts)

	code, ext := r.fail(`mutation { syncHubriseCatalog(mode: FULL) { applied } }`, nil)
	assert.Equal(t, "HUBRISE_RETIRE_CONFIRM_REQUIRED", code)
	assert.EqualValues(t, 6, ext["retiring"])
	assert.EqualValues(t, 8, ext["linked"])
	assert.Equal(t, 8, countRows(t, r.TestContext, `SELECT count(*) FROM products p WHERE p.is_visible AND EXISTS (SELECT 1 FROM hubrise_catalog_links l WHERE l.product_id = p.id AND l.entity_type = 'product')`))

	done := decode[struct{ Applied bool }](t, r.run(`mutation { syncHubriseCatalog(mode: FULL, confirmRetire: true) { applied } }`, nil)["syncHubriseCatalog"])
	assert.True(t, done.Applied)
	assert.Equal(t, 2, countRows(t, r.TestContext, `SELECT count(*) FROM products p WHERE p.is_visible AND EXISTS (SELECT 1 FROM hubrise_catalog_links l WHERE l.product_id = p.id AND l.entity_type = 'product')`))
}

func TestCatalogOperationsReportWhyTheyCannotRun(t *testing.T) {
	sync := `mutation { syncHubriseCatalog(mode: FULL) { applied } }`

	t.Run("not connected", func(t *testing.T) {
		r := newHubriseAdminRig(t, false)
		code, _ := r.fail(sync, nil)
		assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
		code, _ = r.fail(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`, nil)
		assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
	})
	t.Run("switched off", func(t *testing.T) {
		r := newHubriseAdminRig(t, true, hubtest.WithEnabled(false))
		code, _ := r.fail(sync, nil)
		assert.Equal(t, "HUBRISE_NOT_ENABLED", code)
	})
	t.Run("an empty HubRise catalog", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		require.NoError(t, r.srv.ReplaceCatalog(pkghubrise.CatalogData{}))
		code, _ := r.fail(sync, nil)
		assert.Equal(t, "HUBRISE_EMPTY_CATALOG", code)
	})
	t.Run("another import is running", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		ok, err := r.mod.Leases.Acquire(t.Context(), "catalog_import", "other-instance", time.Now(), time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		code, _ := r.fail(sync, nil)
		assert.Equal(t, "HUBRISE_BUSY", code)
	})
	t.Run("HubRise answers an error", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		r.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/location/catalog", Status: 500})
		r.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs", Status: 500})
		code, ext := r.fail(sync, nil)
		assert.Equal(t, "HUBRISE_UPSTREAM", code)
		assert.Equal(t, "server", ext["kind"])
		assert.NotContains(t, fmt.Sprint(ext), hubrisetest.Token)
	})
	t.Run("an unknown mode", func(t *testing.T) {
		r := newHubriseAdminRig(t, true)
		resp := r.ask(r.adminTok, `mutation { syncHubriseCatalog(mode: WHATEVER) { applied } }`, nil)
		require.NotEmpty(t, resp.Errors, "the schema refuses it")
	})
}

func TestLinkingAProductByHand(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	link := func(product uuid.UUID, sku string) string {
		return fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s", skuRef: "%s") }`, product, sku)
	}

	assert.True(t, decode[bool](t, r.run(link(r.Fixtures.SalmonSushi.ID, "MAKI-SAUMON-6"), nil)["linkHubriseProduct"]))
	assert.Equal(t, 1, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))

	code, _ := r.fail(link(r.Fixtures.TunaSushi.ID, "MAKI-SAUMON-6"), nil)
	assert.Equal(t, "HUBRISE_SKU_LINKED", code, "a SKU belongs to one product")

	code, ext := r.fail(link(uuid.New(), "X"), nil)
	assert.Equal(t, "PRODUCT_NOT_FOUND", code)
	assert.NotEmpty(t, ext["productId"])

	assert.True(t, decode[bool](t, r.run(fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s") }`, r.Fixtures.SalmonSushi.ID), nil)["linkHubriseProduct"]))
	assert.Zero(t, countRows(t, r.TestContext, `SELECT count(*) FROM hubrise_catalog_links`), "no SKU removes the link")
}

func TestForgettingTheLinks(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	r.run(fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s", skuRef: "A") }`, r.Fixtures.SalmonSushi.ID), nil)
	r.run(fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s", skuRef: "B") }`, r.Fixtures.TunaSushi.ID), nil)

	assert.Equal(t, 2, decode[int](t, r.run(`mutation { forgetHubriseLinks }`, nil)["forgetHubriseLinks"]))
	assert.Equal(t, 4, countRows(t, r.TestContext, `SELECT count(*) FROM products`), "the products stay")
}

// ---- inventory ----

func TestSyncingTheInventoryNow(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	r.run(fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s", skuRef: "MAKI-SAUMON-6") }`, r.Fixtures.SalmonSushi.ID), nil)
	require.NoError(t, r.srv.SetInventory([]pkghubrise.InventoryEntry{pkghubrise.SoldOutSku("MAKI-SAUMON-6", nil)}))
	r.exec(`UPDATE hubrise_connection SET inventory_baseline_at = now()`)

	n := decode[int](t, r.run(`mutation { syncHubriseInventory }`, nil)["syncHubriseInventory"])
	assert.Equal(t, 1, n)
	var available bool
	require.NoError(t, r.DB.DB.GetContext(t.Context(), &available, `SELECT is_available FROM products WHERE id = $1`, r.Fixtures.SalmonSushi.ID))
	assert.False(t, available)

	t.Run("while the inventory sync is off", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET sync_inventory = FALSE`)
		code, _ := r.fail(`mutation { syncHubriseInventory }`, nil)
		assert.Equal(t, "HUBRISE_NOT_ENABLED", code)
	})
	t.Run("while another sync runs", func(t *testing.T) {
		r.exec(`UPDATE hubrise_connection SET sync_inventory = TRUE`)
		ok, err := r.mod.Leases.Acquire(t.Context(), hubApplication.LeaseInventorySync, "other", time.Now(), time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		code, _ := r.fail(`mutation { syncHubriseInventory }`, nil)
		assert.Equal(t, "HUBRISE_BUSY", code)
	})
	t.Run("without a connection", func(t *testing.T) {
		r.exec(`DELETE FROM hubrise_connection`)
		code, _ := r.fail(`mutation { syncHubriseInventory }`, nil)
		assert.Equal(t, "HUBRISE_NOT_CONNECTED", code)
	})
}

// ---- outbox ----

func TestTheOutboxListsRowsWithoutTheirContentAndRetriesTheParkedOnes(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state, last_error, parked_at) VALUES
		('inventory', 'a', '{"customer":"secret-name"}', 'parked', 'refused', now()),
		('inventory', 'b', '{"customer":"secret-name"}', 'parked', 'refused', now()),
		('inventory', 'c', '{}', 'pending', NULL, NULL)`)

	resp := r.ask(r.adminTok, `{ hubriseOutbox { id kind orderId state attempts lastError nextAttemptAt createdAt sentAt parkedAt } }`, nil)
	require.Empty(t, resp.Errors)
	assert.NotContains(t, string(resp.Data), "secret-name")
	var all struct {
		Items []struct{ ID, Kind, State string } `json:"hubriseOutbox"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &all))
	require.Len(t, all.Items, 3)
	assert.Equal(t, "INVENTORY", all.Items[0].Kind)

	parked := decode[[]struct{ ID, State string }](t, r.run(`{ hubriseOutbox(state: PARKED, first: 1) { id state } }`, nil)["hubriseOutbox"])
	require.Len(t, parked, 1)
	assert.Equal(t, "PARKED", parked[0].State)

	one := decode[int](t, r.run(fmt.Sprintf(`mutation { retryHubriseOutbox(id: "%s") }`, parked[0].ID), nil)["retryHubriseOutbox"])
	assert.Equal(t, 1, one)
	assert.Equal(t, 1, decode[int](t, r.run(`mutation { retryHubriseOutbox }`, nil)["retryHubriseOutbox"]), "the other parked row")
	assert.Zero(t, decode[int](t, r.run(`mutation { retryHubriseOutbox }`, nil)["retryHubriseOutbox"]))

	for _, bad := range []string{"abc", "0", "-3", ""} {
		code, ext := r.fail(`mutation($id: String) { retryHubriseOutbox(id: $id) }`, map[string]any{"id": bad})
		assert.Equal(t, "USER_ERROR", code, bad)
		assert.Equal(t, "id", ext["field"])
	}
}

// ---- Product and Order fields ----

func TestProductsSayWhetherHubRiseOwnsThem(t *testing.T) {
	r := newHubriseAdminRig(t, true, hubtest.WithFeatures(true, true, false, true, false))
	r.run(fmt.Sprintf(`mutation { linkHubriseProduct(productId: "%s", skuRef: "A") }`, r.Fixtures.SalmonSushi.ID), nil)

	type row struct {
		ID      string   `json:"id"`
		Managed bool     `json:"hubriseManaged"`
		Locked  []string `json:"hubriseLockedFields"`
	}
	list := func() map[string]row {
		rows := decode[[]row](t, r.run(`{ products { id hubriseManaged hubriseLockedFields } }`, nil)["products"])
		out := map[string]row{}
		for _, x := range rows {
			out[x.ID] = x
		}
		return out
	}

	got := list()
	salmon := got[r.Fixtures.SalmonSushi.ID.String()]
	assert.True(t, salmon.Managed)
	assert.Equal(t, []string{"price", "category", "vatCategory", "code", "choiceGroups"}, salmon.Locked)
	tuna := got[r.Fixtures.TunaSushi.ID.String()]
	assert.False(t, tuna.Managed)
	assert.Empty(t, tuna.Locked)

	t.Run("a product without a code does not lock one", func(t *testing.T) {
		r.exec(`UPDATE products SET code = NULL WHERE id = $1`, r.Fixtures.SalmonSushi.ID)
		assert.Equal(t, []string{"price", "category", "vatCategory", "choiceGroups"}, list()[r.Fixtures.SalmonSushi.ID.String()].Locked)
	})

	t.Run("nothing is locked while catalog sync is off, paused or disconnected", func(t *testing.T) {
		for _, off := range []string{
			`UPDATE hubrise_connection SET sync_catalog = FALSE`,
			`UPDATE hubrise_connection SET sync_catalog = TRUE, enabled = FALSE`,
		} {
			r.exec(off)
			assert.False(t, list()[r.Fixtures.SalmonSushi.ID.String()].Managed, off)
		}
		r.exec(`DELETE FROM hubrise_connection`)
		assert.False(t, list()[r.Fixtures.SalmonSushi.ID.String()].Managed)
	})

	t.Run("a retired link is not a lock", func(t *testing.T) {
		hubtest.SeedConnection(t, r.DB.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithFeatures(true, true, false, true, false))
		r.exec(`UPDATE hubrise_catalog_links SET retired_at = now()`)
		assert.False(t, list()[r.Fixtures.SalmonSushi.ID.String()].Managed)
	})

	t.Run("without the request loader the answer is the same", func(t *testing.T) {
		r.exec(`UPDATE hubrise_catalog_links SET retired_at = NULL`)
		ctx := utils.SetUserID(context.Background(), r.Fixtures.AdminUser.ID.String())
		ctx = utils.SetIsAdmin(ctx, true)
		managed, err := r.Resolver.Product().HubriseManaged(ctx, &model.Product{ID: r.Fixtures.SalmonSushi.ID})
		require.NoError(t, err)
		assert.True(t, managed)
	})
}

func TestProductChoicesSayWhetherTheyAreAvailable(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	ctx := r.ctxFor(r.Fixtures.AdminUser.ID.String(), true, "en")
	group, err := r.Resolver.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{
		ProductID: r.Fixtures.SalmonSushi.ID, MinSelections: 0, MaxSelections: 2,
		Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Sauce"}}})
	require.NoError(t, err)
	choice, err := r.Resolver.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{
		ChoiceGroupID: &group.ID, PriceModifier: "0.5", Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Soja"}}})
	require.NoError(t, err)
	assert.True(t, choice.IsAvailable, "a new choice is on sale")

	query := fmt.Sprintf(`{ product(id: "%s") { choices { id isAvailable } } }`, r.Fixtures.SalmonSushi.ID)
	read := func() bool {
		var data struct {
			Product struct{ Choices []struct{ IsAvailable bool } }
		}
		resp := r.ask("", query, nil)
		require.Empty(t, resp.Errors)
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		require.Len(t, data.Product.Choices, 1)
		return data.Product.Choices[0].IsAvailable
	}
	assert.True(t, read())
	r.exec(`UPDATE product_choices SET is_available = FALSE WHERE id = $1`, choice.ID)
	assert.False(t, read(), "sold out in HubRise")
}

func TestOrdersShowTheirHubRiseSide(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	orderID := insertOrder(t, r.TestContext, r.Fixtures.RegularUser.ID)
	query := fmt.Sprintf(`{ order(id: "%s") { hubrise { hubriseOrderId pushState pushedAt remoteStatus remoteTotal needsAttention } } }`, orderID)

	none := r.run(query, nil)
	assert.JSONEq(t, `{"hubrise":null}`, string(none["order"]), "an order HubRise never saw")

	r.exec(`INSERT INTO hubrise_orders (order_id, hubrise_order_id, push_state, last_remote_status, remote_total, needs_attention, pushed_at)
		VALUES ($1, 'hr-42', 'sent', 'accepted', 12.5, 'TOTAL_MISMATCH', now())`, orderID)
	got := decode[struct {
		Hubrise struct {
			HubriseOrderID, PushState, RemoteStatus, RemoteTotal, NeedsAttention string
			PushedAt                                                             *string
		}
	}](t, r.run(query, nil)["order"])
	assert.Equal(t, "hr-42", got.Hubrise.HubriseOrderID)
	assert.Equal(t, "sent", got.Hubrise.PushState)
	assert.Equal(t, "accepted", got.Hubrise.RemoteStatus)
	assert.Equal(t, "12.50", got.Hubrise.RemoteTotal)
	assert.Equal(t, "TOTAL_MISMATCH", got.Hubrise.NeedsAttention)
	assert.NotNil(t, got.Hubrise.PushedAt)
}

// ---- failures of the store and of the module ----

func TestHubriseOperationsReportAStoreThatIsDown(t *testing.T) {
	r := newHubriseAdminRig(t, true)
	ctx := r.ctxFor(r.Fixtures.AdminUser.ID.String(), true, "en")

	// The admin service on a closed pool.
	mod, err := hubrise.NewModule(r.mod.Config, r.DB.ClosedConnection(t), nil)
	require.NoError(t, err)
	push := mod.NewPush(hubrise.PushOptions{})
	admin, err := mod.NewAdmin(push, mod.NewCatalog(push, hubrise.CatalogOptions{}), hubrise.AdminOptions{APIBaseURL: hubriseAPIBase})
	require.NoError(t, err)
	down := &resolver.Resolver{HubriseAdmin: admin}

	for name, call := range map[string]func() error{
		"status": func() error { _, err := down.Query().HubriseStatus(ctx); return err },
		"outbox": func() error { _, err := down.Query().HubriseOutbox(ctx, nil, nil); return err },
		"retry":  func() error { _, err := down.Mutation().RetryHubriseOutbox(ctx, nil); return err },
		"order":  func() error { _, err := down.Order().Hubrise(ctx, &model.Order{ID: uuid.New()}); return err },
		"product": func() error {
			_, err := down.Product().HubriseManaged(ctx, &model.Product{ID: uuid.New()})
			return err
		},
		"start": func() error { _, err := down.Mutation().StartHubriseConnection(ctx); return err },
		"disconnect": func() error {
			_, err := down.Mutation().DisconnectHubrise(ctx, nil)
			return err
		},
		"forget": func() error { _, err := down.Mutation().ForgetHubriseLinks(ctx); return err },
		"settings": func() error {
			_, err := down.Mutation().UpdateHubriseSettings(ctx, model.HubriseSettingsInput{})
			return err
		},
		"sync inventory": func() error { _, err := down.Mutation().SyncHubriseInventory(ctx); return err },
	} {
		assert.Error(t, call(), name)
	}

	t.Run("the product loader and the link of a product report it too", func(t *testing.T) {
		loaded := resolver.AttachHubriseLoaders(ctx, down)
		_, err := down.Product().HubriseManaged(loaded, &model.Product{ID: uuid.New()})
		assert.Error(t, err)

		broken := r.brokenResolver(t)
		broken.HubriseAdmin = r.admin
		_, err = broken.Mutation().LinkHubriseProduct(ctx, r.Fixtures.SalmonSushi.ID, nil)
		assert.Error(t, err)
		appErr, isApp := apperr.From(err)
		assert.False(t, isApp && appErr.Code == apperr.CodeProductNotFound, "a store failure is not a missing product")
	})

	t.Run("an unparsable admin id is refused", func(t *testing.T) {
		_, err := r.Resolver.Mutation().StartHubriseConnection(utils.SetUserID(context.Background(), "not-a-uuid"))
		appErr, ok := apperr.From(err)
		require.True(t, ok)
		assert.Equal(t, apperr.CodeForbidden, appErr.Code)
	})
}

func TestHubriseErrorMapping(t *testing.T) {
	code := func(err error) apperr.Code {
		t.Helper()
		appErr, ok := apperr.From(resolver.HubriseError(err))
		require.True(t, ok, "%v", err)
		return appErr.Code
	}
	assert.NoError(t, resolver.HubriseError(nil))
	other := errors.New("boom")
	assert.Same(t, other, resolver.HubriseError(other), "anything else is left alone")

	assert.Equal(t, apperr.CodeHubriseManaged, code(&hubDomain.ManagedError{Fields: []string{"price"}}))
	assert.Equal(t, apperr.CodeHubriseRetireConfirmRequired, code(&hubDomain.RetireConfirmError{Retiring: 6, Linked: 8}))
	assert.Equal(t, apperr.CodeUserError, code(&hubDomain.InvalidSettingError{Field: "x", Message: "bad"}))
	assert.Equal(t, apperr.CodeHubriseRelinkRequired, code(fmt.Errorf("w: %w", hubDomain.ErrRelinkRequired)))
	assert.Equal(t, apperr.CodeHubrisePreviewRequired, code(hubDomain.ErrPreviewRequired))
	assert.Equal(t, apperr.CodeHubriseNotConnected, code(hubDomain.ErrNotConnected))
	assert.Equal(t, apperr.CodeHubriseNotEnabled, code(hubDomain.ErrNotEnabled))
	assert.Equal(t, apperr.CodeHubriseEmptyCatalog, code(hubDomain.ErrEmptyCatalog))
	assert.Equal(t, apperr.CodeHubriseBusy, code(hubDomain.ErrImportInProgress))
	assert.Equal(t, apperr.CodeHubriseBusy, code(hubApplication.ErrInventoryBusy))
	assert.Equal(t, apperr.CodeHubriseSKULinked, code(fmt.Errorf("%w: %q", hubDomain.ErrSKULinked, "S")))
	assert.Equal(t, apperr.CodeHubriseUpstream, code(&pkghubrise.Error{Kind: pkghubrise.KindServer, Status: 502}))
	assert.Equal(t, apperr.CodeHubriseUpstream, code(&pkghubrise.Error{Kind: pkghubrise.KindNetwork}), "with no status")

	// Neither the kind nor the status text carries anything but the class of the failure.
	e, ok := apperr.From(resolver.HubriseError(&pkghubrise.Error{Kind: pkghubrise.KindUnauthorized, Status: 401, Message: "token abc123 revoked"}))
	require.True(t, ok)
	assert.NotContains(t, e.Error(), "abc123")
}
