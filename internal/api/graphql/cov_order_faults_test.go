package graphql_test

import (
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// What updateOrder and createOrder do when a collaborator fails: a failure of a side effect (mail,
// push, lookups) is logged and never undoes the order, a failure of the order's own steps is
// reported and rolled back.

func status(s orderDomain.OrderStatus) *orderDomain.OrderStatus { return &s }

func eta(minutes int) *time.Time {
	t := time.Now().Add(time.Duration(minutes) * time.Minute)
	return &t
}

// waitOrderLogCount waits until the message was logged at least n times for that order. Filtering
// by order_id keeps a line written for another order (the e-mail and push goroutines of earlier
// subtests outlive them) from satisfying the wait.
func waitOrderLogCount(t *testing.T, logs *observer.ObservedLogs, msg, orderID string, n int) {
	t.Helper()
	count := func() int { return logs.FilterMessage(msg).FilterField(zap.String("order_id", orderID)).Len() }
	require.Eventually(t, func() bool { return count() >= n }, 20*time.Second, 20*time.Millisecond,
		"log %q written %d times for order %s, want %d", msg, count(), orderID, n)
}

func TestUpdateOrderStepsThatFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "steps", false)
	id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "fr")
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "fr")

	t.Run("the order cannot be read", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.getFailsAfter = 0
		r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })
		got, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		require.ErrorContains(t, err, "failed to get order")
		assert.Nil(t, got)
	})

	t.Run("the update cannot be saved", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.failUpdate = true
		r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })
		got, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		require.ErrorContains(t, err, "failed to update order status")
		assert.Nil(t, got)
		assert.Equal(t, "PENDING", orderStatusInDB(t, env, id))
	})

	t.Run("the updated order cannot be read back", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.getFailsAfter = 1
		r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })
		_, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{EstimatedReadyTime: eta(30)})
		require.ErrorContains(t, err, "failed to get order")
	})
}

func orderStatusInDB(t *testing.T, env *covEnv, id uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, env.DB.DB.GetContext(t.Context(), &s, `SELECT order_status FROM orders WHERE id = $1`, id))
	return s
}

func TestUpdateOrderSideEffectsThatFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	update := func(r *resolver.Resolver, id uuid.UUID, in model.UpdateOrderInput) {
		t.Helper()
		got, err := r.Mutation().UpdateOrder(ctx, id, in)
		require.NoError(t, err, "a failing side effect must not fail the update")
		require.NotNil(t, got)
	}

	t.Run("the customer cannot be loaded for any of the e-mails", func(t *testing.T) {
		c := env.newPushCustomer(t, "nouser", false)
		r := env.with(func(r *resolver.Resolver) {
			r.UserService = faultyUsers{UserService: env.Resolver.UserService, failGet: true}
		})
		id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")

		update(r, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed), EstimatedReadyTime: eta(30)})
		waitOrderLogCount(t, logs, "failed to retrieve user", id.String(), 1)
		update(r, id, model.UpdateOrderInput{EstimatedReadyTime: eta(50)})
		waitOrderLogCount(t, logs, "failed to retrieve user", id.String(), 2)
		update(r, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusAwaitingUp)})
		waitOrderLogCount(t, logs, "failed to retrieve user", id.String(), 3)
		update(r, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusCanceled)})
		waitOrderLogCount(t, logs, "failed to retrieve user", id.String(), 4)
		assert.Equal(t, "CANCELLED", orderStatusInDB(t, env, id))
	})

	t.Run("the product names of the confirmation e-mail cannot be loaded", func(t *testing.T) {
		c := env.newPushCustomer(t, "noproducts", false)
		order := env.placeOrder(t, c, "en")
		r := env.with(func(r *resolver.Resolver) {
			r.ProductService = faultyProducts{ProductService: env.Resolver.ProductService, failInvoiceNames: true}
		})
		update(r, uuid.MustParse(order.ID), model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		waitLog(t, logs, "failed to retrieve products")
	})

	t.Run("an order line whose product is gone cannot be put in the confirmation e-mail", func(t *testing.T) {
		c := env.newPushCustomer(t, "missingproduct", false)
		order := env.placeOrder(t, c, "en")
		r := env.with(func(r *resolver.Resolver) {
			r.ProductService = faultyProducts{ProductService: env.Resolver.ProductService, emptyInvoiceNames: true}
		})
		update(r, uuid.MustParse(order.ID), model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		waitLog(t, logs, "missing product details")
	})

	t.Run("an e-mail that the mail server refuses is logged, for every kind of e-mail", func(t *testing.T) {
		c := env.newPushCustomer(t, "bounce", true)
		env.Mail.Reject(c.email)

		order := env.placeOrder(t, c, "en") // the "received" e-mail bounces
		waitLog(t, logs, "failed to send order pending email")
		id := uuid.MustParse(order.ID)
		update(env.Resolver, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed), EstimatedReadyTime: eta(30)})
		waitLog(t, logs, "failed to send order confirmed email")
		update(env.Resolver, id, model.UpdateOrderInput{EstimatedReadyTime: eta(50)})
		waitLog(t, logs, "failed to send ready time updated email")
		update(env.Resolver, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusAwaitingUp)})
		waitLog(t, logs, "failed to send order ready email")
		update(env.Resolver, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusCanceled)})
		waitLog(t, logs, "failed to send order canceled email")

		paid := env.placeOnlineOrder(t, c)
		env.markPaid(t, paid.ID)
		update(env.Resolver, uuid.MustParse(paid.ID), model.UpdateOrderInput{Status: status(orderDomain.OrderStatusCanceled)})
		waitLog(t, logs, "failed to send refund issued email")
		assert.Empty(t, env.Mail.MailTo(c.email), "nothing was delivered")
	})

	t.Run("devices the push providers reject are dropped, other failures are logged", func(t *testing.T) {
		c := env.newPushCustomer(t, "badpush", false)
		env.registerDevices(t, c, true)
		id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")
		env.addActivityToken(t, id, "dead-activity")

		// The alert and the Live Update goroutines read the tokens together, so both see the dead ones.
		together := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, meet: 2, arrived: &atomic.Int32{}, t: t}
		})
		update(together, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		waitLog(t, logs, "failed to send alert push")
		waitLog(t, logs, "failed to send FCM push")
		waitLog(t, logs, "failed to send live activity push")
		waitLog(t, logs, "failed to send live update data message")
		require.Eventually(t, func() bool {
			got := env.deviceTokens(t, c.id)
			_, deadIOS := got[c.deadIOS]
			_, deadAndroid := got[c.deadAndroid]
			return !deadIOS && !deadAndroid
		}, 20*time.Second, 20*time.Millisecond, "dead devices are unregistered: %v", env.deviceTokens(t, c.id))
		assert.Contains(t, env.deviceTokens(t, c.id), c.refusedIOS, "a refused push does not unregister the device")
		assert.Contains(t, env.deviceTokens(t, c.id), c.ios)
	})

	t.Run("the ready-time alert reaches working devices and drops dead ones", func(t *testing.T) {
		c := env.newPushCustomer(t, "badeta", false)
		env.registerDevices(t, c, true)
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE orders SET estimated_ready_time = now() + interval '10 minutes' WHERE id = $1`, id)
		require.NoError(t, err)

		update(env.Resolver, id, model.UpdateOrderInput{EstimatedReadyTime: eta(45)})
		waitLog(t, logs, "failed to send ready time APNs push")
		waitLog(t, logs, "failed to send ready time FCM push")
		require.Eventually(t, func() bool {
			got := env.deviceTokens(t, c.id)
			_, deadIOS := got[c.deadIOS]
			_, deadAndroid := got[c.deadAndroid]
			return !deadIOS && !deadAndroid
		}, 20*time.Second, 20*time.Millisecond)
		require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, id.String())) == 1 }, 20*time.Second, 20*time.Millisecond)
	})

	t.Run("a live activity whose tokens cannot be cleared is logged", func(t *testing.T) {
		c := env.newPushCustomer(t, "noclear", false)
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		env.addActivityToken(t, id, "la-noclear")
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, clearActivity: true}
		})
		update(r, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusPickedUp)})
		waitLog(t, logs, "failed to clear live activity tokens")
	})

	t.Run("token lookups that fail leave the customer's devices alone", func(t *testing.T) {
		c := env.newPushCustomer(t, "notokens", false)
		env.registerDevices(t, c, false)
		id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")
		r := env.with(func(r *resolver.Resolver) {
			r.NotificationService = faultyNotif{NotificationService: env.Notif, tokens: true, activityTokens: true}
		})
		update(r, id, model.UpdateOrderInput{Status: status(orderDomain.OrderStatusConfirmed)})
		require.Never(t, func() bool { return len(env.APNs.PushesTo(c.ios)) > 0 }, 300*time.Millisecond, 20*time.Millisecond)
	})
}

func TestCreateOrderStepsThatFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true, PosTokens: []string{"pos-new-order"}})
	logs := captureLogs(t)
	c := env.newPushCustomer(t, "creator", true)
	input := func(online bool, extra map[string]any) model.CreateOrderInput {
		in := model.CreateOrderInput{
			OrderType: model.OrderTypeEnumPickup, IsOnlinePayment: online,
			Items: []*model.CreateOrderItemInput{{ProductID: env.Fixtures.SalmonSushi.ID, Quantity: 2}},
		}
		if code, ok := extra["coupon"].(string); ok {
			in.CouponCode = &code
		}
		return in
	}
	ctx := env.ctxFor(c.id.String(), false, "en")
	// A coupon to reserve.
	resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr",
		`mutation { createCoupon(input: {code: "FAULTY10", discountType: "PERCENTAGE", discountValue: "10", isActive: true}) { id } }`, nil)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)

	t.Run("a caller id that is not a uuid is refused", func(t *testing.T) {
		_, err := env.Resolver.Mutation().CreateOrder(env.ctxFor("nope", false, "en"), input(false, nil))
		require.ErrorContains(t, err, "invalid user ID")
	})

	t.Run("a caller that does not exist is refused", func(t *testing.T) {
		_, err := env.Resolver.Mutation().CreateOrder(env.ctxFor(uuid.NewString(), false, "en"), input(false, nil))
		require.ErrorContains(t, err, "failed to retrieve user")
	})

	t.Run("a coupon that cannot be reserved is a typed error and no order is stored", func(t *testing.T) {
		before := env.orderCountOf(t, c.id)
		r := env.with(func(r *resolver.Resolver) {
			r.CouponService = faultyCoupons{CouponService: env.Resolver.CouponService, incrementErr: errBoom}
		})
		_, err := r.Mutation().CreateOrder(ctx, input(false, map[string]any{"coupon": "FAULTY10"}))
		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeCouponReserveFailed, appErr.Code)

		r = env.with(func(r *resolver.Resolver) {
			r.CouponService = faultyCoupons{CouponService: env.Resolver.CouponService, incrementDenied: true}
		})
		_, err = r.Mutation().CreateOrder(ctx, input(false, map[string]any{"coupon": "FAULTY10"}))
		appErr, ok = apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeCouponExhausted, appErr.Code)
		assert.Equal(t, before, env.orderCountOf(t, c.id))
	})

	t.Run("an order that cannot be saved gives the coupon back, even if giving it back fails", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.failCreate = true
		r := env.with(func(r *resolver.Resolver) {
			r.OrderService = orders
			r.CouponService = faultyCoupons{CouponService: env.Resolver.CouponService, decrementErr: errBoom}
		})
		_, err := r.Mutation().CreateOrder(ctx, input(false, map[string]any{"coupon": "FAULTY10"}))
		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeOrderCreateFailed, appErr.Code)
		waitLog(t, logs, "failed to rollback coupon after order creation failure")
	})

	t.Run("a saved order whose lines cannot be matched to the priced products is an error", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.wrongItems = true
		r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })
		_, err := r.Mutation().CreateOrder(ctx, input(false, nil))
		require.ErrorContains(t, err, "missing product details for")
	})

	t.Run("a refused payment removes the order, and failures of the clean-up are logged", func(t *testing.T) {
		env.Mollie.SetFail(true, false, false)
		t.Cleanup(func() { env.Mollie.SetFail(false, false, false) })
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.failDelete = true
		r := env.with(func(r *resolver.Resolver) {
			r.OrderService = orders
			r.CouponService = faultyCoupons{CouponService: env.Resolver.CouponService, decrementErr: errBoom}
		})
		_, err := r.Mutation().CreateOrder(ctx, input(true, map[string]any{"coupon": "FAULTY10"}))
		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodePaymentFailed, appErr.Code)
		waitLog(t, logs, "failed to delete orphaned order")
		waitLog(t, logs, "failed to rollback coupon after payment creation failure")
	})

	t.Run("a cash order is published to the dashboards, mailed and pushed to staff", func(t *testing.T) {
		env.Mail.WaitMailTo(t, c.email, 0)
		admin := env.Fixtures.AdminUser.ID
		require.NoError(t, env.Notif.RegisterDeviceToken(t.Context(), admin, "admin-ios-new", "ios", "admin"))
		created, err := env.Resolver.Subscription().OrderCreated(ctx)
		require.NoError(t, err)

		order := env.placeOrder(t, c, "en")
		got := recvOrder(t, created)
		assert.Equal(t, order.ID, got.ID.String())
		env.Mail.WaitSubject(t, c.email, "Order pending validation")
		require.Eventually(t, func() bool {
			return len(env.APNs.PushesTo("admin-ios-new")) == 1 && len(env.FCM.PushesTo("pos-new-order")) == 1
		}, 20*time.Second, 20*time.Millisecond)
	})

	t.Run("an online order is neither published nor pushed to staff before it is paid", func(t *testing.T) {
		created, err := env.Resolver.Subscription().OrderCreated(ctx)
		require.NoError(t, err)
		pushesBefore := len(env.APNs.PushesTo("admin-ios-new"))
		order := env.placeOnlineOrder(t, c)
		select {
		case got := <-created:
			t.Fatalf("an unpaid online order was published: %v", got.ID)
		default:
		}
		require.Never(t, func() bool { return len(env.APNs.PushesTo("admin-ios-new")) != pushesBefore }, 300*time.Millisecond, 20*time.Millisecond)
		assert.NotEmpty(t, order.ID)
	})
}

func (e *covEnv) orderCountOf(t *testing.T, user uuid.UUID) int {
	t.Helper()
	return countRows(t, e.TestContext, `SELECT count(*) FROM orders WHERE user_id = $1`, user)
}
