package graphql_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
)

// The order read side: the staff lists, the customer's own orders, the history report and the
// fields of Order / OrderItem that resolve through loaders.

type orderRow struct {
	ID              string                      `json:"id"`
	Status          string                      `json:"status"`
	Type            string                      `json:"type"`
	TotalPrice      string                      `json:"totalPrice"`
	DisplayName     string                      `json:"displayCustomerName"`
	DisplayAddress  string                      `json:"displayAddress"`
	IsManualAddress bool                        `json:"isManualAddress"`
	OrderExtra      any                         `json:"orderExtra"`
	Customer        *struct{ ID, Email string } `json:"customer"`
	Address         *struct {
		ID, StreetName, HouseNumber, Postcode, MunicipalityName string
		Distance                                                float64
	} `json:"address"`
	Payment       *struct{ Status, MolliePaymentID string } `json:"payment"`
	StatusHistory []struct{ Status string }                 `json:"statusHistory"`
	Items         []struct {
		ProductID string                     `json:"productID"`
		Quantity  int                        `json:"quantity"`
		UnitPrice string                     `json:"unitPrice"`
		ChoiceID  *string                    `json:"choiceId"`
		Choice    *struct{ ID, Name string } `json:"choice"`
		Product   struct{ Name string }      `json:"product"`
		VatRate   string                     `json:"vatRateApplied"`
	} `json:"items"`
}

const orderFields = `
	id status type totalPrice displayCustomerName displayAddress isManualAddress orderExtra
	customer { id email }
	address { id streetName houseNumber postcode municipalityName distance }
	payment { status molliePaymentId }
	items { productID quantity unitPrice vatRateApplied choiceId choice { id name } product { name } }`

func (e *covEnv) queryOrders(t *testing.T, token, query string, vars map[string]any, path string) []orderRow {
	t.Helper()
	resp := gqlAs(t, e.TestContext, token, "en", query, vars)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Data, &raw))
	var out []orderRow
	require.NoError(t, json.Unmarshal(raw[path], &out))
	return out
}

func ids(rows []orderRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func TestOrderReadQueries(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	admin := adminToken(t, env.TestContext)
	db := env.DB.DB

	alice, aliceTok := testhelpers.SeedCustomer(t, db, "Alice")
	bob, bobTok := testhelpers.SeedCustomer(t, db, "Bob")
	insert := func(user uuid.UUID, orderType, status, total string, age time.Duration) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, db.QueryRowxContext(t.Context(), `
			INSERT INTO orders (user_id, order_type, order_status, total_price, created_at) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			user, orderType, status, total, time.Now().Add(-age)).Scan(&id))
		return id
	}
	a1 := insert(alice, "PICKUP", "DELIVERED", "10.00", 5*time.Hour)
	a2 := insert(alice, "DELIVERY", "DELIVERED", "30.00", 4*time.Hour)
	a3 := insert(alice, "PICKUP", "CONFIRMED", "20.00", 3*time.Hour)
	b1 := insert(bob, "PICKUP", "DELIVERED", "40.00", 2*time.Hour)
	b2 := insert(bob, "PICKUP", "CANCELLED", "99.00", time.Hour) // never in the history report

	t.Run("staff list every order, newest first", func(t *testing.T) {
		got := env.queryOrders(t, admin, `{ orders { id status } }`, nil, "orders")
		assert.Equal(t, []string{b2.String(), b1.String(), a3.String(), a2.String(), a1.String()}, ids(got),
			"newest (1h old) first, oldest (5h old) last")
	})

	t.Run("a staff member reads any order, a missing one is an error", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, admin, "en", `query ($id: ID!) { order(id: $id) { id totalPrice } }`, map[string]any{"id": b1.String()})
		require.Empty(t, resp.Errors)
		require.JSONEq(t, fmt.Sprintf(`{"order":{"id":%q,"totalPrice":"40"}}`, b1), string(resp.Data))

		resp = gqlAs(t, env.TestContext, admin, "en", `query ($id: ID!) { order(id: $id) { id } }`, map[string]any{"id": uuid.NewString()})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "NOT_FOUND", resp.Errors[0].Extensions["code"])
		assert.Equal(t, "order not found", resp.Errors[0].Message, "a client error keeps its message, it is not the generic internal one")
	})

	t.Run("a customer asking for an order that does not exist gets NOT_FOUND", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, aliceTok, "en", `query ($id: ID!) { myOrder(id: $id) { id } }`, map[string]any{"id": uuid.NewString()})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "NOT_FOUND", resp.Errors[0].Extensions["code"])
	})

	const customerOrders = `query ($u: ID!, $first: Int, $page: Int) { customerOrders(userId: $u, first: $first, page: $page) { id } }`
	t.Run("customerOrders pages through one customer's orders", func(t *testing.T) {
		all := env.queryOrders(t, admin, customerOrders, map[string]any{"u": alice.String()}, "customerOrders")
		assert.ElementsMatch(t, []string{a1.String(), a2.String(), a3.String()}, ids(all))

		page2 := env.queryOrders(t, admin, customerOrders, map[string]any{"u": alice.String(), "first": 2, "page": 2}, "customerOrders")
		assert.Len(t, page2, 1)
		page1 := env.queryOrders(t, admin, customerOrders, map[string]any{"u": alice.String(), "first": 2, "page": 1}, "customerOrders")
		assert.Len(t, page1, 2)
		assert.NotContains(t, ids(page1), page2[0].ID)

		// A page or size below 1 falls back to the defaults.
		def := env.queryOrders(t, admin, customerOrders, map[string]any{"u": alice.String(), "first": 0, "page": -3}, "customerOrders")
		assert.Len(t, def, 3)
	})

	t.Run("myOrders only ever shows the caller's own orders", func(t *testing.T) {
		mine := env.queryOrders(t, aliceTok, `{ myOrders { id } }`, nil, "myOrders")
		assert.ElementsMatch(t, []string{a1.String(), a2.String(), a3.String()}, ids(mine))
		paged := env.queryOrders(t, aliceTok, `{ myOrders(first: 2, page: 2) { id } }`, nil, "myOrders")
		assert.Len(t, paged, 1)
		bobs := env.queryOrders(t, bobTok, `{ myOrders { id } }`, nil, "myOrders")
		assert.Len(t, bobs, 2)
		assert.NotContains(t, ids(bobs), a1.String())
	})

	const history = `query ($input: OrderHistoryInput) { orderHistory(input: $input) { orders { id totalPrice } summary { totalOrders totalRevenue averageOrder } } }`
	type historyResp struct {
		OrderHistory struct {
			Orders  []struct{ ID, TotalPrice string }
			Summary struct {
				TotalOrders  int
				TotalRevenue string
				AverageOrder string
			}
		}
	}
	runHistory := func(t *testing.T, input map[string]any) historyResp {
		t.Helper()
		vars := map[string]any{}
		if input != nil {
			vars["input"] = input
		}
		resp := gqlAs(t, env.TestContext, admin, "en", history, vars)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var out historyResp
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return out
	}

	t.Run("the history report leaves out cancelled orders and totals the rest", func(t *testing.T) {
		got := runHistory(t, nil).OrderHistory
		assert.Equal(t, 4, got.Summary.TotalOrders)
		assert.True(t, decimal.RequireFromString("100").Equal(decimal.RequireFromString(got.Summary.TotalRevenue)), got.Summary.TotalRevenue)
		assert.True(t, decimal.RequireFromString("25").Equal(decimal.RequireFromString(got.Summary.AverageOrder)), got.Summary.AverageOrder)
		assert.Len(t, got.Orders, 4)
	})

	t.Run("the history report filters by type, status, search text and dates, and pages", func(t *testing.T) {
		got := runHistory(t, map[string]any{"orderType": "DELIVERY"}).OrderHistory
		require.Len(t, got.Orders, 1)
		assert.Equal(t, a2.String(), got.Orders[0].ID)

		got = runHistory(t, map[string]any{"status": "CONFIRMED"}).OrderHistory
		require.Len(t, got.Orders, 1)
		assert.Equal(t, a3.String(), got.Orders[0].ID)

		got = runHistory(t, map[string]any{"search": "bob"}).OrderHistory
		require.Len(t, got.Orders, 1)
		assert.Equal(t, b1.String(), got.Orders[0].ID)

		got = runHistory(t, map[string]any{
			"startDate": time.Now().Add(-270 * time.Minute).Format(time.RFC3339),
			"endDate":   time.Now().Add(-210 * time.Minute).Format(time.RFC3339),
		}).OrderHistory
		require.Len(t, got.Orders, 1, "only the order of four hours ago")
		assert.Equal(t, a2.String(), got.Orders[0].ID)

		got = runHistory(t, map[string]any{"first": 3, "page": 2}).OrderHistory
		assert.Len(t, got.Orders, 1)
		assert.Equal(t, 4, got.Summary.TotalOrders, "the summary covers all pages")
	})

	t.Run("the staff queries refuse a customer, the customer ones refuse a stranger", func(t *testing.T) {
		for _, q := range []string{`{ orders { id } }`, `{ orderHistory { summary { totalOrders } } }`} {
			resp := gqlAs(t, env.TestContext, aliceTok, "en", q, nil)
			require.Len(t, resp.Errors, 1, q)
			assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"], q)
		}
		resp := gqlAs(t, env.TestContext, "", "en", `{ myOrders { id } }`, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})

	t.Run("statusHistory is for staff only", func(t *testing.T) {
		const q = `query ($id: ID!) { myOrder(id: $id) { id statusHistory { status } } }`
		resp := gqlAs(t, env.TestContext, aliceTok, "en", q, map[string]any{"id": a1.String()})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
	})
}

func TestOrderReadStepsThatFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	user, _ := testhelpers.SeedCustomer(t, env.DB.DB, "reads")
	id := env.seedOrderRow(t, user, "CONFIRMED", "PICKUP", "en")
	ctx := env.ctxFor(user.String(), true, "en")

	orders := newFaultyOrders(env.Resolver.OrderService)
	orders.failOrdersQuery, orders.failHistory, orders.failStatusHist = true, true, true
	orders.getFailsAfter = 0
	r := env.with(func(r *resolver.Resolver) { r.OrderService = orders })

	t.Run("a failing store is reported by every read", func(t *testing.T) {
		_, err := r.Query().Orders(ctx)
		require.ErrorContains(t, err, "failed to get orders")
		_, err = r.Query().Order(ctx, id)
		require.ErrorContains(t, err, "failed to get order")
		_, err = r.Query().CustomerOrders(ctx, user, nil, nil)
		require.ErrorContains(t, err, "failed to get customer orders")
		_, err = r.Query().MyOrders(ctx, nil, nil)
		require.ErrorContains(t, err, "failed to get orders")
		_, err = r.Query().MyOrder(ctx, id)
		require.ErrorContains(t, err, "failed to get order")
		_, err = r.Query().OrderHistory(ctx, nil)
		require.ErrorContains(t, err, "failed to get order history")
		_, err = r.Order().StatusHistory(ctx, &model.Order{ID: id})
		require.ErrorContains(t, err, "failed to load status history")
	})

	t.Run("an order the service reports as nothing is nothing", func(t *testing.T) {
		nothing := newFaultyOrders(env.Resolver.OrderService)
		nothing.nilOrder = true
		r := env.with(func(r *resolver.Resolver) { r.OrderService = nothing })
		got, err := r.Query().Order(ctx, id)
		require.NoError(t, err)
		assert.Nil(t, got)
		got, err = r.Query().MyOrder(ctx, id)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a caller id that is not a uuid cannot list orders", func(t *testing.T) {
		_, err := env.Resolver.Query().MyOrders(env.ctxFor("nope", false, "en"), nil, nil)
		require.ErrorContains(t, err, "invalid user ID")
	})

	t.Run("somebody else's order is forbidden with a code the presenter keeps quiet", func(t *testing.T) {
		other, _ := testhelpers.SeedCustomer(t, env.DB.DB, "other")
		_, err := env.Resolver.Query().MyOrder(env.ctxFor(other.String(), false, "en"), id)
		require.ErrorContains(t, err, "FORBIDDEN")
	})
}

func TestOrderFieldResolvers(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	admin := adminToken(t, env.TestContext)
	customer, token := testhelpers.SeedCustomer(t, db, "Fields")
	_, err := db.ExecContext(t.Context(), `UPDATE users SET first_name = 'Zoe', last_name = 'Martin' WHERE id = $1`, customer)
	require.NoError(t, err)
	testhelpers.SeedAddress(t, db, "addr-fields", 2000, "4000")

	cat := testhelpers.SeedCategory(t, db, 70, map[string]string{"en": "Mains", "fr": "Plats"})
	bowl := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: cat, Code: "BOWL", Price: "30.00", Names: map[string]string{"en": "Bowl", "fr": "Bol"}})
	group := testhelpers.SeedChoiceGroup(t, db, bowl, 1, 1, map[string]string{"en": "Size", "fr": "Taille"})
	large := testhelpers.SeedChoice(t, db, bowl, group, "1.00", map[string]string{"en": "Large", "fr": "Grand"})

	// A delivery order with an address, extras, a note and a legacy single choice, paid online.
	create := mustCreateOrder(t, env.TestContext, token, "en", map[string]any{
		"orderType": "DELIVERY", "isOnlinePayment": true, "addressPlaceId": "addr-fields", "addressExtra": "2nd floor",
		"orderNote": "ring twice", "orderExtra": []map[string]any{{"name": "sauce", "options": []string{"soy"}}, {"name": "chopsticks"}},
		"items": []map[string]any{{"productId": bowl.String(), "quantity": 1, "choiceId": large.String()}},
	})
	pickup := env.placeOrder(t, &pushCustomer{token: token}, "en")

	t.Run("an order resolves its customer, address, payment, items and extras", func(t *testing.T) {
		byID := func(id string) *orderRow {
			resp := gqlAs(t, env.TestContext, admin, "en", fmt.Sprintf(`query ($id: ID!) { order(id: $id) { %s } }`, orderFields), map[string]any{"id": id})
			require.Empty(t, resp.Errors, "%+v", resp.Errors)
			var data struct{ Order orderRow }
			require.NoError(t, json.Unmarshal(resp.Data, &data))
			return &data.Order
		}
		delivery, pick := byID(create.ID), byID(pickup.ID)

		assert.Equal(t, "MARTIN Zoe", delivery.DisplayName)
		require.NotNil(t, delivery.Customer)
		assert.Equal(t, customer.String(), delivery.Customer.ID)
		require.NotNil(t, delivery.Address)
		assert.Equal(t, "Rue du Test", delivery.Address.StreetName)
		assert.Equal(t, "Rue du Test 1, 4000 Liege", delivery.DisplayAddress)
		assert.False(t, delivery.IsManualAddress)
		require.NotNil(t, delivery.Payment)
		assert.Equal(t, "open", delivery.Payment.Status)
		assert.NotEmpty(t, delivery.Payment.MolliePaymentID)
		assert.Equal(t, []any{
			map[string]any{"name": "sauce", "options": []any{"soy"}},
			map[string]any{"name": "chopsticks"},
		}, delivery.OrderExtra)
		require.Len(t, delivery.Items, 1)
		item := delivery.Items[0]
		assert.Equal(t, bowl.String(), item.ProductID)
		assert.Equal(t, "Bowl", item.Product.Name)
		assert.Equal(t, "6.00", item.VatRate)
		require.NotNil(t, item.ChoiceID)
		require.NotNil(t, item.Choice)
		assert.Equal(t, "Large", item.Choice.Name)

		assert.Equal(t, "Pickup", pick.DisplayAddress)
		assert.Nil(t, pick.Address)
		assert.Nil(t, pick.Payment)
		assert.Equal(t, "MARTIN Zoe", pick.DisplayName)
	})

	t.Run("without the request's loaders a field reports it instead of panicking", func(t *testing.T) {
		bare := t.Context()
		r := env.Resolver
		o := &model.Order{ID: uuid.New()}
		_, err := r.Order().Customer(bare, o)
		require.EqualError(t, err, "no order user loader found")
		_, err = r.Order().Payment(bare, o)
		require.EqualError(t, err, "no order payment loader found")
		_, err = r.Order().Items(bare, o)
		require.EqualError(t, err, "no order items loader found")
		_, err = r.OrderItem().Product(bare, &model.OrderItem{ProductID: uuid.New()})
		require.EqualError(t, err, "no order item product loader found")
		_, err = r.OrderItemSelection().Group(bare, &model.OrderItemSelection{GroupID: uuid.New()})
		require.EqualError(t, err, "no choice group loader found")
		_, err = r.OrderItemSelection().Choice(bare, &model.OrderItemSelection{ChoiceID: uuid.New()})
		require.EqualError(t, err, "no choice loader found")

		// A customer that cannot be loaded does not break the order list: it shows as a guest.
		name, err := r.Order().DisplayCustomerName(bare, o)
		require.NoError(t, err)
		assert.Equal(t, "Guest", name)
	})

	t.Run("loaders that fail report which field failed", func(t *testing.T) {
		broken := env.brokenResolver(t)
		ctx := loadersFor(broken, customer.String(), true, "en")
		o := &model.Order{ID: uuid.New()}
		_, err := broken.Order().Customer(ctx, o)
		require.ErrorContains(t, err, "failed to load order user")
		_, err = broken.Order().Payment(ctx, o)
		require.ErrorContains(t, err, "failed to load order payment")
		_, err = broken.Order().Items(ctx, o)
		require.ErrorContains(t, err, "failed to load order items")
		_, err = broken.OrderItem().Product(ctx, &model.OrderItem{ProductID: uuid.New()})
		require.ErrorContains(t, err, "failed to load order item product")
		_, err = broken.OrderItemSelection().Group(ctx, &model.OrderItemSelection{GroupID: uuid.New()})
		require.ErrorContains(t, err, "failed to load selection group")
		_, err = broken.OrderItemSelection().Choice(ctx, &model.OrderItemSelection{ChoiceID: uuid.New()})
		require.ErrorContains(t, err, "failed to load selection choice")
		name, err := broken.Order().DisplayCustomerName(ctx, o)
		require.NoError(t, err)
		assert.Equal(t, "Guest", name)
		choice := uuid.New()
		_, err = broken.OrderItem().Choice(ctx, &model.OrderItem{ChoiceID: &choice})
		require.ErrorContains(t, err, "failed to load product choice")
	})

	t.Run("a selection that points at nothing is an error naming the id", func(t *testing.T) {
		ctx := env.ctxFor(customer.String(), false, "en")
		missing := uuid.New()
		_, err := env.Resolver.OrderItemSelection().Group(ctx, &model.OrderItemSelection{GroupID: missing})
		require.ErrorContains(t, err, "selection group "+missing.String()+" not found")
		_, err = env.Resolver.OrderItemSelection().Choice(ctx, &model.OrderItemSelection{ChoiceID: missing})
		require.ErrorContains(t, err, "selection choice "+missing.String()+" not found")
	})

	t.Run("an item without a legacy choice, or a product that is gone, resolves to null", func(t *testing.T) {
		ctx := env.ctxFor(customer.String(), false, "en")
		got, err := env.Resolver.OrderItem().Choice(ctx, &model.OrderItem{})
		require.NoError(t, err)
		assert.Nil(t, got)
		product, err := env.Resolver.OrderItem().Product(ctx, &model.OrderItem{ProductID: uuid.New()})
		require.NoError(t, err)
		assert.Nil(t, product)
	})

	t.Run("an address with a box number, a manual flag and the absence of both", func(t *testing.T) {
		ctx := env.ctxFor(customer.String(), false, "en")
		street, house, post, city, box := "Rue Neuve", "5", "4000", "Liege", "B2"
		lat, lng, dist := 50.6, 5.5, 1234.0
		manual := true
		o := &model.Order{StreetName: &street, HouseNumber: &house, Postcode: &post, MunicipalityName: &city,
			BoxNumber: &box, AddressLat: &lat, AddressLng: &lng, AddressDistance: &dist, IsManualAddr: &manual}
		addr, err := env.Resolver.Order().Address(ctx, o)
		require.NoError(t, err)
		require.NotNil(t, addr)
		assert.Equal(t, 1234.0, addr.Distance)
		assert.Equal(t, &lat, addr.Lat)
		display, err := env.Resolver.Order().DisplayAddress(ctx, o)
		require.NoError(t, err)
		assert.Equal(t, "Rue Neuve 5 B2, 4000 Liege", display)
		isManual, err := env.Resolver.Order().IsManualAddress(ctx, o)
		require.NoError(t, err)
		assert.True(t, isManual)

		isManual, err = env.Resolver.Order().IsManualAddress(ctx, &model.Order{})
		require.NoError(t, err)
		assert.False(t, isManual, "an order without the flag is not manual")
		none, err := env.Resolver.Order().Address(ctx, &model.Order{})
		require.NoError(t, err)
		assert.Nil(t, none)

		extra, err := env.Resolver.Order().OrderExtra(ctx, &model.Order{OrderExtra: []any{"x"}})
		require.NoError(t, err)
		assert.Equal(t, []any{"x"}, extra)
	})
}
