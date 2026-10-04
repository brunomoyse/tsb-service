package graphql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	notificationApplication "tsb-service/internal/modules/notification/application"
	notificationDomain "tsb-service/internal/modules/notification/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

var errBoom = errors.New("boom")

// faultyNotif is the notification service with chosen operations failing.
type faultyNotif struct {
	notificationApplication.NotificationService
	register, unregister, registerActivity bool
	adminTokens, tokens, activityTokens    bool
	clearActivity                          bool
}

func (f faultyNotif) RegisterDeviceToken(ctx context.Context, u uuid.UUID, tok, platform, role string) error {
	if f.register {
		return errBoom
	}
	return f.NotificationService.RegisterDeviceToken(ctx, u, tok, platform, role)
}

func (f faultyNotif) UnregisterDeviceToken(ctx context.Context, u uuid.UUID, tok string) error {
	if f.unregister {
		return errBoom
	}
	return f.NotificationService.UnregisterDeviceToken(ctx, u, tok)
}

func (f faultyNotif) RegisterLiveActivityToken(ctx context.Context, o uuid.UUID, tok string) error {
	if f.registerActivity {
		return errBoom
	}
	return f.NotificationService.RegisterLiveActivityToken(ctx, o, tok)
}

func (f faultyNotif) GetAdminDeviceTokens(ctx context.Context) ([]notificationDomain.DevicePushToken, error) {
	if f.adminTokens {
		return nil, errBoom
	}
	return f.NotificationService.GetAdminDeviceTokens(ctx)
}

func (f faultyNotif) GetDeviceTokens(ctx context.Context, u uuid.UUID) ([]notificationDomain.DevicePushToken, error) {
	if f.tokens {
		return nil, errBoom
	}
	return f.NotificationService.GetDeviceTokens(ctx, u)
}

func (f faultyNotif) GetLiveActivityTokens(ctx context.Context, o uuid.UUID) ([]notificationDomain.LiveActivityToken, error) {
	if f.activityTokens {
		return nil, errBoom
	}
	return f.NotificationService.GetLiveActivityTokens(ctx, o)
}

func (f faultyNotif) ClearLiveActivityTokens(ctx context.Context, o uuid.UUID) error {
	if f.clearActivity {
		return errBoom
	}
	return f.NotificationService.ClearLiveActivityTokens(ctx, o)
}

// with returns a copy of the resolver changed by mutate: the same services, one of them replaced.
func (e *covEnv) with(mutate func(r *resolver.Resolver)) *resolver.Resolver {
	r := *e.Resolver
	mutate(&r)
	return &r
}

// seedOrderRow inserts an order of the user in the status and returns its id.
func (e *covEnv) seedOrderRow(t *testing.T, user uuid.UUID, status, orderType, lang string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.DB.DB.QueryRowxContext(t.Context(), `
		INSERT INTO orders (user_id, order_type, order_status, total_price, language)
		VALUES ($1, $2, $3, 18.00, $4) RETURNING id`, user, orderType, status, lang).Scan(&id))
	return id
}

func (e *covEnv) deviceTokens(t *testing.T, user uuid.UUID) map[string]string {
	t.Helper()
	rows, err := e.DB.DB.QueryxContext(t.Context(), `SELECT device_token, role FROM device_push_tokens WHERE user_id = $1`, user)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var tok, role string
		require.NoError(t, rows.Scan(&tok, &role))
		out[tok] = role
	}
	return out
}

func TestDeviceTokenResolvers(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	id, token := testhelpers.SeedCustomer(t, env.DB.DB, "tokens")
	adminID := env.Fixtures.AdminUser.ID
	admin := adminToken(t, env.TestContext)
	const register = `mutation ($t: String!, $p: String!) { registerDeviceToken(deviceToken: $t, platform: $p) }`
	const unregister = `mutation ($t: String!) { unregisterDeviceToken(deviceToken: $t) }`

	t.Run("a customer's device is registered with the user role, an admin's with the admin role", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", register, map[string]any{"t": "cust-ios", "p": "ios"})
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"registerDeviceToken":true}`, string(resp.Data))
		resp = gqlAs(t, env.TestContext, admin, "fr", register, map[string]any{"t": "admin-android", "p": "android"})
		require.Empty(t, resp.Errors)

		assert.Equal(t, map[string]string{"cust-ios": "user"}, env.deviceTokens(t, id))
		assert.Equal(t, map[string]string{"admin-android": "admin"}, env.deviceTokens(t, adminID))
	})

	t.Run("registering the same device twice keeps one row", func(t *testing.T) {
		for range 2 {
			resp := gqlAs(t, env.TestContext, token, "fr", register, map[string]any{"t": "twice", "p": "android"})
			require.Empty(t, resp.Errors)
		}
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM device_push_tokens WHERE device_token = 'twice'`))
	})

	t.Run("only ios and android are platforms", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", register, map[string]any{"t": "x", "p": "windows"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "USER_ERROR", resp.Errors[0].Extensions["code"])
		assert.Equal(t, "platform must be 'ios' or 'android'", resp.Errors[0].Message)
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM device_push_tokens WHERE device_token = 'x'`))
	})

	t.Run("a storage failure is an internal error", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, register: true, unregister: true}
		})
		ctx := env.ctxFor(id.String(), false, "fr")
		ok, err := r.Mutation().RegisterDeviceToken(ctx, "t", "ios")
		require.ErrorIs(t, err, errBoom)
		assert.False(t, ok)
		ok, err = r.Mutation().UnregisterDeviceToken(ctx, "t")
		require.ErrorIs(t, err, errBoom)
		assert.False(t, ok)
	})

	t.Run("a caller id that is not a uuid is refused", func(t *testing.T) {
		ctx := utils.SetUserID(t.Context(), "not-a-uuid")
		ok, err := env.Resolver.Mutation().RegisterDeviceToken(ctx, "t", "ios")
		require.EqualError(t, err, "invalid user ID")
		assert.False(t, ok)
		ok, err = env.Resolver.Mutation().UnregisterDeviceToken(ctx, "t")
		require.EqualError(t, err, "invalid user ID")
		assert.False(t, ok)
		ok, err = env.Resolver.Mutation().RegisterLiveActivityToken(ctx, uuid.New(), "t")
		require.EqualError(t, err, "invalid user ID")
		assert.False(t, ok)
		n, err := env.Resolver.Mutation().UpdateMyOrdersLanguage(ctx, "fr")
		require.EqualError(t, err, "invalid user ID")
		assert.Zero(t, n)
	})

	t.Run("unregister removes the caller's own token only", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, admin, "fr", unregister, map[string]any{"t": "cust-ios"})
		require.Empty(t, resp.Errors)
		assert.Contains(t, env.deviceTokens(t, id), "cust-ios", "another user's unregister must not remove it")

		resp = gqlAs(t, env.TestContext, token, "fr", unregister, map[string]any{"t": "cust-ios"})
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"unregisterDeviceToken":true}`, string(resp.Data))
		assert.NotContains(t, env.deviceTokens(t, id), "cust-ios")
	})
}

func TestRegisterLiveActivityToken(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	owner, ownerTok := testhelpers.SeedCustomer(t, env.DB.DB, "owner")
	_, strangerTok := testhelpers.SeedCustomer(t, env.DB.DB, "stranger")
	orderID := env.seedOrderRow(t, owner, "CONFIRMED", "PICKUP", "fr")
	const m = `mutation ($o: ID!, $t: String!) { registerLiveActivityToken(orderId: $o, token: $t) }`

	t.Run("the owner binds an activity token to their order", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, ownerTok, "fr", m, map[string]any{"o": orderID.String(), "t": "activity-1"})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.JSONEq(t, `{"registerLiveActivityToken":true}`, string(resp.Data))
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM live_activity_tokens WHERE order_id = $1 AND push_token = 'activity-1'`, orderID))
	})

	t.Run("somebody else's order is reported as not found", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, strangerTok, "fr", m, map[string]any{"o": orderID.String(), "t": "activity-2"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "NOT_FOUND", resp.Errors[0].Extensions["code"])
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM live_activity_tokens WHERE push_token = 'activity-2'`))
	})

	t.Run("an order that does not exist is an error", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, ownerTok, "fr", m, map[string]any{"o": uuid.NewString(), "t": "activity-3"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})

	t.Run("a storage failure is an internal error", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, registerActivity: true}
		})
		ok, err := r.Mutation().RegisterLiveActivityToken(env.ctxFor(owner.String(), false, "fr"), orderID, "activity-4")
		require.ErrorIs(t, err, errBoom)
		assert.False(t, ok)
	})
}

func TestUpdateMyOrdersLanguage(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)
	owner, token := testhelpers.SeedCustomer(t, env.DB.DB, "lang")
	const m = `mutation ($l: String!) { updateMyOrdersLanguage(language: $l) }`
	langOf := func(id uuid.UUID) string {
		var l string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &l, `SELECT language FROM orders WHERE id = $1`, id))
		return l
	}

	t.Run("a language the shop does not speak is refused", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", m, map[string]any{"l": "klingon"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})

	t.Run("without in-progress orders nothing is updated", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", m, map[string]any{"l": "nl"})
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"updateMyOrdersLanguage":0}`, string(resp.Data))
	})

	pending := env.seedOrderRow(t, owner, "PENDING", "PICKUP", "fr")
	confirmed := env.seedOrderRow(t, owner, "CONFIRMED", "PICKUP", "fr")
	delivered := env.seedOrderRow(t, owner, "DELIVERED", "DELIVERY", "fr")
	_, err := env.DB.DB.ExecContext(t.Context(), `INSERT INTO live_activity_tokens (order_id, push_token) VALUES ($1, 'la-ok'), ($1, 'dead-ios')`, confirmed)
	require.NoError(t, err)
	_, err = env.DB.DB.ExecContext(t.Context(), `INSERT INTO device_push_tokens (user_id, device_token, platform, role) VALUES
		($1, 'phone-android', 'android', 'user'), ($1, 'phone-ios', 'ios', 'user'), ($1, 'refused-android', 'android', 'user')`, owner)
	require.NoError(t, err)

	t.Run("in-progress orders switch language and their live surfaces are re-pushed in it", func(t *testing.T) {
		// "NL_be" is what a device reports; the shop keeps the base language.
		resp := gqlAs(t, env.TestContext, token, "fr", m, map[string]any{"l": " NL_be "})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.JSONEq(t, `{"updateMyOrdersLanguage":2}`, string(resp.Data))
		assert.Equal(t, "nl", langOf(pending))
		assert.Equal(t, "nl", langOf(confirmed))
		assert.Equal(t, "fr", langOf(delivered), "a finished order keeps the language it was placed in")

		// The iOS Live Activity gets the update in Dutch...
		require.Eventually(t, func() bool { return len(env.APNs.pushesTo("la-ok")) == 1 }, 20*time.Second, 20*time.Millisecond)
		aps, _ := env.APNs.pushesTo("la-ok")[0].Payload["aps"].(map[string]any)
		assert.Equal(t, "update", aps["event"])
		state, _ := aps["content-state"].(map[string]any)
		assert.NotEmpty(t, state["subtitle"])
		assert.Equal(t, notificationApplication.GetLiveActivityContentState(orderDomain.OrderStatusConfirmed, "nl", "PICKUP", nil)["subtitle"], state["subtitle"])

		// ...and the Android devices (not the iOS one) get a data message with the Dutch text.
		require.Eventually(t, func() bool { return len(env.FCM.pushesTo("phone-android")) == 1 }, 20*time.Second, 20*time.Millisecond)
		data, _ := env.FCM.pushesTo("phone-android")[0].Payload["data"].(map[string]any)
		assert.Contains(t, data["deepLinkUrl"], confirmed.String(), "the data message is addressed to the order: %v", data)
		assert.Equal(t, "Bestelling bevestigd", data["title"])
		assert.Equal(t, "update", data["event"])
		assert.Empty(t, env.FCM.pushesTo("phone-ios"))
		// The PENDING order has no localized text and no live activity: one push in all.
		assert.Len(t, env.APNs.pushesTo("la-ok"), 1)

		// A dead live-activity token and a refused FCM token are logged, never fatal.
		waitLog(t, logs, "failed to re-push live activity (language)")
		waitLog(t, logs, "failed to re-push live update (language)")
	})

	t.Run("failing token lookups skip the re-push", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, activityTokens: true, tokens: true}
		})
		before := len(env.APNs.pushesTo("la-ok"))
		fcmBefore := env.FCM.count()
		n, err := r.Mutation().UpdateMyOrdersLanguage(env.ctxFor(owner.String(), false, "fr"), "en")
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		// The re-push runs off the request path: give it a moment to prove it stays silent.
		require.Never(t, func() bool {
			return len(env.APNs.pushesTo("la-ok")) != before || env.FCM.count() != fcmBefore
		}, 300*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("a storage failure is reported", func(t *testing.T) {
		closed := env.brokenResolver(t)
		_, err := closed.Mutation().UpdateMyOrdersLanguage(env.ctxFor(owner.String(), false, "fr"), "en")
		require.ErrorContains(t, err, "failed to update orders language")
	})
}

func TestSendNewOrderPush(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true, PosTokens: []string{"pos-1", "refused-pos"}})
	logs := captureLogs(t)
	admin := env.Fixtures.AdminUser.ID
	for tok, platform := range map[string]string{
		"adm-ios": "ios", "dead-ios": "ios", "refused-ios": "ios",
		"adm-android": "android", "dead-android": "android", "refused-android": "android",
	} {
		require.NoError(t, env.Notif.RegisterDeviceToken(t.Context(), admin, tok, platform, "admin"))
	}
	// A customer's own device must never be told about somebody else's order.
	custID, _ := testhelpers.SeedCustomer(t, env.DB.DB, "bystander")
	require.NoError(t, env.Notif.RegisterDeviceToken(t.Context(), custID, "cust-android", "android", "user"))

	newOrder := func(lang string) *orderDomain.Order {
		return &orderDomain.Order{ID: uuid.New(), Language: lang, OrderType: orderDomain.OrderTypePickUp, TotalPrice: decimal.RequireFromString("25.00")}
	}

	t.Run("without push providers, with no order, or for a store-review order nothing is sent", func(t *testing.T) {
		bare := env.with(func(r *resolver.Resolver) { r.APNsClient, r.FCMClient = nil, nil })
		bare.SendNewOrderPush(newOrder("fr"))
		env.Resolver.SendNewOrderPush(nil)
		test := newOrder("fr")
		test.IsTest = true
		env.Resolver.SendNewOrderPush(test)
		// All three return before starting any work, so the fakes have seen nothing yet.
		assert.Zero(t, env.FCM.count())
		assert.Empty(t, env.APNs.pushesTo("adm-ios"))
		assert.Equal(t, 1, logs.FilterMessage("suppressing new-order push for store-review test order").Len())
	})

	t.Run("admin devices and POS handhelds are alerted, dead tokens are dropped", func(t *testing.T) {
		order := newOrder("en")
		env.Resolver.SendNewOrderPush(order)

		require.Eventually(t, func() bool {
			return len(env.APNs.pushesTo("adm-ios")) == 1 && len(env.FCM.pushesTo("adm-android")) == 1 &&
				len(env.FCM.pushesTo("pos-1")) == 1 && len(env.FCM.pushesTo("refused-pos")) == 1
		}, 20*time.Second, 20*time.Millisecond)

		aps, _ := env.APNs.pushesTo("adm-ios")[0].Payload["aps"].(map[string]any)
		alert, _ := aps["alert"].(map[string]any)
		assert.Equal(t, "New order", alert["title"])
		assert.Equal(t, order.ID.String(), env.APNs.pushesTo("adm-ios")[0].Payload["orderId"])
		assert.Equal(t, "new_order", env.APNs.pushesTo("adm-ios")[0].Payload["type"])

		msg, _ := env.FCM.pushesTo("pos-1")[0].Payload["notification"].(map[string]any)
		assert.Equal(t, "New order", msg["title"])
		assert.Equal(t, "Awaiting confirmation", msg["body"])
		assert.Empty(t, env.FCM.pushesTo("cust-android"), "a customer's device is not an admin device")

		// Tokens the providers report as dead are removed; ones refused for another reason stay.
		require.Eventually(t, func() bool { return len(env.deviceTokens(t, admin)) == 4 }, 20*time.Second, 20*time.Millisecond)
		assert.NotContains(t, env.deviceTokens(t, admin), "dead-ios")
		assert.NotContains(t, env.deviceTokens(t, admin), "dead-android")
		assert.Contains(t, env.deviceTokens(t, admin), "refused-ios")
		waitLog(t, logs, "failed to send POS FCM push")
		waitLog(t, logs, "failed to send admin FCM push")
		waitLog(t, logs, "sending POS FCM pushes")
	})

	t.Run("a failing admin lookup does not stop the POS handhelds", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, adminTokens: true}
		})
		before := len(env.FCM.pushesTo("pos-1"))
		r.SendNewOrderPush(newOrder("fr"))
		require.Eventually(t, func() bool { return len(env.FCM.pushesTo("pos-1")) == before+1 }, 20*time.Second, 20*time.Millisecond)
		waitLog(t, logs, "failed to fetch admin device tokens")
	})

	t.Run("a failing POS lookup is logged", func(t *testing.T) {
		env.Pos.err = errBoom
		t.Cleanup(func() { env.Pos.err = nil })
		env.Resolver.SendNewOrderPush(newOrder("fr"))
		waitLog(t, logs, "failed to fetch POS FCM tokens")
	})

	t.Run("with APNs only, android admin devices are skipped", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) { r.FCMClient = nil })
		before := env.FCM.count()
		r.SendNewOrderPush(newOrder("fr"))
		require.Eventually(t, func() bool { return len(env.APNs.pushesTo("adm-ios")) >= 2 }, 20*time.Second, 20*time.Millisecond)
		assert.Equal(t, before, env.FCM.count())
	})
}
