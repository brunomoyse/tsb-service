package graphql_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// mockClients hands the worker the client of the HubRise mock.
type mockClients struct{ c *pkghubrise.Client }

func (m mockClients) Client(context.Context, *hubDomain.Connection) (*pkghubrise.Client, error) {
	return m.c, nil
}

// withHubrise connects the HubRise order push to the resolver: the hooks, and the order service's
// status observer, the way the composition root does it. It returns the mock and the push.
func (e *covEnv) withHubrise(t *testing.T, seed ...hubtest.SeedOption) (*hubrisetest.Server, *hubrise.Push) {
	t.Helper()
	srv := hubrisetest.New(t)
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: hubDomain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, e.DB.DB, nil)
	require.NoError(t, err)
	opts := append([]hubtest.SeedOption{
		hubtest.WithToken(hubrisetest.Token), hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)),
	}, seed...)
	hubtest.SeedConnection(t, e.DB.DB, opts...)
	push := mod.NewPush(hubrise.PushOptions{Clients: mockClients{c: srv.NewClient(t)}})
	hooks := resolver.HubriseOrderHooks{Push: push.Pusher}
	e.Resolver.Hubrise = hooks
	e.Resolver.OrderService.SetStatusObserver(hooks)
	return srv, push
}

func (e *covEnv) hubriseRows(t *testing.T, orderID, kind string) int {
	t.Helper()
	return countRows(t, e.TestContext, `SELECT count(*) FROM hubrise_outbox WHERE order_id = $1 AND kind = $2`, orderID, kind)
}

func (e *covEnv) channelOf(t *testing.T, orderID string) *string {
	t.Helper()
	var ch *string
	require.NoError(t, e.DB.DB.GetContext(t.Context(), &ch, `SELECT channel FROM orders WHERE id = $1`, orderID))
	return ch
}

// The order records where it was placed from: the app is recognised by the OIDC client of its token,
// never by anything the client sends.
func TestCreateOrderRecordsItsChannel(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	customer, webToken := testhelpers.SeedCustomer(t, env.DB.DB, "channel")
	appToken, err := testhelpers.GenerateTestAppAccessToken(customer.String())
	require.NoError(t, err)
	input := map[string]any{"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(env.Fixtures.SalmonSushi.ID, 1)}

	web := mustCreateOrder(t, env.TestContext, webToken, "fr", input)
	require.NotNil(t, env.channelOf(t, web.ID))
	assert.Equal(t, "web", *env.channelOf(t, web.ID))

	// The one active order a customer can hold is not an issue without a coupon.
	app := mustCreateOrder(t, env.TestContext, appToken, "fr", input)
	require.NotNil(t, env.channelOf(t, app.ID))
	assert.Equal(t, "app", *env.channelOf(t, app.ID))
}

// A cash order is actionable as soon as it is committed: it is queued for HubRise then, a later
// status change is queued after it, and the worker sends the order once with the status it has by then.
func TestCashOrderIsQueuedForHubriseAndSentWithItsCurrentStatus(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv, push := env.withHubrise(t)
	c := env.newPushCustomer(t, "hubrise-cash", false)

	order := env.placeOrder(t, c, "fr")
	assert.Equal(t, 1, env.hubriseRows(t, order.ID, "order_create"))
	assert.Empty(t, srv.Requests(), "queueing never calls HubRise from the request")

	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
	assert.Equal(t, 1, env.hubriseRows(t, order.ID, "order_status"))
	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED"})
	assert.Equal(t, 1, env.hubriseRows(t, order.ID, "order_status"), "no transition, no row")

	for range 5 {
		n, err := push.Worker.DrainOnce(t.Context())
		require.NoError(t, err)
		if n == 0 {
			break
		}
	}
	remote, ok := srv.OrderByPrivateRef(order.ID)
	require.True(t, ok, "HubRise has the order")
	assert.Equal(t, pkghubrise.StatusAccepted, remote.Status, "the status at send time, not the one at creation")
	assert.Len(t, srv.RequestsTo("POST", "/v1/location/orders"), 1)
	assert.Empty(t, srv.RequestsTo("PATCH", "/v1/location/orders"), "the create covered the status change")

	// From here on each change goes out as a PATCH.
	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "PREPARING"})
	for range 3 {
		_, err := push.Worker.DrainOnce(t.Context())
		require.NoError(t, err)
	}
	remote, _ = srv.OrderByPrivateRef(order.ID)
	assert.Equal(t, pkghubrise.StatusInPreparation, remote.Status)
}

// An online order waits for its payment: placing it queues nothing, and the order is not pushed twice.
func TestOnlineOrderIsNotQueuedBeforeItIsPaid(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, _ = env.withHubrise(t)
	c := env.newPushCustomer(t, "hubrise-online", false)

	order := mustCreateOrder(t, env.TestContext, c.token, "fr", map[string]any{
		"orderType": "PICKUP", "isOnlinePayment": true, "items": lines(env.Fixtures.SalmonSushi.ID, 1),
	})
	assert.Zero(t, env.hubriseRows(t, order.ID, "order_create"))
	assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_orders`))
}

// Standalone parity: with HubRise unconfigured, or configured but paused, ordering and status changes
// behave exactly as before and nothing reaches the integration tables or the network.
func TestOrdersAreUnchangedWithoutAnActiveHubriseConnection(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		env := setupCovEnv(t, covOptions{})
		require.Nil(t, env.Resolver.Hubrise)
		c := env.newPushCustomer(t, "standalone", false)
		order := env.placeOrder(t, c, "fr")
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`))
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_orders`))
	})

	for name, seed := range map[string][]hubtest.SeedOption{
		"disabled":        {hubtest.WithEnabled(false)},
		"pushing is off":  {hubtest.WithFeatures(false, true, true, false, true)},
		"status sync off": {hubtest.WithFeatures(true, false, true, false, true)},
	} {
		t.Run(name, func(t *testing.T) {
			env := setupCovEnv(t, covOptions{})
			srv, push := env.withHubrise(t, seed...)
			c := env.newPushCustomer(t, "paused", false)
			order := env.placeOrder(t, c, "fr")
			env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
			if name == "status sync off" {
				assert.Equal(t, 1, env.hubriseRows(t, order.ID, "order_create"), "orders are still pushed")
			} else {
				assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox`))
			}
			assert.Zero(t, env.hubriseRows(t, order.ID, "order_status"))
			for range 2 {
				_, err := push.Worker.DrainOnce(t.Context())
				require.NoError(t, err)
			}
			if name != "status sync off" {
				assert.Empty(t, srv.Requests(), "zero HTTP calls")
			}
		})
	}
}

// The test orders staff place to check the shop never reach the kitchen printer of the EPOS.
func TestTestOrdersAreNotPushedToHubrise(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, _ = env.withHubrise(t)
	c := env.newPushCustomer(t, "hubrise-test", false)
	order := env.placeOrder(t, c, "fr")
	_, err := env.DB.DB.ExecContext(t.Context(), `DELETE FROM hubrise_outbox; DELETE FROM hubrise_orders`)
	require.NoError(t, err)
	_, err = env.DB.DB.ExecContext(t.Context(), `UPDATE orders SET is_test = true WHERE id = $1`, order.ID)
	require.NoError(t, err)
	require.NoError(t, env.Resolver.Hubrise.OrderReady(t.Context(), uuid.MustParse(order.ID)))
	assert.Zero(t, env.hubriseRows(t, order.ID, "order_create"))
}
