package graphql_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/api/hubrisewire"
	"tsb-service/internal/modules/hubrise"
	hubApp "tsb-service/internal/modules/hubrise/application"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	paymentInterfaces "tsb-service/internal/modules/payment/interfaces"
	"tsb-service/internal/shared/audit"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// e2eRig is the HubRise integration installed the way the main program installs it
// (hubrisewire.Install): the hooks and observers on the real services, the two public endpoints
// served over HTTP, and the workers running on short ticks. The only fakes are the HubRise mock and
// the boundaries of the shop (Mollie, e-mail, push).
type e2eRig struct {
	*covEnv
	t        *testing.T
	srv      *hubrisetest.Server
	mod      *hubrise.Module
	rt       *hubrisewire.Runtime
	web      *httptest.Server
	adminTok string
	// dashboard is where the browser is sent after the OAuth round trip.
	dashboard string
}

const e2eDashboard = "https://dash.example.test/hubrise"

// quickTicks makes the workers notice work within a few milliseconds.
func quickTicks(o *hubrisewire.Options) {
	o.Push.Worker.Tick = 20 * time.Millisecond
	o.Sync.Processor.Tick = 20 * time.Millisecond
	o.Inventory.Events.Tick = 20 * time.Millisecond
}

// newHubriseModule builds the module of a deployment configured for the mock.
func newHubriseModule(t *testing.T, e *covEnv, srv *hubrisetest.Server) *hubrise.Module {
	t.Helper()
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: hubDomain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, e.DB.DB, nil)
	require.NoError(t, err)
	return mod
}

// installHubrise installs the integration on a resolver the way main does, serving its public
// endpoints on router. apiBase is the public base URL HubRise is given (with /api/v1).
func (e *covEnv) installHubrise(t *testing.T, res *resolver.Resolver, srv *hubrisetest.Server, router *gin.Engine, apiBase string,
	tune ...func(*hubrisewire.Options)) (*hubrise.Module, *hubrisewire.Runtime) {
	t.Helper()
	mod := newHubriseModule(t, e, srv)
	opts := hubrisewire.Options{
		Module: mod, Resolver: res, APIBaseURL: apiBase, DashboardURL: e2eDashboard,
		Audit: audit.NewRecorder(e.DB.DB), OAuthHTTP: srv.HTTPClient(),
		// The mock has no use for the rate limits of the real API.
		Clients: hubApp.NewPooledClients(mod.Codec, srv.APIBaseURL(), nil, pkghubrise.NewLimiter(pkghubrise.Limits{})),
	}
	quickTicks(&opts)
	for _, f := range tune {
		f(&opts)
	}
	rt, err := hubrisewire.Install(opts)
	require.NoError(t, err)
	require.NotNil(t, rt)
	t.Cleanup(rt.Stop)
	rt.Register(router.Group("/api/v1"))
	return mod, rt
}

// withHubriseInstalled installs the integration on the environment, not connected yet.
func (e *covEnv) withHubriseInstalled(t *testing.T, srv *hubrisetest.Server, tune ...func(*hubrisewire.Options)) *e2eRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	web := httptest.NewServer(router)
	t.Cleanup(web.Close)
	mod, rt := e.installHubrise(t, e.Resolver, srv, router, web.URL+"/api/v1", tune...)

	adminTok, err := testhelpers.GenerateTestAccessToken(e.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	return &e2eRig{covEnv: e, t: t, srv: srv, mod: mod, rt: rt, web: web, adminTok: adminTok, dashboard: e2eDashboard}
}

// runWorkers starts the workers of a runtime; they stop when the test ends.
func runWorkers(t *testing.T, rt *hubrisewire.Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the workers did not stop")
		}
	})
}

// run starts the workers; they stop when the test ends.
func (r *e2eRig) run() {
	r.t.Helper()
	runWorkers(r.t, r.rt)
}

func (r *e2eRig) ask(query string, vars map[string]any) graphqlResponseWithExtensions {
	r.t.Helper()
	return gqlAs(r.t, r.TestContext, r.adminTok, "en", query, vars)
}

func (r *e2eRig) mustAsk(query string, vars map[string]any) map[string]json.RawMessage {
	r.t.Helper()
	resp := r.ask(query, vars)
	require.Empty(r.t, resp.Errors, "%s: %+v", query, resp.Errors)
	var data map[string]json.RawMessage
	require.NoError(r.t, json.Unmarshal(resp.Data, &data))
	return data
}

// refuses asks as the admin and returns the code of the one error.
func (r *e2eRig) refuses(query string, vars map[string]any) string {
	r.t.Helper()
	resp := r.ask(query, vars)
	require.Len(r.t, resp.Errors, 1, "%s: %+v", query, resp.Errors)
	code, _ := resp.Errors[0].Extensions["code"].(string)
	return code
}

func (r *e2eRig) status() statusDoc {
	r.t.Helper()
	return decode[statusDoc](r.t, r.mustAsk(`{ hubriseStatus { `+statusFields+` } }`, nil)["hubriseStatus"])
}

func (r *e2eRig) settings(input string) statusDoc {
	r.t.Helper()
	return decode[statusDoc](r.t, r.mustAsk(`mutation { updateHubriseSettings(input: {`+input+`}) { `+statusFields+` } }`, nil)["updateHubriseSettings"])
}

// connect plays the admin: the authorization URL, HubRise approving, the browser coming back to the
// shop's OAuth endpoint. It returns where the browser is sent.
func (r *e2eRig) connect() *url.URL {
	r.t.Helper()
	authURL := decode[string](r.t, r.mustAsk(`mutation { startHubriseConnection }`, nil)["startHubriseConnection"])
	state, code := browse(r.t, r.srv, authURL)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(r.web.URL + "/api/v1/hubrise/oauth/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code))
	require.NoError(r.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(r.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(r.t, err)
	return loc
}

// publishMenu is the catalog the restaurant keeps in HubRise: the salmon sushi the shop already
// sells (matched by its menu code) and a new gyoza.
func (r *e2eRig) publishMenu() {
	r.t.Helper()
	product := func(ref, name, sku, price, code string) pkghubrise.Product {
		return pkghubrise.Product{
			Ref: ref, CategoryRef: "SUSHI", Name: name, TaxRate: &pkghubrise.TaxRate{Delivery: "6", Collection: "6", EatIn: "12"},
			Skus: []pkghubrise.Sku{{Ref: sku, Price: pkghubrise.Money(price + " EUR"), CustomFields: json.RawMessage(`{"code":"` + code + `"}`)}},
		}
	}
	require.NoError(r.t, r.srv.ReplaceCatalog(pkghubrise.CatalogData{
		Categories: []pkghubrise.Category{{Ref: "SUSHI", Name: "Sushi"}},
		Products: []pkghubrise.Product{
			product("P-SAUMON", "Sushi au Saumon", "SKU-SAUMON", "12.50", "SUSHI-SALMON"),
			product("P-GYOZA", "Gyoza", "SKU-GYOZA", "6.50", "GYOZA"),
		},
	}))
}

// eposEvents makes HubRise deliver what happened on its side to the shop's callback endpoint and
// returns the HTTP statuses the shop answered.
func (r *e2eRig) eposEvents() []int {
	r.t.Helper()
	var out []int
	for _, d := range r.srv.Dispatch(r.t.Context(), hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: r.web.Client()}) {
		out = append(out, d.Status)
	}
	return out
}

// epos plays the EPOS changing the status of an order and HubRise calling the shop back.
func (r *e2eRig) epos(remoteID string, status pkghubrise.OrderStatus) {
	r.t.Helper()
	require.NoError(r.t, r.srv.SetOrderStatus(remoteID, status))
	for _, s := range r.eposEvents() {
		require.Equal(r.t, http.StatusOK, s)
	}
}

// waitRemoteOrder waits for the worker to have pushed the order.
func (r *e2eRig) waitRemoteOrder(orderID string) pkghubrise.Order {
	r.t.Helper()
	var remote pkghubrise.Order
	require.Eventually(r.t, func() bool {
		var ok bool
		remote, ok = r.srv.OrderByPrivateRef(orderID)
		return ok
	}, 20*time.Second, 20*time.Millisecond, "the order never reached HubRise")
	return remote
}

func (r *e2eRig) quiet(d time.Duration) { time.Sleep(d) }

func (r *e2eRig) waitStatus(orderID uuid.UUID, want string) {
	r.t.Helper()
	require.Eventually(r.t, func() bool { return orderStatusInDB(r.t, r.covEnv, orderID) == want }, 20*time.Second, 20*time.Millisecond,
		"the order stayed %s instead of %s", orderStatusInDB(r.t, r.covEnv, orderID), want)
}

// attention is why staff must look at the order ("" when nothing).
func (r *e2eRig) attention(orderID string) string {
	r.t.Helper()
	var code *string
	require.NoError(r.t, r.DB.DB.GetContext(r.t.Context(), &code, `SELECT needs_attention FROM hubrise_orders WHERE order_id = $1`, orderID))
	if code == nil {
		return ""
	}
	return *code
}

// payOnline is the customer paying at Mollie and Mollie calling the shop's webhook, the real handler
// with the resolver as its notifier and order-ready hook, as in main.
func (r *e2eRig) payOnline(order *createdOrder) {
	r.t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/webhook", paymentInterfaces.NewPaymentHandler(r.Resolver.PaymentService, r.Resolver.Broker, r.Resolver, r.Resolver).UpdatePaymentStatusHandler)
	r.Mollie.MarkPaid(order.Payment.MolliePaymentID)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("id="+order.Payment.MolliePaymentID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(r.t, http.StatusOK, w.Code, w.Body.String())
}

// remoteRequests counts the calls the shop made to HubRise.
func (r *e2eRig) remoteRequests(method, path string) int {
	return len(r.srv.RequestsTo(method, path))
}

// productRow reads what the shop sells of a product: the price and whether it is orderable.
func (r *e2eRig) productRow(id uuid.UUID) (price string, available bool) {
	r.t.Helper()
	var row struct {
		Price     string `db:"price"`
		Available bool   `db:"is_available"`
	}
	require.NoError(r.t, r.DB.DB.GetContext(r.t.Context(), &row, `SELECT price::text AS price, is_available FROM products WHERE id = $1`, id))
	return row.Price, row.Available
}

func (r *e2eRig) managed(id uuid.UUID) bool {
	r.t.Helper()
	data := r.mustAsk(`query ($id: ID!) { product(id: $id) { hubriseManaged hubriseLockedFields } }`, map[string]any{"id": id.String()})
	return decode[struct{ HubriseManaged bool }](r.t, data["product"]).HubriseManaged
}

// A restaurant goes from nothing to a HubRise shop and back, in the order of the rollout ladder:
// connect, adopt the menu it has, preview and import the catalog, push orders, follow the EPOS,
// refund or not, stop selling what the kitchen ran out of, pause and resume without losing a thing.
// Everything goes through the real hooks, endpoints and workers; only HubRise, Mollie, the mail and
// the push services are fakes.
func TestHubriseRolloutFromConnectionToPauseAndResume(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	srv := hubrisetest.New(t)
	r := env.withHubriseInstalled(t, srv)
	r.run()
	salmon := env.Fixtures.SalmonSushi.ID

	t.Run("the deployment has HubRise but the restaurant has not connected", func(t *testing.T) {
		st := r.status()
		assert.True(t, st.Available)
		assert.False(t, st.Connected)
		assert.False(t, st.Enabled)
		// Orders are placed as ever and nothing leaves.
		c := env.newPushCustomer(t, "e2e-before", false)
		env.placeOrder(t, c, "fr")
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`))
		assert.Empty(t, srv.Requests())
	})

	t.Run("connecting returns the admin to the dashboard with the locations registered", func(t *testing.T) {
		loc := r.connect()
		assert.Equal(t, "dash.example.test", loc.Host)
		assert.Equal(t, "/hubrise", loc.Path)
		assert.Equal(t, "connected", loc.Query().Get("hubrise"))

		st := r.status()
		assert.True(t, st.Connected)
		assert.False(t, st.Enabled, "connected is not enabled: the admin turns the features on")
		require.NotNil(t, st.LocationID)
		assert.Equal(t, hubrisetest.LocationID, *st.LocationID)

		cb := srv.Callback(hubrisetest.Token)
		require.NotNil(t, cb, "HubRise was told where to call")
		assert.Equal(t, r.web.URL+"/api/v1/hubrise/callback", cb.URL)
		assert.Equal(t, map[string][]string{"order": {"update"}, "catalog": {"create", "update", "delete"}, "inventory": {"update", "patch"}}, cb.Events)
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM staff_audit_log WHERE action = 'hubrise.connect' AND success`))
	})

	r.publishMenu()

	t.Run("the menu the shop already has is adopted without touching it", func(t *testing.T) {
		st := r.settings(`enabled: true`)
		assert.True(t, st.Enabled)
		assert.False(t, st.Features.PushOrders || st.Features.SyncStatus || st.Features.SyncInventory || st.Features.SyncCatalog || st.SyncCustomers,
			"enabling starts no feature: each one is turned on by hand")
		// An order placed now, before the menu is linked, leaves nothing behind for HubRise.
		early := env.newPushCustomer(t, "e2e-enabled-only", false)
		earlyOrder := env.placeOrder(t, early, "fr")
		env.mustUpdateOrder(t, earlyOrder.ID, map[string]any{"status": "PICKED_UP"}) // served: no open order to catch up later
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`))
		r.quiet(100 * time.Millisecond)
		assert.Zero(t, r.remoteRequests(http.MethodPost, "/v1/location/orders"), "no order reaches HubRise")
		assert.Zero(t, r.remoteRequests(http.MethodPatch, "/v1/location/orders"))
		priceBefore, _ := r.productRow(salmon)

		preview := r.mustAsk(`mutation { previewHubriseCatalogSync(mode: LINK_ONLY) { changes { action key } } }`, nil)
		changes := decode[struct {
			Changes []struct{ Action, Key string }
		}](t, preview["previewHubriseCatalogSync"]).Changes
		assert.Contains(t, changes, struct{ Action, Key string }{"LINK", "SKU-SAUMON"})
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links`), "a preview writes nothing")

		res := r.mustAsk(`mutation { syncHubriseCatalog(mode: LINK_ONLY) { applied } }`, nil)
		assert.True(t, decode[struct{ Applied bool }](t, res["syncHubriseCatalog"]).Applied)
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product'`))
		priceAfter, _ := r.productRow(salmon)
		assert.Equal(t, priceBefore, priceAfter, "adoption changes no product data")
		assert.False(t, r.managed(salmon), "and nothing is locked while the catalog is not synced")
	})

	t.Run("HubRise becomes the source of truth once the catalog was previewed", func(t *testing.T) {
		assert.Equal(t, "HUBRISE_PREVIEW_REQUIRED", r.refuses(`mutation { updateHubriseSettings(input: { syncCatalog: true }) { enabled } }`, nil))
		r.mustAsk(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`, nil)
		assert.True(t, r.settings(`syncCatalog: true`).Features.SyncCatalog)

		res := r.mustAsk(`mutation { syncHubriseCatalog(mode: FULL) { applied failedBatches } }`, nil)
		assert.True(t, decode[struct{ Applied bool }](t, res["syncHubriseCatalog"]).Applied)
		assert.True(t, r.managed(salmon))
		assert.Equal(t, 2, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links WHERE entity_type = 'product'`), "the gyoza was created")

		// The price is HubRise's: the dashboard cannot change it any more.
		resp := r.ask(`mutation ($id: ID!) { updateProduct(id: $id, input: { price: "1.00" }) { id } }`, map[string]any{"id": salmon.String()})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, string(apperr.CodeHubriseManaged), resp.Errors[0].Extensions["code"])
	})

	// The shop sells what HubRise says from here on: look the new product up for the rest.
	var gyoza uuid.UUID
	require.NoError(t, env.DB.DB.GetContext(t.Context(), &gyoza, `SELECT product_id FROM hubrise_catalog_links WHERE hubrise_ref = 'SKU-GYOZA'`))

	t.Run("the features are turned on one by one, pushing stamping its starting point", func(t *testing.T) {
		pushSince := func() *time.Time {
			var since *time.Time
			require.NoError(t, env.DB.DB.GetContext(t.Context(), &since, `SELECT push_orders_since FROM hubrise_connection`))
			return since
		}
		// Connecting stamped the first value; turning the push on moves it to now, so the orders of the
		// time it was off are never replayed as history.
		connectedAt := pushSince()
		require.NotNil(t, connectedAt)
		r.quiet(5 * time.Millisecond)
		st := r.settings(`pushOrders: true`)
		assert.True(t, st.Features.PushOrders)
		assert.False(t, st.Features.SyncStatus || st.Features.SyncInventory || st.SyncCustomers, "the others stay off")
		stamped := pushSince()
		require.NotNil(t, stamped)
		assert.True(t, stamped.After(*connectedAt), "pushOrders becoming effective restamps push_orders_since")

		assert.True(t, r.settings(`syncStatus: true`).Features.SyncStatus)
		assert.True(t, r.settings(`syncCustomers: true`).SyncCustomers)
		assert.True(t, r.settings(`syncInventory: true`).Features.SyncInventory)
		assert.Equal(t, stamped.UnixMicro(), pushSince().UnixMicro(), "the other switches do not touch the stamp")
	})

	t.Run("an order is pushed with the refs of the catalog and follows the EPOS", func(t *testing.T) {
		c := env.newPushCustomer(t, "e2e-flow", true)
		env.registerDevices(t, c, false)
		order := mustCreateOrder(t, env.TestContext, c.token, "en", map[string]any{
			"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(salmon, 1, gyoza, 2),
		})
		id := uuid.MustParse(order.ID)

		remote := r.waitRemoteOrder(order.ID)
		assert.Equal(t, pkghubrise.StatusNew, remote.Status)
		var refs []string
		for _, it := range remote.Items {
			refs = append(refs, it.SkuRef)
		}
		assert.ElementsMatch(t, []string{"SKU-SAUMON", "SKU-GYOZA"}, refs)
		remoteTotal, err := remote.Total.Decimal()
		require.NoError(t, err)
		assert.True(t, decimal.RequireFromString(order.TotalPrice).Equal(remoteTotal), "HubRise adds up to what the customer pays: %s vs %s", remote.Total, order.TotalPrice)
		assert.Empty(t, r.attention(order.ID), "nothing for staff to look at")
		assert.Equal(t, 1, r.remoteRequests(http.MethodPost, "/v1/location/orders"))

		// The EPOS accepts: the order is confirmed, with the mail and the push of a dashboard change.
		r.epos(remote.ID, pkghubrise.StatusAccepted)
		r.waitStatus(id, "CONFIRMED")
		env.Mail.WaitSubject(t, c.email, "Order confirmed")
		require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == 1 }, 20*time.Second, 20*time.Millisecond)
		assert.Equal(t, []string{"PENDING", "CONFIRMED"}, env.historyOf(t, id))
		assert.Zero(t, r.remoteRequests(http.MethodPatch, "/v1/location/orders"), "no echo to the system it came from")

		// Staff move it on in the dashboard: the EPOS hears about it.
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "PREPARING"})
		require.Eventually(t, func() bool {
			got, _ := srv.Order(remote.ID)
			return got.Status == pkghubrise.StatusInPreparation
		}, 20*time.Second, 20*time.Millisecond)
	})

	t.Run("a paid order is pushed once the payment is confirmed, with its payment", func(t *testing.T) {
		c := env.newPushCustomer(t, "e2e-paid", false)
		order := env.placeOnlineOrder(t, c)
		r.quiet(100 * time.Millisecond)
		_, pushed := srv.OrderByPrivateRef(order.ID)
		assert.False(t, pushed, "not before it is paid")

		r.payOnline(order)
		remote := r.waitRemoteOrder(order.ID)
		require.Len(t, remote.Payments, 1)
		assert.Equal(t, "MOLLIE", remote.Payments[0].Ref)
		assert.Len(t, srv.Orders(), 2, "the first order and this one: one create per order")
	})

	t.Run("a cancel at the EPOS of a paid order is the staff's call unless the refund switch is on", func(t *testing.T) {
		c := env.newPushCustomer(t, "e2e-cancel-keep", true)
		order := env.placeOnlineOrder(t, c)
		r.payOnline(order)
		remote := r.waitRemoteOrder(order.ID)
		id := uuid.MustParse(order.ID)

		r.epos(remote.ID, pkghubrise.StatusCancelled)
		require.Eventually(t, func() bool { return r.attention(order.ID) == hubDomain.AttentionRemoteCancelledPaid }, 20*time.Second, 20*time.Millisecond)
		assert.NotEqual(t, "CANCELLED", orderStatusInDB(t, env, id), "a mis-tap at the till never moves money")
		assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"+order.Payment.MolliePaymentID+"/refunds"))
	})

	t.Run("with the refund switch on the whole cancel path runs", func(t *testing.T) {
		assert.True(t, r.settings(`autoRefundOnCancel: true`).AutoRefundOnCancel)
		c := env.newPushCustomer(t, "e2e-cancel-refund", true)
		order := env.placeOnlineOrder(t, c)
		r.payOnline(order)
		remote := r.waitRemoteOrder(order.ID)
		id := uuid.MustParse(order.ID)

		r.epos(remote.ID, pkghubrise.StatusCancelled)
		r.waitStatus(id, "CANCELLED")
		payID := order.Payment.MolliePaymentID
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID+"/refunds"), 1)
		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		assert.Equal(t, env.paymentCol(t, "amount", payID), env.paymentCol(t, "amount_refunded", payID))
	})

	t.Run("what the kitchen ran out of cannot be ordered, and coming back in stock reopens it", func(t *testing.T) {
		require.NoError(t, srv.SetInventory([]pkghubrise.InventoryEntry{pkghubrise.SoldOutSku("SKU-GYOZA", nil)}))
		for _, s := range r.eposEvents() {
			require.Equal(t, http.StatusOK, s)
		}
		require.Eventually(t, func() bool { _, ok := r.productRow(gyoza); return !ok }, 20*time.Second, 20*time.Millisecond)

		c := env.newPushCustomer(t, "e2e-soldout", false)
		got, orderErr := createOrderAs(t, env.TestContext, c.token, "fr", map[string]any{
			"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(gyoza, 1),
		})
		requireOrderError(t, got, orderErr, apperr.CodeProductUnavailable)

		// Staff put it back on the menu: HubRise is told, the stock entry goes away.
		resp := r.ask(`mutation ($id: ID!) { updateProduct(id: $id, input: { isAvailable: true }) { id isAvailable } }`, map[string]any{"id": gyoza.String()})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.Eventually(t, func() bool { return len(srv.Inventory()) == 0 }, 20*time.Second, 20*time.Millisecond)
		again := mustCreateOrder(t, env.TestContext, c.token, "fr", map[string]any{
			"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(gyoza, 1),
		})
		r.waitRemoteOrder(again.ID)
	})

	t.Run("pausing stops everything and resuming loses nothing and replays nothing", func(t *testing.T) {
		linksBefore := countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links`)
		priceBefore, _ := r.productRow(salmon)
		srv.ClearRequests()
		ordersBefore := len(srv.Orders())
		outboxBefore := countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`)

		assert.False(t, r.settings(`enabled: false`).Enabled)
		assert.False(t, r.managed(salmon), "nothing is locked while paused")

		// A customer orders, staff serve the order and toggle a product: the shop is the standalone shop.
		c := env.newPushCustomer(t, "e2e-paused", false)
		paused := env.placeOrder(t, c, "fr")
		env.mustUpdateOrder(t, paused.ID, map[string]any{"status": "PICKED_UP"})
		for _, avail := range []bool{false, true} {
			resp := r.ask(`mutation ($id: ID!, $a: Boolean!) { updateProduct(id: $id, input: { isAvailable: $a }) { id } }`,
				map[string]any{"id": gyoza.String(), "a": avail})
			require.Empty(t, resp.Errors, "%+v", resp.Errors)
		}
		r.quiet(200 * time.Millisecond)
		assert.Empty(t, srv.Requests(), "zero calls to HubRise")
		assert.Equal(t, outboxBefore, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`), "and queues nothing")
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_orders WHERE order_id = $1`, paused.ID))

		// Resumed: the links and the menu are as they were, the order served meanwhile is not replayed
		// to the kitchen, the next order goes through.
		assert.True(t, r.settings(`enabled: true`).Enabled)
		assert.Equal(t, linksBefore, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_links`))
		priceAfter, _ := r.productRow(salmon)
		assert.Equal(t, priceBefore, priceAfter)
		assert.True(t, r.managed(salmon), "and HubRise owns the menu again")
		require.NoError(t, r.rt.Push.Reconciler.RunOnce(t.Context()))

		next := mustCreateOrder(t, env.TestContext, env.newPushCustomer(t, "e2e-resumed", false).token, "fr", map[string]any{
			"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(salmon, 1),
		})
		r.waitRemoteOrder(next.ID)
		_, replayed := srv.OrderByPrivateRef(paused.ID)
		assert.False(t, replayed, "history is never replayed: %s", paused.ID)
		// The open orders of the last hour are the ones the reconciler may catch up (the grace for
		// the orders in flight when pushing is switched on); the served one is not among them.
		assert.GreaterOrEqual(t, len(srv.Orders()), ordersBefore+1)
	})
}
