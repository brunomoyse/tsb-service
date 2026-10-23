package graphql_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/modules/hubrise"
	hubApp "tsb-service/internal/modules/hubrise/application"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/interfaces"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// hubriseRig is the whole HubRise integration wired the way the composition root will: the order
// push, the inbound side with the real resolver as the order transitioner, the callback endpoint, and
// the mock HubRise registered to call it.
type hubriseRig struct {
	t      *testing.T
	env    *covEnv
	srv    *hubrisetest.Server
	mod    *hubrise.Module
	push   *hubrise.Push
	sync   *hubrise.Sync
	server *httptest.Server
}

func (e *covEnv) withHubriseSync(t *testing.T, seed ...hubtest.SeedOption) *hubriseRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := hubrisetest.New(t)
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: hubDomain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, e.DB.DB, nil)
	require.NoError(t, err)
	hubtest.SeedConnection(t, e.DB.DB, append([]hubtest.SeedOption{
		hubtest.WithToken(hubrisetest.Token), hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)),
		hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
	}, seed...)...)
	push := mod.NewPush(hubrise.PushOptions{Clients: mockClients{c: srv.NewClient(t)}})
	hooks := resolver.HubriseOrderHooks{Push: push.Pusher}
	e.Resolver.Hubrise = hooks
	e.Resolver.OrderService.SetStatusObserver(hooks)
	sync := mod.NewSync(push, hubrise.SyncOptions{Transitioner: resolver.HubriseOrderTransitioner{Resolver: e.Resolver}})

	router := gin.New()
	interfaces.NewCallbackHandler(sync.Receiver, nil).Register(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	_, err = srv.NewClient(t).SetCallback(t.Context(), pkghubrise.Callback{
		URL: server.URL + "/hubrise/callback", Events: map[string][]string{"order": {"update"}},
	})
	require.NoError(t, err)
	return &hubriseRig{t: t, env: e, srv: srv, mod: mod, push: push, sync: sync, server: server}
}

func (h *hubriseRig) drainAll() {
	h.t.Helper()
	for range 20 {
		n, err := h.push.Worker.DrainOnce(h.t.Context())
		require.NoError(h.t, err)
		if n == 0 {
			return
		}
	}
	h.t.Fatal("the outbox never emptied")
}

// pushOrder queues the push of an order and sends it.
func (h *hubriseRig) pushOrder(orderID string) pkghubrise.Order {
	h.t.Helper()
	require.NoError(h.t, h.env.Resolver.Hubrise.OrderReady(h.t.Context(), uuid.MustParse(orderID)))
	h.drainAll()
	remote, ok := h.srv.OrderByPrivateRef(orderID)
	require.True(h.t, ok, "HubRise has the order")
	return remote
}

// epos plays the EPOS: it changes the order's status at HubRise, HubRise calls the shop back, and
// the shop processes what it received.
func (h *hubriseRig) epos(remoteID string, status pkghubrise.OrderStatus) {
	h.t.Helper()
	require.NoError(h.t, h.srv.SetOrderStatus(remoteID, status))
	for _, d := range h.srv.Dispatch(h.t.Context(), hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: h.server.Client()}) {
		require.Equal(h.t, http.StatusOK, d.Status)
	}
	_, err := h.sync.Processor.ProcessOnce(h.t.Context())
	require.NoError(h.t, err)
}

func (h *hubriseRig) attention(orderID string) string {
	h.t.Helper()
	var code *string
	require.NoError(h.t, h.env.DB.DB.GetContext(h.t.Context(), &code, `SELECT needs_attention FROM hubrise_orders WHERE order_id = $1`, orderID))
	if code == nil {
		return ""
	}
	return *code
}

func (h *hubriseRig) patches() int {
	return len(h.srv.RequestsTo(http.MethodPatch, "/v1/location/orders"))
}

// A status the EPOS sets reaches the customer exactly as a dashboard change would: the e-mail, the
// push notifications, the dashboards' subscription, the history. And nothing is sent back to HubRise.
func TestHubriseStatusEventsDriveTheOrderLikeTheDashboard(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-status", true)
	env.registerDevices(t, c, false)
	all, stop := context.WithCancel(t.Context())
	defer stop()
	updatedAll, err := env.Resolver.Subscription().OrderUpdated(all)
	require.NoError(t, err)

	order := env.placeOrder(t, c, "en")
	id := uuid.MustParse(order.ID)
	env.Mail.WaitSubject(t, c.email, "Order received, awaiting confirmation")
	remote := h.pushOrder(order.ID)
	h.srv.ClearRequests()

	t.Run("accepted confirms the order and tells everyone", func(t *testing.T) {
		h.epos(remote.ID, pkghubrise.StatusAccepted)
		assert.Equal(t, "CONFIRMED", orderStatusInDB(t, env, id))
		assert.Equal(t, []string{"PENDING", "CONFIRMED"}, env.historyOf(t, id))
		env.Mail.WaitSubject(t, c.email, "Order confirmed")
		require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == 1 }, 20*time.Second, 20*time.Millisecond)
		// The subscription saw the order (the first publication is the one of the placed order or this one).
		var seen bool
		for range 3 {
			select {
			case o := <-updatedAll:
				seen = seen || (o.ID.String() == order.ID && string(o.Status) == "CONFIRMED")
			case <-time.After(2 * time.Second):
			}
			if seen {
				break
			}
		}
		assert.True(t, seen, "the dashboards were told")
	})

	t.Run("ready for collection sends the ready e-mail", func(t *testing.T) {
		h.epos(remote.ID, pkghubrise.StatusAwaitingCollection)
		assert.Equal(t, "AWAITING_PICK_UP", orderStatusInDB(t, env, id))
		env.Mail.WaitSubject(t, c.email, "Your order is ready!")
	})

	t.Run("completed picks it up", func(t *testing.T) {
		h.epos(remote.ID, pkghubrise.StatusCompleted)
		assert.Equal(t, "PICKED_UP", orderStatusInDB(t, env, id))
	})

	assert.Equal(t, []string{"PENDING", "CONFIRMED", "AWAITING_PICK_UP", "PICKED_UP"}, env.historyOf(t, id))
	assert.Zero(t, h.patches(), "no echo back to HubRise")
	assert.Zero(t, env.hubriseRows(t, order.ID, "order_status"), "nothing queued either")
	assert.Zero(t, h.srv.RequestsTo(http.MethodPost, "/v1/location/orders"), "and no second order")
}

// The shop and the EPOS both change the status within a few seconds: the shop's own PATCH and the
// EPOS's event cross, so the event payload is not trusted and the order is read again.
func TestHubriseEventNextToALocalPatchIsReconciledWithTheOrderAsHubRiseHasIt(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-cross", false)
	order := env.placeOrder(t, c, "en")
	id := uuid.MustParse(order.ID)
	remote := h.pushOrder(order.ID)

	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
	h.drainAll()
	assert.Equal(t, 1, h.patches(), "the dashboard's change went out")
	h.srv.ClearRequests()

	h.epos(remote.ID, pkghubrise.StatusInPreparation)
	assert.Equal(t, "PREPARING", orderStatusInDB(t, env, id))
	assert.Len(t, h.srv.RequestsTo(http.MethodGet, "/v1/location/orders/"), 1, "read again, not trusted")
	assert.Zero(t, h.patches())
	assert.Equal(t, []string{"PENDING", "CONFIRMED", "PREPARING"}, env.historyOf(t, id))
}

// A remote cancel of an order that was not paid online goes through the whole cancel path.
func TestHubriseCancelOfAnUnpaidOrderRollsBackTheCouponAndTellsTheCustomer(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-cancel", true)
	env.registerDevices(t, c, false)
	order := env.placeOrder(t, c, "en")
	id := uuid.MustParse(order.ID)
	remote := h.pushOrder(order.ID)

	var couponID uuid.UUID
	require.NoError(t, env.DB.DB.GetContext(t.Context(), &couponID, `
		INSERT INTO coupons (code, discount_type, discount_value, used_count) VALUES ('HUBRISE10', 'fixed', 1.00, 1) RETURNING id`))
	_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE orders SET coupon_code = 'HUBRISE10' WHERE id = $1`, id)
	require.NoError(t, err)
	_, err = env.DB.DB.ExecContext(t.Context(), `INSERT INTO coupon_users (coupon_id, user_id, used_count) VALUES ($1, $2, 1)`, couponID, c.id)
	require.NoError(t, err)

	h.epos(remote.ID, pkghubrise.StatusRejected)

	assert.Equal(t, "CANCELLED", orderStatusInDB(t, env, id))
	assert.Equal(t, 0, countRows(t, env.TestContext, `SELECT used_count FROM coupons WHERE id = $1`, couponID), "the coupon is given back")
	env.Mail.WaitSubject(t, c.email, "Order cancelled")
	require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == 1 }, 20*time.Second, 20*time.Millisecond)
	var reason *string
	require.NoError(t, env.DB.DB.GetContext(t.Context(), &reason, `SELECT cancellation_reason FROM orders WHERE id = $1`, id))
	require.NotNil(t, reason)
	assert.Equal(t, "OTHER", *reason)
	assert.Zero(t, h.patches(), "the cancel is not sent back to the system it came from")
	assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"))
}

func TestHubriseCancelOfAnOnlinePaidOrder(t *testing.T) {
	// payAndPush places an online order, pays it, and pushes it.
	payAndPush := func(t *testing.T, env *covEnv, h *hubriseRig, label string) (*pushCustomer, *createdOrder, pkghubrise.Order) {
		t.Helper()
		c := env.newPushCustomer(t, label, true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		return c, order, h.pushOrder(order.ID)
	}

	t.Run("by default staff decide: nothing is refunded and the order is flagged", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		h := env.withHubriseSync(t)
		c, order, remote := payAndPush(t, env, h, "hubrise-keep")
		id := uuid.MustParse(order.ID)

		h.epos(remote.ID, pkghubrise.StatusCancelled)

		assert.NotEqual(t, "CANCELLED", orderStatusInDB(t, env, id), "an EPOS mis-tap must never move money")
		assert.Equal(t, hubDomain.AttentionRemoteCancelledPaid, h.attention(order.ID))
		assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"+order.Payment.MolliePaymentID))

		// Staff cancel in the dashboard: refunded, and the matching PATCH is skipped.
		h.srv.ClearRequests()
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})
		assert.Equal(t, "CANCELLED", orderStatusInDB(t, env, id))
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+order.Payment.MolliePaymentID+"/refunds"), 1)
		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		h.drainAll()
		assert.Zero(t, h.patches(), "HubRise has it cancelled already")
	})

	t.Run("with the refund switch on the whole cancel path runs", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		h := env.withHubriseSync(t)
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE hubrise_connection SET auto_refund_on_cancel = TRUE`)
		require.NoError(t, err)
		c, order, remote := payAndPush(t, env, h, "hubrise-refund")
		id := uuid.MustParse(order.ID)
		payID := order.Payment.MolliePaymentID

		h.epos(remote.ID, pkghubrise.StatusCancelled)

		assert.Equal(t, "CANCELLED", orderStatusInDB(t, env, id))
		assert.Equal(t, []string{"POST /v2/payments/" + payID + "/refunds"}, env.Mollie.CallsMatching("POST /v2/payments/"+payID))
		assert.Equal(t, env.paymentCol(t, "amount", payID), env.paymentCol(t, "amount_refunded", payID))
		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		env.Mail.WaitSubject(t, c.email, "Order cancelled")
		assert.Equal(t, hubDomain.AttentionUnlinkedItem, h.attention(order.ID), "only the flag of the push: the cancel raised none")
		assert.Zero(t, h.patches())
	})

	t.Run("a payment that cannot be refunded leaves the order and flags it", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		h := env.withHubriseSync(t)
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE hubrise_connection SET auto_refund_on_cancel = TRUE`)
		require.NoError(t, err)
		_, order, remote := payAndPush(t, env, h, "hubrise-voucher")
		id := uuid.MustParse(order.ID)
		env.Mollie.SetNotRefundable(order.Payment.MolliePaymentID)

		h.epos(remote.ID, pkghubrise.StatusCancelled)

		assert.NotEqual(t, "CANCELLED", orderStatusInDB(t, env, id), "untouched")
		assert.Equal(t, hubDomain.AttentionRemoteCancelFailed, h.attention(order.ID))
		var state string
		var attempts int
		var next *time.Time
		require.NoError(t, env.DB.DB.QueryRowxContext(t.Context(), `SELECT state, attempts, next_attempt_at FROM hubrise_events`).Scan(&state, &attempts, &next))
		assert.Equal(t, "pending", state, "failed with backoff")
		assert.Equal(t, 1, attempts)
		require.NotNil(t, next)
		assert.True(t, next.After(time.Now()))
	})
}

// A redelivered or forged callback changes nothing.
func TestHubriseCallbackReplayAndForgery(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-replay", false)
	order := env.placeOrder(t, c, "en")
	id := uuid.MustParse(order.ID)
	remote := h.pushOrder(order.ID)
	forge := func() {
		require.NoError(t, h.srv.SetOrderStatus(remote.ID, pkghubrise.StatusAccepted))
		for _, d := range h.srv.Dispatch(t.Context(), hubrisetest.Dispatcher{
			Secret: hubrisetest.ClientSecret, Client: h.server.Client(), BadSignature: true,
		}) {
			assert.Equal(t, http.StatusUnauthorized, d.Status)
		}
	}
	forge()
	assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_events`), "a forged event is not stored")

	require.NoError(t, h.srv.SetOrderStatus(remote.ID, pkghubrise.StatusAccepted))
	for range 3 { // HubRise retried: the same event, three times
		for _, d := range h.srv.Dispatch(t.Context(), hubrisetest.Dispatcher{
			Secret: hubrisetest.ClientSecret, Client: h.server.Client(), Keep: true,
		}) {
			assert.Equal(t, http.StatusOK, d.Status)
		}
	}
	_, err := h.sync.Processor.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "CONFIRMED", orderStatusInDB(t, env, id))
	assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_events`))
	assert.Equal(t, []string{"PENDING", "CONFIRMED"}, env.historyOf(t, id), "applied once")
}

// The transitioner reports what the order service says, and maps the one error the sync treats
// specially.
func TestHubriseOrderTransitionerErrors(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_ = env.withHubriseSync(t)
	tr := resolver.HubriseOrderTransitioner{Resolver: env.Resolver}

	err := tr.Transition(t.Context(), uuid.New(), hubApp.OrderChange{Status: "CONFIRMED"})
	require.Error(t, err)
	appErr, ok := apperr.From(err)
	require.True(t, ok)
	assert.Equal(t, apperr.CodeNotFound, appErr.Code)
	assert.NotErrorIs(t, err, hubApp.ErrNotRefundable)

	c := env.newPushCustomer(t, "hubrise-tr", true)
	order := env.placeOnlineOrder(t, c)
	env.markPaid(t, order.ID)
	env.Mollie.SetNotRefundable(order.Payment.MolliePaymentID)
	err = tr.Transition(t.Context(), uuid.MustParse(order.ID), hubApp.OrderChange{Status: "CANCELLED"})
	require.ErrorIs(t, err, hubApp.ErrNotRefundable)

	// The ready time alone changes nothing else.
	cash := env.placeOrder(t, env.newPushCustomer(t, "hubrise-tr2", false), "en")
	eta := time.Now().Add(40 * time.Minute).Truncate(time.Second)
	require.NoError(t, tr.Transition(t.Context(), uuid.MustParse(cash.ID), hubApp.OrderChange{EstimatedReadyTime: &eta}))
	assert.Equal(t, "PENDING", orderStatusInDB(t, env, uuid.MustParse(cash.ID)))
}
