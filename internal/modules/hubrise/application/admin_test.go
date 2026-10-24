package application_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/utils"
)

const (
	adminAPIBase  = "https://api.example.test/api/v1"
	adminCallback = adminAPIBase + "/hubrise/callback"
)

// auditLog records what the Admin asks to audit, with the principal of the context.
type auditLog struct {
	mu      sync.Mutex
	entries []auditEntry
}

type auditEntry struct {
	Action  string
	Success bool
	Details map[string]any
	UserID  string
	Admin   bool
}

func (a *auditLog) record(ctx context.Context, action string, success bool, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, auditEntry{Action: action, Success: success, Details: details, UserID: utils.GetUserID(ctx), Admin: utils.GetIsAdmin(ctx)})
}

func (a *auditLog) all() []auditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]auditEntry(nil), a.entries...)
}

// adminRig is the connection management on a database, the mock and the real repositories.
type adminRig struct {
	*env
	admin     *application.Admin
	catalog   *hubrise.Catalog
	inventory *hubrise.Inventory
	audit     *auditLog
}

func newAdminRig(t *testing.T, options ...envOption) *adminRig {
	t.Helper()
	// The seeded connection is at the location and catalog the mock serves, so a reconnect is the same one.
	defaults := []envOption{withSharedClock(), withSeed(hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID))}
	e := newEnv(t, append(defaults, options...)...)
	limiter := pkghubrise.NewLimiter(pkghubrise.Limits{})
	pooled := application.NewPooledClients(e.mod.Codec, e.srv.APIBaseURL(), e.srv.HTTPClient(), limiter)
	r := &adminRig{env: e, audit: &auditLog{}}
	r.catalog = e.mod.NewCatalog(e.push, hubrise.CatalogOptions{Clients: pooled, Holder: "admin-test"})
	r.inventory = e.mod.NewInventory(e.push, hubrise.InventoryOptions{Clients: pooled, Drift: r.catalog.Drift})
	admin, err := e.mod.NewAdmin(e.push, r.catalog, hubrise.AdminOptions{
		Clients: pooled, OAuthHTTP: e.srv.HTTPClient(), APIBaseURL: adminAPIBase, Inventory: r.inventory.Service, Audit: r.audit.record,
		TokenClient: func(token string) (*pkghubrise.Client, error) {
			return pkghubrise.New(e.srv.APIBaseURL(), token, pkghubrise.WithHTTPClient(e.srv.HTTPClient()), pkghubrise.WithLimiter(limiter))
		},
	})
	require.NoError(t, err)
	r.admin = admin
	return r
}

// authorize plays the admin approving the connection in HubRise: it starts the connection and
// returns the state and a fresh authorization code, the way the browser would bring them back.
func (r *adminRig) authorize(adminID uuid.UUID) (state, code string) {
	r.t.Helper()
	authURL, err := r.admin.StartConnection(r.t.Context(), adminID)
	require.NoError(r.t, err)
	u, err := url.Parse(authURL)
	require.NoError(r.t, err)
	return u.Query().Get("state"), r.srv.IssueAuthCode()
}

func (r *adminRig) conn() *domain.Connection {
	r.t.Helper()
	c, err := r.mod.Connections.Get(r.t.Context())
	require.NoError(r.t, err)
	return c
}

func (r *adminRig) tokenOf(c *domain.Connection) string {
	r.t.Helper()
	tok, err := c.AccessToken(r.mod.Codec)
	require.NoError(r.t, err)
	return tok.Reveal()
}

// link records a catalog link of a local product.
func (r *adminRig) link(productID uuid.UUID, sku string) {
	r.t.Helper()
	r.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin) VALUES ('product', $1, $2, $2, 'manual')`, productID, sku)
}

func (r *adminRig) outboxRows(state string) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.db.GetContext(r.t.Context(), &n, `SELECT count(*) FROM hubrise_outbox WHERE state = $1`, state))
	return n
}

// ---- connect ----

func TestStartConnectionIssuesAOneTimeStateBoundToTheAdmin(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	admin := uuid.New()

	authURL, err := r.admin.StartConnection(t.Context(), admin)
	require.NoError(t, err)

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.Equal(t, r.srv.OAuthBaseURL()+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	assert.Equal(t, hubrisetest.ClientID, q.Get("client_id"))
	assert.Equal(t, adminAPIBase+"/hubrise/oauth/callback", q.Get("redirect_uri"))
	for _, scope := range []string{"orders.write", "customer_list.write", "catalog.read", "inventory.read", "inventory.write"} {
		assert.Contains(t, q.Get("scope"), scope, "the scope asks for "+scope)
	}
	state := q.Get("state")
	assert.GreaterOrEqual(t, len(state), 40, "an unguessable nonce")

	var boundTo uuid.UUID
	var expires time.Time
	require.NoError(t, r.db.QueryRowContext(t.Context(), `SELECT admin_user_id, expires_at FROM hubrise_oauth_states WHERE state = $1`, state).Scan(&boundTo, &expires))
	assert.Equal(t, admin, boundTo)
	assert.WithinDuration(t, r.clock.Now().Add(10*time.Minute), expires, time.Second, "valid for the ten minutes of the authorization code")

	other, err := r.admin.StartConnection(t.Context(), admin)
	require.NoError(t, err)
	assert.NotEqual(t, authURL, other, "every start has its own nonce")
}

func TestStartConnectionPurgesExpiredStates(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	first, _ := r.authorize(uuid.New())
	r.clock.Advance(11 * time.Minute)
	_, _ = r.authorize(uuid.New())

	var n int
	require.NoError(t, r.db.GetContext(t.Context(), &n, `SELECT count(*) FROM hubrise_oauth_states WHERE state = $1`, first))
	assert.Zero(t, n)
}

func TestCompleteConnectionStoresTheSealedTokenAndRegistersTheCallback(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	adminID := uuid.New()
	state, code := r.authorize(adminID)

	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.Equal(t, adminID, res.AdminID)
	assert.False(t, res.RelinkRequired)

	c := r.conn()
	require.True(t, c.Connected())
	assert.Equal(t, hubrisetest.Token, r.tokenOf(c), "the stored token opens to the one HubRise issued")
	assert.NotContains(t, string(c.AccessTokenEnc), hubrisetest.Token, "and is not stored in the clear")
	assert.False(t, c.Enabled, "connecting does not switch anything on")
	assert.False(t, c.PushOrders || c.SyncStatus || c.SyncInventory || c.SyncCatalog || c.SyncCustomers,
		"and every feature starts off, to be turned on one by one")
	assert.Equal(t, hubrisetest.AccountID, *c.AccountID)
	assert.Equal(t, hubrisetest.AccountName, *c.AccountName)
	assert.Equal(t, hubrisetest.LocationID, *c.LocationID)
	assert.Equal(t, hubrisetest.LocationName, *c.LocationName)
	assert.Equal(t, hubrisetest.CatalogID, *c.CatalogID)
	assert.Equal(t, hubrisetest.CatalogName, *c.CatalogName)
	assert.Equal(t, hubrisetest.CustomerListID, *c.CustomerListID)
	assert.Equal(t, hubrisetest.CustomerList, *c.CustomerListName)
	assert.Equal(t, pkghubrise.DefaultScope, *c.Scope)
	require.NotNil(t, c.PushOrdersSince)
	assert.WithinDuration(t, r.clock.Now(), *c.PushOrdersSince, time.Second)
	require.NotNil(t, c.CallbackRegisteredAt)

	cb := r.srv.Callback(hubrisetest.Token)
	require.NotNil(t, cb, "the callback is registered at HubRise")
	assert.Equal(t, adminCallback, cb.URL)
	assert.Equal(t, map[string][]string{
		"order": {"update"}, "catalog": {"create", "update", "delete"}, "inventory": {"update", "patch"},
	}, cb.Events, "exactly the events the integration uses")

	var used int
	require.NoError(t, r.db.GetContext(t.Context(), &used, `SELECT count(*) FROM hubrise_oauth_states WHERE state = $1`, state))
	assert.Zero(t, used, "the state is spent")

	entries := r.audit.all()
	require.Len(t, entries, 1)
	assert.Equal(t, application.AuditConnect, entries[0].Action)
	assert.True(t, entries[0].Success)
	assert.True(t, entries[0].Admin, "attributed to the admin who started it")
	assert.Equal(t, adminID.String(), entries[0].UserID)
	assert.NotContains(t, strings.ToLower(fmt.Sprint(entries[0].Details)), "token")
}

func TestCompleteConnectionRefusesAnUnusableState(t *testing.T) {
	r := newAdminRig(t, withoutConnection())

	t.Run("unknown", func(t *testing.T) {
		code := r.srv.IssueAuthCode()
		_, err := r.admin.CompleteConnection(t.Context(), "never-issued", code)
		require.ErrorIs(t, err, domain.ErrInvalidState)
		_, getErr := r.mod.Connections.Get(t.Context())
		require.ErrorIs(t, getErr, domain.ErrNotFound, "nothing stored")
		// The code was never exchanged: it still works.
		_, exErr := r.srv.NewOAuth(t).ExchangeCode(t.Context(), code)
		assert.NoError(t, exErr, "a refused state does not spend the code")
	})

	t.Run("expired", func(t *testing.T) {
		state, code := r.authorize(uuid.New())
		r.clock.Advance(10*time.Minute + time.Second)
		_, err := r.admin.CompleteConnection(t.Context(), state, code)
		require.ErrorIs(t, err, domain.ErrInvalidState)
		_, getErr := r.mod.Connections.Get(t.Context())
		assert.ErrorIs(t, getErr, domain.ErrNotFound)
	})

	t.Run("replayed", func(t *testing.T) {
		state, code := r.authorize(uuid.New())
		_, err := r.admin.CompleteConnection(t.Context(), state, code)
		require.NoError(t, err)
		_, err = r.admin.CompleteConnection(t.Context(), state, r.srv.IssueAuthCode())
		require.ErrorIs(t, err, domain.ErrInvalidState, "a state works once")
	})
}

func TestCompleteConnectionReportsARefusedCodeWithoutLeakingIt(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	state, _ := r.authorize(uuid.New())

	_, err := r.admin.CompleteConnection(t.Context(), state, "forged-secret-code")
	require.ErrorIs(t, err, domain.ErrAuthorizationFailed)
	assert.NotContains(t, err.Error(), "forged-secret-code")
	_, getErr := r.mod.Connections.Get(t.Context())
	require.ErrorIs(t, getErr, domain.ErrNotFound)

	entries := r.audit.all()
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Success, "a failed connection is audited too")
	assert.NotContains(t, fmt.Sprint(entries[0].Details), "forged-secret-code")

	// The state was spent by the attempt.
	_, err = r.admin.CompleteConnection(t.Context(), state, r.srv.IssueAuthCode())
	assert.ErrorIs(t, err, domain.ErrInvalidState)
}

func TestACallbackRegistrationFailureLeavesThePreviousConnectionAlone(t *testing.T) {
	r := newAdminRig(t) // connected and enabled already
	before := r.conn()
	r.srv.AddFault(hubrisetest.Fault{Method: "POST", Path: "/v1/callback", Status: 500})
	state, code := r.authorize(uuid.New())

	_, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.ErrorIs(t, err, domain.ErrAuthorizationFailed)

	after := r.conn()
	assert.Equal(t, before.AccessTokenEnc, after.AccessTokenEnc, "the stored connection is untouched")
	assert.True(t, after.Enabled)
}

func TestACallbackRegistrationRefusedByHubRiseIsAnAuthorizationFailure(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	r.srv.AddFault(hubrisetest.Fault{Method: "POST", Path: "/v1/callback", Status: 422})
	state, code := r.authorize(uuid.New())
	_, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.ErrorIs(t, err, domain.ErrAuthorizationFailed)
	assert.Contains(t, err.Error(), "register the callback")
}

func TestReconnectingKeepsTheSettingsAndThePushStamp(t *testing.T) {
	r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, false, false, false, true)))
	stamp := r.conn().PushOrdersSince
	require.NotNil(t, stamp)
	state, code := r.authorize(uuid.New())

	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.False(t, res.RelinkRequired, "same location, same catalog")

	c := r.conn()
	assert.True(t, c.Enabled, "re-authorizing is not a disable")
	assert.False(t, c.SyncStatus, "the toggles stay as the owner set them")
	assert.WithinDuration(t, *stamp, *c.PushOrdersSince, time.Millisecond, "a live connection keeps its stamp: no history is replayed")
}

func TestReconnectingAfterADisconnectStampsThePushAgain(t *testing.T) {
	r := newAdminRig(t, withSeed(hubtest.WithPushOrdersSince(time.Now().Add(-72*time.Hour))))
	require.NoError(t, r.admin.Disconnect(t.Context(), false))
	state, code := r.authorize(uuid.New())
	_, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)

	assert.WithinDuration(t, r.clock.Now(), *r.conn().PushOrdersSince, time.Second, "only orders from now on")
	assert.False(t, r.conn().Enabled, "a disconnect switched it off, a reconnect does not switch it back on")
}

// ---- relink ----

func TestReconnectingToAnotherLocationBlocksUntilTheLinksAreForgotten(t *testing.T) {
	// The old connection belongs to the other HubRise client, so revoking it is visible.
	r := newAdminRig(t, withSeed(hubtest.WithToken(hubrisetest.EPOSToken), hubtest.WithLocation("old-loc", "old-cat")))
	r.link(r.fx.SalmonSushi.ID, "OLD-SKU")
	_, err := r.srv.NewClient(t, pkghubrise.WithHTTPClient(r.srv.HTTPClient())).SetCallback(t.Context(), pkghubrise.Callback{})
	require.NoError(t, err)
	state, code := r.authorize(uuid.New())

	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.True(t, res.RelinkRequired, "the links name refs of another catalog")

	c := r.conn()
	assert.Equal(t, hubrisetest.Token, r.tokenOf(c), "the new token is stored")
	assert.False(t, c.Enabled)
	assert.False(t, c.SyncCatalog)
	assert.False(t, c.SyncInventory)
	require.NotNil(t, c.RelinkRequiredAt)
	assert.True(t, r.srv.TokenRevoked(hubrisetest.EPOSToken), "the old location's token is revoked")

	on := true
	_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &on})
	require.ErrorIs(t, err, domain.ErrRelinkRequired)

	n, err := r.admin.ForgetLinks(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	updated, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &on})
	require.NoError(t, err)
	assert.True(t, updated.Enabled, "once the old links are gone it can be enabled")
	assert.Nil(t, updated.RelinkRequiredAt)
}

func TestReconnectingToAnotherLocationWithoutLinksNeedsNoRelink(t *testing.T) {
	r := newAdminRig(t, withSeed(hubtest.WithLocation("old-loc", "old-cat")))
	state, code := r.authorize(uuid.New())
	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.False(t, res.RelinkRequired)
	assert.True(t, r.conn().Enabled)
}

func TestDisconnectThenReconnectToAnotherCatalogNeedsARelink(t *testing.T) {
	r := newAdminRig(t)
	r.link(r.fx.SalmonSushi.ID, "OLD-SKU")
	require.NoError(t, r.admin.Disconnect(t.Context(), false))
	r.srv.SetOAuthTarget(&hubrisetest.OAuthTarget{LocationID: "loc-2", LocationName: "Other", CatalogID: "cat-2", CatalogName: "Other menu"})

	state, code := r.authorize(uuid.New())
	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.True(t, res.RelinkRequired, "the kept links belong to the old catalog")
	assert.Equal(t, "loc-2", *r.conn().LocationID)
}

// ---- disconnect ----

func TestDisconnectRevokesTheTokenAndKeepsTheRowAndTheLinks(t *testing.T) {
	r := newAdminRig(t)
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	_, err := r.srv.NewClient(t).SetCallback(t.Context(), pkghubrise.Callback{URL: adminCallback, Events: application.CallbackEvents()})
	require.NoError(t, err)

	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	c := r.conn()
	assert.False(t, c.Connected())
	assert.Nil(t, c.AccessTokenEnc)
	assert.False(t, c.Enabled)
	assert.Nil(t, c.CallbackRegisteredAt)
	assert.Equal(t, hubrisetest.LocationID, *c.LocationID, "the ids stay, so a reconnect can tell it is the same location")
	assert.True(t, r.srv.TokenRevoked(hubrisetest.Token))
	assert.Nil(t, r.srv.Callback(hubrisetest.Token), "and the callback is gone")
	assert.EqualValues(t, 1, r.countLinks(), "the links stay")

	entries := r.audit.all()
	require.Len(t, entries, 1)
	assert.Equal(t, application.AuditDisconnect, entries[0].Action)
	assert.Equal(t, true, entries[0].Details["revoked"])
	assert.Equal(t, false, entries[0].Details["forget_links"])
}

func (r *adminRig) countLinks() int {
	r.t.Helper()
	n, err := r.mod.CatalogLinks.Count(r.t.Context(), true)
	require.NoError(r.t, err)
	return n
}

func TestDisconnectCanForgetTheLinks(t *testing.T) {
	r := newAdminRig(t)
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	r.link(r.fx.TunaSushi.ID, "SKU-2")

	require.NoError(t, r.admin.Disconnect(t.Context(), true))

	assert.Zero(t, r.countLinks())
	var n int
	require.NoError(t, r.db.GetContext(t.Context(), &n, `SELECT count(*) FROM products`))
	assert.Positive(t, n, "the products stay, as ordinary products")
	assert.EqualValues(t, 2, r.audit.all()[0].Details["links_forgotten"])
}

func TestDisconnectWorksWhenHubRiseAlreadyRefusesTheToken(t *testing.T) {
	r := newAdminRig(t)
	r.srv.RevokeToken(hubrisetest.Token)

	require.NoError(t, r.admin.Disconnect(t.Context(), false), "a token HubRise already refuses is as good as gone")

	assert.False(t, r.conn().Connected())
	assert.Equal(t, application.AuditDisconnect, r.audit.all()[0].Action)
}

func TestDisconnectOfADisconnectedRowOnlyForgetsTheLinks(t *testing.T) {
	r := newAdminRig(t)
	require.NoError(t, r.admin.Disconnect(t.Context(), false))
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	before := len(r.srv.Requests())

	require.NoError(t, r.admin.Disconnect(t.Context(), true))

	assert.Zero(t, r.countLinks())
	assert.Len(t, r.srv.Requests(), before, "no call to HubRise: there is no token")
}

func TestDisconnectWithoutAConnectionIsNotConnected(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	assert.ErrorIs(t, r.admin.Disconnect(t.Context(), false), domain.ErrNotConnected)
}

func TestDisconnectThenReconnectResumesLosslessly(t *testing.T) {
	r := newAdminRig(t)
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	state, code := r.authorize(uuid.New())
	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.False(t, res.RelinkRequired, "the same location")
	assert.EqualValues(t, 1, r.countLinks(), "the links were kept")
	assert.True(t, r.conn().Connected())
	assert.NotNil(t, r.srv.Callback(hubrisetest.Token), "and the callback is registered again")

	on := true
	updated, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &on})
	require.NoError(t, err)
	assert.True(t, updated.Enabled)
}

// ---- status ----

func TestStatusWithoutAConnection(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	st, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.Nil(t, st.Connection)
	assert.Zero(t, st.UnlinkedProducts)
	assert.Zero(t, st.Attention)
	assert.Equal(t, domain.OutboxCounts{}, st.Backlog)
}

func TestStatusCountsTheBacklogTheUnlinkedProductsAndTheOrdersNeedingAttention(t *testing.T) {
	r := newAdminRig(t)
	var visible int
	require.NoError(t, r.db.GetContext(t.Context(), &visible, `SELECT count(*) FROM products WHERE is_visible`))
	require.Greater(t, visible, 1)
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state) VALUES ('inventory', 'inv:sku:A', '{}', 'pending'), ('inventory', 'inv:sku:B', '{}', 'parked')`)
	order := r.createOrder()
	r.exec(`INSERT INTO hubrise_orders (order_id, push_state, needs_attention) VALUES ($1, 'sent', 'UNLINKED_ITEM')`, order)

	st, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	require.NotNil(t, st.Connection)
	assert.True(t, st.Connection.Connected())
	assert.Equal(t, domain.OutboxCounts{Pending: 1, Parked: 1}, st.Backlog)
	assert.Equal(t, visible-1, st.UnlinkedProducts, "the linked product is not counted")
	assert.Equal(t, 1, st.Attention)
}

func TestStatusOfADisconnectedRowKeepsTheBacklogButNotTheUnlinkedCount(t *testing.T) {
	r := newAdminRig(t)
	r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state) VALUES ('inventory', 'inv:sku:A', '{}', 'parked')`)
	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	st, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.False(t, st.Connection.Connected())
	assert.Equal(t, 1, st.Backlog.Parked)
	assert.Zero(t, st.UnlinkedProducts)
}

// createOrder inserts a minimal order and returns its id.
func (r *adminRig) createOrder() uuid.UUID {
	r.t.Helper()
	var id uuid.UUID
	require.NoError(r.t, r.db.GetContext(r.t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
		VALUES ($1, 'CONFIRMED', 'PICKUP', false, 10, 'fr') RETURNING id`, r.fx.RegularUser.ID))
	return id
}

// ---- settings ----

func TestUpdateSettingsNeedsAConnection(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	on := true
	_, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &on})
	assert.ErrorIs(t, err, domain.ErrNotConnected)

	rig := newAdminRig(t)
	require.NoError(t, rig.admin.Disconnect(t.Context(), false))
	_, err = rig.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &on})
	assert.ErrorIs(t, err, domain.ErrNotConnected, "a disconnected row has no settings to change")
}

func TestUpdateSettingsValidatesAndNormalizes(t *testing.T) {
	r := newAdminRig(t)
	str := func(s string) *string { return &s }

	for name, patch := range map[string]domain.SettingsPatch{
		"unknown language": {CatalogLanguage: str("de")},
		"empty language":   {CatalogLanguage: str("")},
		"long variant":     {VariantRef: str(strings.Repeat("x", 101))},
		"control char":     {ChannelName: str("web\nshop")},
		"long delivery":    {DeliveryChargeRef: str(strings.Repeat("é", 101))},
		"long fee":         {PaymentFeeChargeRef: str(strings.Repeat("y", 200))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.admin.UpdateSettings(t.Context(), patch)
			var invalid *domain.InvalidSettingError
			require.ErrorAs(t, err, &invalid)
			assert.NotEmpty(t, invalid.Field)
			assert.NotEmpty(t, invalid.Message)
		})
	}

	updated, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{
		CatalogLanguage: str(" ZH "), VariantRef: str(" delivery "), ChannelName: str("  webshop"),
		DeliveryChargeRef: str("DELIV"), PaymentFeeChargeRef: str("FEE "),
	})
	require.NoError(t, err)
	assert.Equal(t, "zh", updated.CatalogLanguage)
	assert.Equal(t, "delivery", *updated.VariantRef)
	assert.Equal(t, "webshop", *updated.ChannelName)
	assert.Equal(t, "DELIV", *updated.DeliveryChargeRef)
	assert.Equal(t, "FEE", *updated.PaymentFeeChargeRef)

	cleared, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{VariantRef: str("   "), ChannelName: str("")})
	require.NoError(t, err)
	assert.Nil(t, cleared.VariantRef, "an empty value clears the option")
	assert.Nil(t, cleared.ChannelName)
	assert.Equal(t, "DELIV", *cleared.DeliveryChargeRef, "the others are untouched")
	assert.Equal(t, "zh", cleared.CatalogLanguage)
}

func TestUpdateSettingsTogglesAreIndependent(t *testing.T) {
	r := newAdminRig(t)
	yes, no := true, false

	c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{
		PushOrders: &no, SyncStatus: &no, SyncCustomers: &no, CatalogAutoImport: &yes, AutoRefundOnCancel: &yes, ImportImages: &yes,
	})
	require.NoError(t, err)
	assert.False(t, c.PushOrders)
	assert.False(t, c.SyncStatus)
	assert.False(t, c.SyncCustomers)
	assert.True(t, c.CatalogAutoImport)
	assert.True(t, c.AutoRefundOnCancel)
	assert.True(t, c.ImportImages)
	assert.True(t, c.SyncInventory, "untouched")
	assert.True(t, c.Enabled, "untouched")
}

func TestPushingBecomingEffectiveStampsPushOrdersSince(t *testing.T) {
	yes, no := true, false
	longAgo := time.Now().Add(-90 * 24 * time.Hour)

	t.Run("enabling", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithEnabled(false), hubtest.WithPushOrdersSince(longAgo)))
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &yes})
		require.NoError(t, err)
		assert.WithinDuration(t, r.clock.Now(), *c.PushOrdersSince, time.Second)
	})
	t.Run("enabling with every feature off (a new connection) stamps nothing, the push switch does", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithEnabled(false), hubtest.WithFeatures(false, false, false, false, false), hubtest.WithPushOrdersSince(longAgo)))
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &yes})
		require.NoError(t, err)
		assert.WithinDuration(t, longAgo, *c.PushOrdersSince, time.Second, "nothing is pushed yet")
		c, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: &yes})
		require.NoError(t, err)
		assert.WithinDuration(t, r.clock.Now(), *c.PushOrdersSince, time.Second, "only the orders from now on")
	})
	t.Run("push_orders on again", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithFeatures(false, true, true, false, true), hubtest.WithPushOrdersSince(longAgo)))
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: &yes})
		require.NoError(t, err)
		assert.WithinDuration(t, r.clock.Now(), *c.PushOrdersSince, time.Second)
	})
	t.Run("while disabled it is not effective, so nothing is stamped", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithEnabled(false), hubtest.WithFeatures(false, true, true, false, true), hubtest.WithPushOrdersSince(longAgo)))
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: &yes})
		require.NoError(t, err)
		assert.WithinDuration(t, longAgo, *c.PushOrdersSince, time.Second)
	})
	t.Run("an unrelated change keeps the stamp", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithPushOrdersSince(longAgo)))
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncStatus: &no})
		require.NoError(t, err)
		assert.WithinDuration(t, longAgo, *c.PushOrdersSince, time.Second)
	})
	t.Run("the caller cannot set it", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithPushOrdersSince(longAgo)))
		forged := time.Now().Add(24 * time.Hour)
		c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncStatus: &no, PushOrdersSince: &forged})
		require.NoError(t, err)
		assert.WithinDuration(t, longAgo, *c.PushOrdersSince, time.Second)
	})
}

func TestEnablingCatalogSyncNeedsAFreshPreviewOfTheCurrentCatalog(t *testing.T) {
	yes := true
	r := newAdminRig(t)

	_, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
	require.ErrorIs(t, err, domain.ErrPreviewRequired, "no preview yet")

	res, err := r.admin.PreviewCatalog(t.Context(), domain.SyncDryRun)
	require.NoError(t, err)
	assert.False(t, res.Applied)

	c, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
	require.NoError(t, err)
	assert.True(t, c.SyncCatalog)

	t.Run("a stale preview does not count", func(t *testing.T) {
		no := false
		_, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &no})
		require.NoError(t, err)
		r.clock.Advance(25 * time.Hour)
		_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
		assert.ErrorIs(t, err, domain.ErrPreviewRequired)
	})

	t.Run("nor does a preview of another catalog", func(t *testing.T) {
		_, err := r.admin.PreviewCatalog(t.Context(), domain.SyncFull)
		require.NoError(t, err)
		data := hubrisetest.SeedCatalog()
		data.Products[0].Skus[0].Price = "99.00 EUR"
		require.NoError(t, r.srv.ReplaceCatalog(data))
		_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
		assert.ErrorIs(t, err, domain.ErrPreviewRequired)
	})
}

func TestAPreviewInLinkOnlyModeDoesNotCountAsThePreviewCatalogSyncNeeds(t *testing.T) {
	yes := true
	r := newAdminRig(t)
	res, err := r.admin.PreviewCatalog(t.Context(), domain.SyncLinkOnly)
	require.NoError(t, err)
	assert.False(t, res.Applied)
	assert.Nil(t, r.conn().CatalogPreviewHash, "nothing recorded")
	assert.Zero(t, r.countLinks(), "and nothing written")

	_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
	assert.ErrorIs(t, err, domain.ErrPreviewRequired)
}

func TestAPreviewOfAnUnknownModeIsRefused(t *testing.T) {
	r := newAdminRig(t)
	_, err := r.admin.PreviewCatalog(t.Context(), domain.SyncMode("EVERYTHING"))
	var invalid *domain.InvalidSettingError
	require.ErrorAs(t, err, &invalid)
	_, err = r.admin.SyncCatalog(t.Context(), domain.SyncMode(""), false)
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, "mode", invalid.Field)
}

func TestSyncCatalogRunsTheImportInTheMode(t *testing.T) {
	r := newAdminRig(t)
	res, err := r.admin.SyncCatalog(t.Context(), domain.SyncFull, false)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Positive(t, r.countLinks(), "the catalog was imported and linked")

	require.NoError(t, r.admin.Disconnect(t.Context(), false))
	_, err = r.admin.SyncCatalog(t.Context(), domain.SyncFull, false)
	assert.ErrorIs(t, err, domain.ErrNotConnected)
}

func TestLinkProductByHandAndUnlink(t *testing.T) {
	r := newAdminRig(t)
	sku := "MAKI-SAUMON-6"
	require.NoError(t, r.admin.LinkProduct(t.Context(), r.fx.SalmonSushi.ID, &sku))
	assert.EqualValues(t, 1, r.countLinks())

	err := r.admin.LinkProduct(t.Context(), r.fx.TunaSushi.ID, &sku)
	require.ErrorIs(t, err, domain.ErrSKULinked, "a SKU belongs to one product")

	require.NoError(t, r.admin.LinkProduct(t.Context(), r.fx.SalmonSushi.ID, nil))
	assert.Zero(t, r.countLinks())
}

func TestInventoryBecomingEffectiveRestartsTheBaseline(t *testing.T) {
	yes := true
	r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, true, false, false, true)))
	r.link(r.fx.SalmonSushi.ID, "SKU-1")
	r.exec(`UPDATE products SET is_available = false WHERE id = $1`, r.fx.SalmonSushi.ID)
	r.exec(`UPDATE hubrise_connection SET inventory_baseline_at = now() - interval '2 days'`)

	_, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncInventory: &yes})
	require.NoError(t, err)

	assert.Equal(t, 1, r.outboxRows("pending"), "the shop's sold-out product is pushed again")
	c := r.conn()
	require.NotNil(t, c.InventoryBaselineAt)
	assert.WithinDuration(t, r.clock.Now(), *c.InventoryBaselineAt, time.Second, "and the baseline is stamped again")

	t.Run("a change that leaves the sync as it was does not", func(t *testing.T) {
		before := r.outboxRows("pending")
		no := false
		_, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncStatus: &no})
		require.NoError(t, err)
		assert.Equal(t, before, r.outboxRows("pending"))
	})
}

func TestInventoryBaselineResetStillWorksWithoutTheInventoryService(t *testing.T) {
	yes := true
	e := newEnv(t, withSeed(hubtest.WithFeatures(true, true, false, false, true)))
	pooled := application.NewPooledClients(e.mod.Codec, e.srv.APIBaseURL(), e.srv.HTTPClient(), nil)
	catalog := e.mod.NewCatalog(e.push, hubrise.CatalogOptions{Clients: pooled})
	admin, err := e.mod.NewAdmin(e.push, catalog, hubrise.AdminOptions{Clients: pooled, APIBaseURL: adminAPIBase})
	require.NoError(t, err)
	e.exec(`UPDATE hubrise_connection SET inventory_baseline_at = now()`)

	_, err = admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncInventory: &yes})
	require.NoError(t, err)

	c, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.Nil(t, c.InventoryBaselineAt, "the next sync pushes the baseline")
}

func TestSyncInventoryNowReadsTheInventory(t *testing.T) {
	r := newAdminRig(t)
	r.link(r.fx.SalmonSushi.ID, "MAKI-SAUMON-6")
	require.NoError(t, r.srv.SetInventory([]pkghubrise.InventoryEntry{pkghubrise.SoldOutSku("MAKI-SAUMON-6", nil)}))
	r.exec(`UPDATE hubrise_connection SET inventory_baseline_at = now()`)

	n, err := r.admin.SyncInventory(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var available bool
	require.NoError(t, r.db.GetContext(t.Context(), &available, `SELECT is_available FROM products WHERE id = $1`, r.fx.SalmonSushi.ID))
	assert.False(t, available, "HubRise sold it out")
}

func TestSyncInventoryNowIsRefusedWhenItCannotRun(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		r := newAdminRig(t, withoutConnection())
		_, err := r.admin.SyncInventory(t.Context())
		assert.ErrorIs(t, err, domain.ErrNotConnected)
	})
	t.Run("switched off", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithEnabled(false)))
		_, err := r.admin.SyncInventory(t.Context())
		assert.ErrorIs(t, err, domain.ErrNotEnabled)
	})
	t.Run("inventory sync off", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, true, false, false, true)))
		_, err := r.admin.SyncInventory(t.Context())
		assert.ErrorIs(t, err, domain.ErrNotEnabled)
	})
	t.Run("without the inventory service", func(t *testing.T) {
		e := newEnv(t)
		pooled := application.NewPooledClients(e.mod.Codec, e.srv.APIBaseURL(), e.srv.HTTPClient(), nil)
		admin, err := e.mod.NewAdmin(e.push, e.mod.NewCatalog(e.push, hubrise.CatalogOptions{Clients: pooled}), hubrise.AdminOptions{APIBaseURL: adminAPIBase})
		require.NoError(t, err)
		_, err = admin.SyncInventory(t.Context())
		assert.ErrorIs(t, err, domain.ErrNotEnabled)
	})
	t.Run("another process is syncing", func(t *testing.T) {
		r := newAdminRig(t)
		ok, err := r.mod.Leases.Acquire(t.Context(), application.LeaseInventorySync, "someone-else", r.clock.Now(), time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = r.admin.SyncInventory(t.Context())
		assert.ErrorIs(t, err, application.ErrInventoryBusy)
	})
}

// ---- outbox ----

func (r *adminRig) seedOutbox(n int, state string) {
	r.t.Helper()
	for range n {
		r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state, last_error)
			VALUES ('inventory', $1, '{"customer":"secret"}', $2, 'boom')`, "inv:sku:"+uuid.NewString(), state)
	}
}

func TestOutboxListsRowsWithoutTheirContent(t *testing.T) {
	r := newAdminRig(t)
	r.seedOutbox(3, "parked")
	r.seedOutbox(2, "pending")

	all, err := r.admin.Outbox(t.Context(), nil, 0)
	require.NoError(t, err)
	assert.Len(t, all, 5)
	for _, it := range all {
		assert.Nil(t, it.Payload, "the payload holds customer data")
	}

	parked := domain.OutboxParked
	only, err := r.admin.Outbox(t.Context(), &parked, 2)
	require.NoError(t, err)
	assert.Len(t, only, 2, "limited")
	for _, it := range only {
		assert.Equal(t, domain.OutboxParked, it.State)
	}
	assert.Greater(t, only[0].ID, only[1].ID, "newest first")
}

func TestOutboxLimitIsClamped(t *testing.T) {
	r := newAdminRig(t)
	r.seedOutbox(3, "pending")
	for _, first := range []int{-5, 0, 1000} {
		items, err := r.admin.Outbox(t.Context(), nil, first)
		require.NoError(t, err)
		assert.Len(t, items, 3)
	}
}

func TestRetryOutboxReactivatesParkedRows(t *testing.T) {
	r := newAdminRig(t)
	woken := 0
	r.admin = r.rebuildAdmin(func() { woken++ })
	r.seedOutbox(3, "parked")
	var one int64
	require.NoError(t, r.db.GetContext(t.Context(), &one, `SELECT id FROM hubrise_outbox ORDER BY id LIMIT 1`))

	n, err := r.admin.RetryOutbox(t.Context(), &one)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "one inventory row: its dedupe key has only itself")
	assert.Equal(t, 1, woken, "the worker is told to drain now")

	n, err = r.admin.RetryOutbox(t.Context(), nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assert.Zero(t, r.outboxRows("parked"))

	n, err = r.admin.RetryOutbox(t.Context(), nil)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to retry")
	assert.Equal(t, 2, woken, "and nobody is woken for nothing")
}

// rebuildAdmin builds the admin again with a wake function.
func (r *adminRig) rebuildAdmin(wake func()) *application.Admin {
	r.t.Helper()
	pooled := application.NewPooledClients(r.mod.Codec, r.srv.APIBaseURL(), r.srv.HTTPClient(), nil)
	oauth := r.srv.NewOAuth(r.t)
	return application.NewAdmin(application.AdminDeps{
		Config: application.AdminConfig{RedirectURL: adminAPIBase + "/x", CallbackURL: adminCallback}, OAuth: oauth,
		Connections: r.mod.Connections, OAuthStates: r.mod.OAuthStates, Links: r.mod.CatalogLinks, Orders: r.mod.Orders,
		Outbox: r.mod.Outbox, Codec: r.mod.Codec, Clients: pooled, Catalog: r.catalog.Service, Clock: r.clock, Wake: wake,
	})
}

// ---- order and products ----

func TestOrderInfo(t *testing.T) {
	r := newAdminRig(t)
	order := r.createOrder()

	info, err := r.admin.Order(t.Context(), order)
	require.NoError(t, err)
	assert.Nil(t, info, "an order never handed to HubRise has no HubRise side")

	r.exec(`INSERT INTO hubrise_orders (order_id, hubrise_order_id, push_state, last_remote_status, remote_total, needs_attention, pushed_at)
		VALUES ($1, 'hr-1', 'sent', 'accepted', 12.5, 'TOTAL_MISMATCH', now())`, order)
	info, err = r.admin.Order(t.Context(), order)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "hr-1", *info.HubriseOrderID)
	assert.Equal(t, domain.PushSent, info.PushState)
	assert.Equal(t, "accepted", *info.LastRemoteStatus)
	assert.Equal(t, "12.50", *info.RemoteTotal)
	assert.Equal(t, "TOTAL_MISMATCH", *info.NeedsAttention)
	assert.NotNil(t, info.PushedAt)

	r.exec(`UPDATE hubrise_orders SET remote_total = NULL`)
	info, err = r.admin.Order(t.Context(), order)
	require.NoError(t, err)
	assert.Nil(t, info.RemoteTotal)
}

func TestManagedProductsAreTheLinkedOnesWhileCatalogSyncIsOn(t *testing.T) {
	t.Run("catalog sync on", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, true, true, true, true)))
		r.link(r.fx.SalmonSushi.ID, "SKU-1")
		got, err := r.admin.ManagedProducts(t.Context(), []uuid.UUID{r.fx.SalmonSushi.ID, r.fx.TunaSushi.ID})
		require.NoError(t, err)
		assert.Equal(t, map[uuid.UUID]bool{r.fx.SalmonSushi.ID: true}, got)

		got, err = r.admin.ManagedProducts(t.Context(), nil)
		require.NoError(t, err)
		assert.Empty(t, got)

		r.exec(`UPDATE hubrise_catalog_links SET retired_at = now()`)
		got, err = r.admin.ManagedProducts(t.Context(), []uuid.UUID{r.fx.SalmonSushi.ID})
		require.NoError(t, err)
		assert.Empty(t, got, "a retired link is not a lock")
	})

	for name, seed := range map[string][]hubtest.SeedOption{
		"catalog sync off": {hubtest.WithFeatures(true, true, true, false, true)},
		"disabled":         {hubtest.WithEnabled(false), hubtest.WithFeatures(true, true, true, true, true)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAdminRig(t, withSeed(seed...))
			r.link(r.fx.SalmonSushi.ID, "SKU-1")
			got, err := r.admin.ManagedProducts(t.Context(), []uuid.UUID{r.fx.SalmonSushi.ID})
			require.NoError(t, err)
			assert.Empty(t, got, "nothing is locked then")
		})
	}

	t.Run("not connected", func(t *testing.T) {
		r := newAdminRig(t, withoutConnection())
		got, err := r.admin.ManagedProducts(t.Context(), []uuid.UUID{uuid.New()})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestCallbackURLFor(t *testing.T) {
	cb, redirect, err := application.CallbackURLFor(" https://shop.example.test/api/v1/ ")
	require.NoError(t, err)
	assert.Equal(t, "https://shop.example.test/api/v1/hubrise/callback", cb)
	assert.Equal(t, "https://shop.example.test/api/v1/hubrise/oauth/callback", redirect)

	for _, bad := range []string{"", "ftp://x", "not a url", "/relative"} {
		_, _, err := application.CallbackURLFor(bad)
		assert.Error(t, err, bad)
	}
}

func TestCallbackEventsAreExactlyTheOnesWeUse(t *testing.T) {
	ev := application.CallbackEvents()
	assert.Equal(t, map[string][]string{
		"order": {"update"}, "catalog": {"create", "update", "delete"}, "inventory": {"update", "patch"},
	}, ev)
	for resource, types := range ev {
		for _, typ := range types {
			assert.True(t, pkghubrise.ValidCallbackEvent(resource, typ), resource+"."+typ)
		}
	}
	ev["order"] = nil
	assert.NotNil(t, application.CallbackEvents()["order"], "a caller cannot change the registration")
}

func TestAdminAuditIsOptional(t *testing.T) {
	r := newAdminRig(t)
	pooled := application.NewPooledClients(r.mod.Codec, r.srv.APIBaseURL(), r.srv.HTTPClient(), nil)
	admin, err := r.mod.NewAdmin(r.push, r.catalog, hubrise.AdminOptions{Clients: pooled, APIBaseURL: adminAPIBase})
	require.NoError(t, err)
	require.NoError(t, admin.Disconnect(t.Context(), false), "no audit function: nothing recorded, nothing broken")
}

func TestNewAdminRejectsABadAPIBaseURL(t *testing.T) {
	e := newEnv(t)
	_, err := e.mod.NewAdmin(e.push, e.mod.NewCatalog(e.push, hubrise.CatalogOptions{}), hubrise.AdminOptions{APIBaseURL: "nonsense"})
	assert.Error(t, err)
}

func TestNewAdminDefaultsTheTokenClientToTheConfiguredAPI(t *testing.T) {
	e := newEnv(t, withoutConnection())
	pooled := application.NewPooledClients(e.mod.Codec, e.srv.APIBaseURL(), e.srv.HTTPClient(), nil)
	admin, err := e.mod.NewAdmin(e.push, e.mod.NewCatalog(e.push, hubrise.CatalogOptions{Clients: pooled}),
		hubrise.AdminOptions{Clients: pooled, OAuthHTTP: e.srv.HTTPClient(), APIBaseURL: adminAPIBase})
	require.NoError(t, err)

	authURL, err := admin.StartConnection(t.Context(), uuid.New())
	require.NoError(t, err)
	u, _ := url.Parse(authURL)
	res, err := admin.CompleteConnection(t.Context(), u.Query().Get("state"), e.srv.IssueAuthCode())
	require.NoError(t, err, "the default client reaches the API base URL of the config (the mock)")
	assert.False(t, res.RelinkRequired)
	assert.NotNil(t, e.srv.Callback(hubrisetest.Token))
}
