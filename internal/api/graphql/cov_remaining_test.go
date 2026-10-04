package graphql_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// The remaining failure paths of the resolvers: each one maps a failing collaborator to the error
// or log line the clients and operators rely on.

func TestQuoteOrderWhenCollaboratorsFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, token := testhelpers.SeedCustomer(t, env.DB.DB, "quoter")
	input := quoteInput("PICKUP", lines(env.Fixtures.SalmonSushi.ID, 1), nil)

	t.Run("a caller that cannot be looked up is quoted as a regular customer", func(t *testing.T) {
		tc := withResolver(t, env.TestContext, func(r *resolver.Resolver) {
			r.UserService = faultyUsers{UserService: env.Resolver.UserService, failGet: true}
		})
		q := quoteAs(t, tc, token, "en", input)
		assert.Equal(t, "12.50", q.Subtotal)
		assert.Empty(t, quoteCodes(q))
	})

	t.Run("a failing product lookup is a server fault, not a quote", func(t *testing.T) {
		tc := withResolver(t, env.TestContext, func(r *resolver.Resolver) {
			r.ProductService = faultyProducts{ProductService: env.Resolver.ProductService, failPricing: true}
		})
		resp := gqlAs(t, tc, token, "en", quoteOrderQuery, map[string]any{"input": input})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})
}

func TestCreateOrderWhenTheCouponRaceIsLost(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "racer", false)
	orders := newFaultyOrders(env.Resolver.OrderService)
	orders.createErr = &pq.Error{Code: "23505", Constraint: "one_active_coupon_order_per_user"}
	r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })

	_, err := r.Mutation().CreateOrder(env.ctxFor(c.id.String(), false, "en"), model.CreateOrderInput{
		OrderType: model.OrderTypeEnumPickup, Items: []*model.CreateOrderItemInput{{ProductID: env.Fixtures.SalmonSushi.ID, Quantity: 1}}})
	requireCode(t, err, apperr.CodeCouponAlreadyActive)
}

func TestUpdateOrderOfADeliveryAndRefundMailFailures(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	logs := captureLogs(t)

	t.Run("confirming a delivery order mails the customer with the address", func(t *testing.T) {
		c := env.newPushCustomer(t, "deliv", true)
		cat := testhelpers.SeedCategory(t, env.DB.DB, 80, map[string]string{"en": "Big", "fr": "Gros"})
		feast := testhelpers.SeedProduct(t, env.DB.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "FEAST", Price: "30.00", Names: map[string]string{"en": "Feast", "fr": "Festin"}})
		testhelpers.SeedAddress(t, env.DB.DB, "addr-deliv", 2000, "4000")
		order := mustCreateOrder(t, env.TestContext, c.token, "en", map[string]any{
			"orderType": "DELIVERY", "isOnlinePayment": false, "addressPlaceId": "addr-deliv", "items": lines(feast, 1)})
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(40)})
		env.Mail.WaitSubject(t, c.email, "Order confirmed")
	})

	t.Run("a refund is not announced to a customer who turned e-mails off", func(t *testing.T) {
		c := env.newPushCustomer(t, "quietrefund", false)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.Eventually(t, func() bool {
			var refunded string
			_ = env.DB.DB.GetContext(t.Context(), &refunded, `SELECT amount_refunded::text FROM mollie_payments WHERE order_id = $1`, order.ID)
			return refunded != "0" && refunded != ""
		}, 10*time.Second, 50*time.Millisecond)
		require.Never(t, func() bool { return len(env.Mail.MailTo(c.email)) > 0 }, 300*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("a refund whose customer cannot be loaded is still issued", func(t *testing.T) {
		c := env.newPushCustomer(t, "norefundmail", false)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		r := env.with(func(r *resolver.Resolver) {
			r.UserService = faultyUsers{UserService: env.Resolver.UserService, failGet: true}
		})
		got, err := r.Mutation().UpdateOrder(env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en"), uuid.MustParse(order.ID),
			model.UpdateOrderInput{Status: status(orderDomain.OrderStatusCanceled)})
		require.NoError(t, err)
		assert.Equal(t, orderDomain.OrderStatusCanceled, got.Status)
		waitOrderLogCount(t, logs, "failed to retrieve user", order.ID, 2) // the refund mail and the cancellation mail
		assert.NotEmpty(t, env.Mollie.CallsMatching("POST /v2/payments/"+order.Payment.MolliePaymentID))
	})

	t.Run("an order without a customer row resolves to a guest", func(t *testing.T) {
		ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
		o := &model.Order{ID: uuid.New()}
		customer, err := env.Resolver.Order().Customer(ctx, o)
		require.NoError(t, err)
		assert.Nil(t, customer)
		name, err := env.Resolver.Order().DisplayCustomerName(ctx, o)
		require.NoError(t, err)
		assert.Equal(t, "Guest", name)
	})
}

func TestProductAdministrationWhenTheStoreFails(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	product := env.Fixtures.SalmonSushi.ID
	group, err := env.Resolver.Mutation().CreateProductChoiceGroup(admin, model.CreateProductChoiceGroupInput{
		ProductID: product, MinSelections: 0, MaxSelections: 1, Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "G"}}})
	require.NoError(t, err)
	choice, err := env.Resolver.Mutation().CreateProductChoice(admin, model.CreateProductChoiceInput{
		ChoiceGroupID: &group.ID, PriceModifier: "1", Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "C"}}})
	require.NoError(t, err)
	with := func(f faultyProducts) *resolver.Resolver {
		f.ProductService = env.Resolver.ProductService
		return env.with(func(r *resolver.Resolver) { r.ProductService = f })
	}

	t.Run("the product cannot be read back after its update", func(t *testing.T) {
		r := with(faultyProducts{getCalls: &atomic.Int32{}, getFailsAfter: 1})
		_, err := r.Mutation().UpdateProduct(admin, product, model.UpdateProductInput{IsHalal: new(true)})
		require.ErrorContains(t, err, "failed to fetch product")
	})

	t.Run("choice and group writes that fail are reported", func(t *testing.T) {
		_, err := with(faultyProducts{updateGroup: errBoom}).Mutation().UpdateProductChoiceGroup(admin, group.ID, model.UpdateProductChoiceGroupInput{SortOrder: new(2)})
		require.ErrorContains(t, err, "failed to update product choice group")
		_, err = with(faultyProducts{createChoice: errBoom}).Mutation().CreateProductChoice(admin, model.CreateProductChoiceInput{ChoiceGroupID: &group.ID, PriceModifier: "1"})
		require.ErrorContains(t, err, "failed to create product choice")
		_, err = with(faultyProducts{updateChoice: errBoom}).Mutation().UpdateProductChoice(admin, choice.ID, model.UpdateProductChoiceInput{SortOrder: new(2)})
		require.ErrorContains(t, err, "failed to update product choice")
	})
}

func TestUserFieldsWhenCollaboratorsFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")

	t.Run("customer statistics that cannot be computed", func(t *testing.T) {
		orders := newFaultyOrders(env.Resolver.OrderService)
		orders.failStats = true
		r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })
		_, err := r.Query().CustomerStats(admin, nil)
		require.ErrorContains(t, err, "failed to get customer stats")
	})

	t.Run("a default address that cannot be read is an error, none is null", func(t *testing.T) {
		r := env.with(func(r *resolver.Resolver) {
			r.AddressService = faultyAddresses{AddressService: env.Resolver.AddressService}
		})
		_, err := r.User().Address(admin, &model.User{DefaultPlaceID: new("p")})
		require.ErrorContains(t, err, "failed to fetch user's default address")
		got, err := r.User().Address(admin, &model.User{})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("orders that cannot be loaded", func(t *testing.T) {
		broken := env.brokenResolver(t)
		_, err := broken.User().Orders(loadersFor(broken, "", false, "en"), &model.User{ID: uuid.New()})
		require.ErrorContains(t, err, "failed to load user orders")
	})
}

func TestOrderingOpenForAStoreReviewerWhoCannotBeLookedUp(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE restaurant_config SET opening_hours = '{}'::jsonb, ordering_hours = NULL`)
	require.NoError(t, err)
	user, _ := testhelpers.SeedCustomer(t, env.DB.DB, "reviewer")
	r := env.with(func(r *resolver.Resolver) {
		r.UserService = faultyUsers{UserService: env.Resolver.UserService, failGet: true}
	})

	open, err := r.RestaurantConfig().IsOrderingCurrentlyOpen(env.ctxFor(user.String(), false, "en"), &model.RestaurantConfig{})
	require.NoError(t, err)
	assert.False(t, open, "an unknown caller gets no store-review exception")
	open, err = env.Resolver.RestaurantConfig().IsOrderingCurrentlyOpen(env.ctxFor(user.String(), false, "en"), &model.RestaurantConfig{})
	require.NoError(t, err)
	assert.False(t, open, "a regular customer neither")
}

func TestNewOrderPushToAnAdminDeviceWhoseConnectionBreaks(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)
	require.NoError(t, env.Notif.RegisterDeviceToken(t.Context(), env.Fixtures.AdminUser.ID, "broken-ios-admin", "ios", "admin"))
	env.Resolver.SendNewOrderPush(&orderDomain.Order{ID: uuid.New(), Language: "fr", OrderType: orderDomain.OrderTypePickUp})
	waitLog(t, logs, "failed to send admin APNs push")
	assert.Contains(t, env.deviceTokens(t, env.Fixtures.AdminUser.ID), "broken-ios-admin", "a transport failure does not unregister the device")
}
