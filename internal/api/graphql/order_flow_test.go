package graphql_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	couponApplication "tsb-service/internal/modules/coupon/application"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// The customer ordering flow, end to end through the real resolvers and a real Postgres: every
// outcome of createOrder (stored order, charged amount, Mollie request) and every refusal with its
// apperr code and parameters, each compared with what quoteOrder says about the same basket.

// orderingShop is a menu and a set of delivery addresses to order from.
type orderingShop struct {
	tc   *TestContext
	stub *testhelpers.MollieStub
	// salmon 12.50 and tea 3.50 come from the shared fixtures (salmon discountable, tea not).
	salmon, tea uuid.UUID
	feast       uuid.UUID // 30.00, discountable: clears the delivery minimum on its own
	side        uuid.UUID // 7.50, discountable
	sideB       uuid.UUID // 7.49, discountable
	soldOut     uuid.UUID
	lunchOnly   uuid.UUID
}

func setupShop(t *testing.T) *orderingShop {
	t.Helper()
	tc, stub := setupOrderingEnv(t)
	db := tc.DB.DB
	full := func(label string) translations { return shape(label, "fr", "en", "nl", "zh") }
	cat := testhelpers.SeedCategory(t, db, 50, full("Menu"))
	seed := func(code, price string, mutate func(*testhelpers.ProductSpec)) uuid.UUID {
		spec := testhelpers.ProductSpec{CategoryID: cat, Code: code, Price: price, Names: full(code)}
		if mutate != nil {
			mutate(&spec)
		}
		return testhelpers.SeedProduct(t, db, spec)
	}
	shop := &orderingShop{
		tc: tc, stub: stub,
		salmon: tc.Fixtures.SalmonSushi.ID, tea: tc.Fixtures.GreenTea.ID,
		feast:     seed("FEAST", "30.00", nil),
		side:      seed("SIDE", "7.50", nil),
		sideB:     seed("SIDEB", "7.49", nil),
		soldOut:   seed("SOLD", "5.00", func(p *testhelpers.ProductSpec) { p.SoldOut = true }),
		lunchOnly: seed("LUNCH", "9.00", func(p *testhelpers.ProductSpec) { p.LunchOnly = true }),
	}
	testhelpers.SeedAddress(t, db, "addr-near", 2000, "4000")
	testhelpers.SeedAddress(t, db, "addr-excluded", 2000, "4610")
	testhelpers.SeedAddress(t, db, "addr-far", 12000, "4000")
	return shop
}

func (s *orderingShop) customer(t *testing.T, label string) (uuid.UUID, string) {
	t.Helper()
	return testhelpers.SeedCustomer(t, s.tc.DB.DB, label)
}

// address makes sure a place id at the given distance (meters) is cached and returns it.
func (s *orderingShop) address(t *testing.T, meters int) string {
	t.Helper()
	placeID := fmt.Sprintf("addr-%d", meters)
	if countRows(t, s.tc, `SELECT count(*) FROM address_cache WHERE place_id = $1`, placeID) == 0 {
		testhelpers.SeedAddress(t, s.tc.DB.DB, placeID, meters, "4000")
	}
	return placeID
}

func (s *orderingShop) createCoupon(t *testing.T, input map[string]any) {
	t.Helper()
	input["isActive"] = true
	if _, ok := input["isActive"]; ok && input["inactive"] != nil {
		input["isActive"] = false
		delete(input, "inactive")
	}
	resp := gqlAs(t, s.tc, adminToken(t, s.tc), "fr",
		`mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id } }`, map[string]any{"input": input})
	require.Empty(t, resp.Errors, "coupon setup: %+v", resp.Errors)
}

func (s *orderingShop) couponUsedCount(t *testing.T, code string) int {
	t.Helper()
	return countRows(t, s.tc, `SELECT used_count FROM coupons WHERE code = $1`, code)
}

func (s *orderingShop) orderCount(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	return countRows(t, s.tc, `SELECT count(*) FROM orders WHERE user_id = $1`, userID)
}

func TestOrderingFlowSuccess(t *testing.T) {
	shop := setupShop(t)
	tc := shop.tc

	type want struct {
		total, takeaway, coupon, fee, txFee string
	}
	cases := []struct {
		name      string
		orderType string
		items     []map[string]any
		extra     func() map[string]any
		want      want
	}{
		{"pickup cash: 10 percent off the discountable goods", "PICKUP", lines(shop.salmon, 2), nil,
			want{total: "22.50", takeaway: "2.50", coupon: "0.00", txFee: "0.00"}},
		{"pickup below 20 EUR gets no discount", "PICKUP", lines(shop.salmon, 1), nil,
			want{total: "12.50", takeaway: "0.00", coupon: "0.00", txFee: "0.00"}},
		{"pickup of goods that are not discountable", "PICKUP", lines(shop.tea, 7), nil,
			want{total: "24.50", takeaway: "0.00", coupon: "0.00", txFee: "0.00"}},
		{"pickup at exactly 20 EUR is discounted", "PICKUP", lines(shop.salmon, 1, shop.side, 1), nil,
			want{total: "18.00", takeaway: "2.00", coupon: "0.00", txFee: "0.00"}},
		{"pickup one cent under 20 EUR is not discounted (the total is snapped to 10 cents)", "PICKUP", lines(shop.salmon, 1, shop.sideB, 1), nil,
			want{total: "20.00", takeaway: "0.00", coupon: "0.00", txFee: "0.00"}},
		{"a line at the maximum quantity", "PICKUP", lines(shop.tea, 99), nil,
			want{total: "346.50", takeaway: "0.00", coupon: "0.00", txFee: "0.00"}},
		{"the same product on two lines", "PICKUP", lines(shop.salmon, 1, shop.salmon, 1), nil,
			want{total: "22.50", takeaway: "2.50", coupon: "0.00", txFee: "0.00"}},
		{"pickup online adds the transaction fee after the discount", "PICKUP", lines(shop.salmon, 2),
			func() map[string]any { return map[string]any{"isOnlinePayment": true} },
			want{total: "22.80", takeaway: "2.50", coupon: "0.00", txFee: "0.30"}},
		{"delivery in the free tier", "DELIVERY", lines(shop.feast, 1),
			func() map[string]any { return map[string]any{"addressPlaceId": "addr-near"} },
			want{total: "30.00", takeaway: "0.00", coupon: "0.00", fee: "0.00", txFee: "0.00"}},
		{"delivery in a paid tier", "DELIVERY", lines(shop.feast, 1),
			func() map[string]any { return map[string]any{"addressPlaceId": shop.address(t, 4500)} },
			want{total: "32.00", takeaway: "0.00", coupon: "0.00", fee: "2.00", txFee: "0.00"}},
		{"delivery at the edge of the zone", "DELIVERY", lines(shop.feast, 1),
			func() map[string]any { return map[string]any{"addressPlaceId": shop.address(t, 8999)} },
			want{total: "36.00", takeaway: "0.00", coupon: "0.00", fee: "6.00", txFee: "0.00"}},
		{"delivery online", "DELIVERY", lines(shop.feast, 1),
			func() map[string]any {
				return map[string]any{"addressPlaceId": shop.address(t, 5500), "isOnlinePayment": true}
			},
			want{total: "33.30", takeaway: "0.00", coupon: "0.00", fee: "3.00", txFee: "0.30"}},
		{"delivery at exactly the 25 EUR minimum", "DELIVERY", lines(shop.salmon, 2),
			func() map[string]any { return map[string]any{"addressPlaceId": "addr-near"} },
			want{total: "25.00", takeaway: "0.00", coupon: "0.00", fee: "0.00", txFee: "0.00"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, token := shop.customer(t, "success")
			extra := map[string]any{}
			if c.extra != nil {
				extra = c.extra()
			}
			input := createOrderInput(c.orderType, c.items, extra)

			quote := quoteAs(t, tc, token, "fr", quoteInput(c.orderType, c.items, extra))
			require.Empty(t, quoteCodes(quote), "the quote of a valid basket has no issue")

			mollieBefore := len(shop.stub.Requests())
			order := mustCreateOrder(t, tc, token, "fr", input)
			stored := loadStoredOrder(t, tc, order.ID)

			assert.Equal(t, c.want.total, stored.TotalPrice)
			assert.Equal(t, c.want.takeaway, stored.TakeawayDiscount)
			assert.Equal(t, c.want.coupon, stored.CouponDiscount)
			assert.Equal(t, c.want.txFee, stored.TransactionFee)
			if c.want.fee != "" {
				require.NotNil(t, stored.DeliveryFee)
				assert.Equal(t, c.want.fee, *stored.DeliveryFee)
			}
			assert.Equal(t, "PENDING", stored.OrderStatus)
			assert.Equal(t, c.orderType, stored.OrderType)
			assert.True(t, decimal.RequireFromString(order.TotalPrice).Equal(decimal.RequireFromString(stored.TotalPrice)), "the response total %s is the stored total %s", order.TotalPrice, stored.TotalPrice)
			assert.Equal(t, len(c.items), countRows(t, tc, `SELECT count(*) FROM order_product WHERE order_id = $1`, order.ID))

			// Quote and createOrder are the same maths.
			assert.Equal(t, quote.Total, stored.TotalPrice, "quote total")
			assert.Equal(t, quote.PickupDiscount, stored.TakeawayDiscount, "quote pickup discount")
			assert.Equal(t, quote.CouponDiscount, stored.CouponDiscount, "quote coupon discount")
			assert.Equal(t, quote.OnlineFee, stored.TransactionFee, "quote online fee")
			if stored.DeliveryFee != nil {
				assert.Equal(t, quote.DeliveryFee, *stored.DeliveryFee, "quote delivery fee")
			}

			online, _ := extra["isOnlinePayment"].(bool)
			sent := shop.stub.Requests()
			if !online {
				require.Nil(t, order.Payment, "a cash order has no payment")
				assert.Len(t, sent, mollieBefore, "a cash order never reaches Mollie")
				assert.False(t, stored.IsOnlinePayment)
				return
			}
			// Online: the order is saved, the payment created for the stored total, the checkout link returned.
			require.NotNil(t, order.Payment)
			assert.True(t, stored.IsOnlinePayment)
			assert.Contains(t, string(order.Payment.Links), "https://www.mollie.com/checkout/")
			require.Len(t, sent, mollieBefore+1)
			request := sent[mollieBefore]
			assert.Equal(t, stored.TotalPrice, request.Amount.Value)
			assert.Equal(t, "EUR", request.Amount.Currency)
			assert.Equal(t, "https://shop.example.test/order-completed/"+order.ID, request.RedirectURL)
			sum := decimal.Zero
			for _, l := range request.Lines {
				v, err := decimal.NewFromString(l.TotalAmount.Value)
				require.NoError(t, err)
				sum = sum.Add(v)
			}
			assert.Equal(t, request.Amount.Value, sum.StringFixed(2), "the Mollie lines add up to the amount")
		})
	}

	t.Run("notes, extras and the ready time are stored", func(t *testing.T) {
		_, token := shop.customer(t, "notes")
		ready := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
		input := createOrderInput("PICKUP", lines(shop.salmon, 1), map[string]any{
			"orderNote":          "no wasabi",
			"addressExtra":       "2nd floor",
			"orderExtra":         []map[string]any{{"name": "chopsticks", "options": []string{"2"}}},
			"preferredReadyTime": ready.Format(time.RFC3339),
		})
		order := mustCreateOrder(t, tc, token, "fr", input)
		var row struct {
			Note  *string `db:"order_note"`
			Extra *string `db:"address_extra"`
			Order *string `db:"order_extra"`
			Ready *string `db:"preferred_ready_time"`
		}
		require.NoError(t, tc.DB.DB.GetContext(t.Context(), &row,
			`SELECT order_note, address_extra, order_extra::text, preferred_ready_time::text FROM orders WHERE id = $1`, order.ID))
		require.NotNil(t, row.Note)
		assert.Equal(t, "no wasabi", *row.Note)
		require.NotNil(t, row.Extra)
		assert.Equal(t, "2nd floor", *row.Extra)
		require.NotNil(t, row.Order)
		assert.Contains(t, *row.Order, "chopsticks")
		assert.NotNil(t, row.Ready)
	})

	t.Run("the cash amount the customer pays with", func(t *testing.T) {
		_, token := shop.customer(t, "cash")
		items := lines(shop.salmon, 2)
		paid := func(extra map[string]any) *string {
			return loadStoredOrder(t, tc, mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", items, extra)).ID).CashPayment
		}

		kept := paid(map[string]any{"cashPaymentAmount": "50"})
		require.NotNil(t, kept)
		assert.Equal(t, "50.00", *kept)
		assert.Nil(t, paid(map[string]any{"cashPaymentAmount": "   "}), "a blank amount means no amount")
		assert.Nil(t, paid(nil))
		withDecimals := paid(map[string]any{"cashPaymentAmount": " 22.50 "})
		require.NotNil(t, withDecimals)
		assert.Equal(t, "22.50", *withDecimals)
		assert.Nil(t, paid(map[string]any{"cashPaymentAmount": "50", "isOnlinePayment": true}), "ignored for an online payment")

		for _, bad := range []string{"abc", "-5", "12,5", "1e", "NaN"} {
			got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", items, map[string]any{"cashPaymentAmount": bad}))
			requireOrderError(t, got, orderErr, apperr.CodeCashAmountInvalid)
		}
	})
}

// failCase is a basket createOrder refuses.
type failCase struct {
	name  string
	build func(s *orderingShop, t *testing.T) (orderType string, items []map[string]any, extra map[string]any)
	code  apperr.Code
	// params are extensions the error must carry besides its code.
	params map[string]any
	// reported marks codes that are ours to fix, not the customer's: they reach Sentry.
	reported bool
	// createOnly marks refusals quoteOrder has no counterpart for.
	createOnly bool
}

func TestOrderingFlowRefusals(t *testing.T) {
	shop := setupShop(t)
	tc := shop.tc
	missing := uuid.New()

	cases := []failCase{
		{name: "empty basket", code: apperr.CodeOrderEmpty,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", []map[string]any{}, nil
			}},
		{name: "more than 50 lines", code: apperr.CodeOrderTooManyItems,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				var items []map[string]any
				for range 51 {
					items = append(items, map[string]any{"productId": s.tea.String(), "quantity": 1})
				}
				return "PICKUP", items, nil
			}},
		{name: "quantity zero", code: apperr.CodeInvalidQuantity,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.tea, 0), nil
			}},
		{name: "negative quantity", code: apperr.CodeInvalidQuantity,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.tea, -3), nil
			}},
		{name: "quantity over 99", code: apperr.CodeInvalidQuantity, params: map[string]any{"productId": "$tea"},
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.salmon, 1, s.tea, 100), nil
			}},
		{name: "unknown product carries its id", code: apperr.CodeProductNotFound, params: map[string]any{"productId": missing.String()},
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.salmon, 1, missing, 1), nil
			}},
		{name: "sold-out product carries its id", code: apperr.CodeProductUnavailable, params: map[string]any{"productId": "$soldOut"},
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.salmon, 1, s.soldOut, 1), nil
			}},
		{name: "delivery below the minimum", code: apperr.CodeDeliveryMinimumNotMet, params: map[string]any{"minimum": "25"},
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.tea, 1), map[string]any{"addressPlaceId": "addr-near"}
			}},
		{name: "delivery minimum counts goods, not the fee", code: apperr.CodeDeliveryMinimumNotMet, params: map[string]any{"minimum": "25"},
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.side, 3), map[string]any{"addressPlaceId": s.address(t, 8000)} // 22.50 + 5 fee
			}},
		{name: "delivery without an address", code: apperr.CodeAddressRequired,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), nil
			}},
		{name: "delivery with a blank address", code: apperr.CodeAddressRequired,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), map[string]any{"addressPlaceId": ""}
			}},
		{name: "delivery to a place that cannot be resolved", code: apperr.CodeAddressUnresolvable, reported: true,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), map[string]any{"addressPlaceId": "never-heard-of-it"}
			}},
		{name: "delivery out of the zone", code: apperr.CodeDeliveryOutOfZone,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), map[string]any{"addressPlaceId": "addr-far"}
			}},
		{name: "delivery at exactly the radius is out", code: apperr.CodeDeliveryOutOfZone,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), map[string]any{"addressPlaceId": s.address(t, 9000)}
			}},
		{name: "delivery to an excluded postcode", code: apperr.CodeDeliveryAreaExcluded,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "DELIVERY", lines(s.feast, 1), map[string]any{"addressPlaceId": "addr-excluded"}
			}},
		{name: "unknown coupon", code: apperr.CodeCouponInvalid,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.salmon, 2), map[string]any{"couponCode": "NO-SUCH-CODE"}
			}},
		{name: "cash amount that is not a number", code: apperr.CodeCashAmountInvalid, createOnly: true,
			build: func(s *orderingShop, t *testing.T) (string, []map[string]any, map[string]any) {
				return "PICKUP", lines(s.salmon, 1), map[string]any{"cashPaymentAmount": "lots"}
			}},
	}

	resolveParam := func(v any) any {
		switch v {
		case "$tea":
			return shop.tea.String()
		case "$soldOut":
			return shop.soldOut.String()
		}
		return v
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			userID, token := shop.customer(t, "refusal")
			orderType, items, extra := c.build(shop, t)
			mollieBefore := len(shop.stub.Requests())

			got, orderErr := createOrderAs(t, tc, token, "nl", createOrderInput(orderType, items, extra))
			if c.reported {
				require.Nil(t, got)
				require.NotNil(t, orderErr)
				require.Equal(t, string(c.code), orderErr.Code())
				assert.False(t, apperr.IsExpected(c.code), "%s is reported", c.code)
			} else {
				requireOrderError(t, got, orderErr, c.code)
			}
			for key, value := range c.params {
				assert.Equal(t, resolveParam(value), orderErr.Extensions[key], "extensions.%s of %s", key, c.code)
			}
			assert.Zero(t, shop.orderCount(t, userID), "a refused order leaves nothing behind")
			assert.Len(t, shop.stub.Requests(), mollieBefore, "a refused order never reaches Mollie")

			if c.createOnly {
				return
			}
			// quoteOrder reports the same problem instead of failing.
			quote := quoteAs(t, tc, token, "nl", quoteInput(orderType, items, extra))
			assert.Contains(t, quoteCodes(quote), string(c.code), "the quote must show what createOrder refuses")
		})
	}

	t.Run("an anonymous caller cannot create an order", func(t *testing.T) {
		got, orderErr := createOrderAs(t, tc, "", "fr", createOrderInput("PICKUP", lines(shop.salmon, 1), nil))
		requireOrderError(t, got, orderErr, apperr.CodeUnauthenticated)
		assert.Zero(t, countRows(t, tc, `SELECT count(*) FROM orders`))
	})

	t.Run("an expired token cannot create an order", func(t *testing.T) {
		expired, err := testhelpers.GenerateExpiredToken(tc.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)
		got, orderErr := createOrderAs(t, tc, expired, "fr", createOrderInput("PICKUP", lines(shop.salmon, 1), nil))
		requireOrderError(t, got, orderErr, apperr.CodeUnauthenticated)
	})

	t.Run("a product deleted between the quote and the order is PRODUCT_NOT_FOUND", func(t *testing.T) {
		_, token := shop.customer(t, "deleted")
		cat := testhelpers.SeedCategory(t, tc.DB.DB, 70, shape("Gone", "fr", "en"))
		gone := testhelpers.SeedProduct(t, tc.DB.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "GONE", Price: "5.00", Names: shape("Gone", "fr", "en")})
		items := lines(shop.salmon, 1, gone, 1)

		assert.Empty(t, quoteCodes(quoteAs(t, tc, token, "nl", quoteInput("PICKUP", items, nil))))
		_, err := tc.DB.DB.ExecContext(t.Context(), `DELETE FROM products WHERE id = $1`, gone)
		require.NoError(t, err)

		got, orderErr := createOrderAs(t, tc, token, "nl", createOrderInput("PICKUP", items, nil))
		e := requireOrderError(t, got, orderErr, apperr.CodeProductNotFound)
		assert.Equal(t, gone.String(), e.Extensions["productId"])
		assert.Contains(t, quoteCodes(quoteAs(t, tc, token, "nl", quoteInput("PICKUP", items, nil))), "PRODUCT_NOT_FOUND")
	})

	t.Run("a price that changed after the quote is charged at today's price", func(t *testing.T) {
		_, token := shop.customer(t, "price")
		cat := testhelpers.SeedCategory(t, tc.DB.DB, 71, shape("Moving", "fr", "en"))
		moving := testhelpers.SeedProduct(t, tc.DB.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "MOVING", Price: "10.00", Names: shape("Moving", "fr", "en"), NotDiscountable: true})
		items := []map[string]any{{"productId": moving.String(), "quantity": 2, "expectedLineTotal": "20.00"}}

		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET price = 11.00 WHERE id = $1`, moving)
		require.NoError(t, err)

		quote := quoteAs(t, tc, token, "fr", quoteInput("PICKUP", items, nil))
		assert.Equal(t, []string{"PRICE_CHANGED"}, quoteCodes(quote), "the client is told its price is stale")
		assert.Equal(t, "22.00", quote.Total)

		// createOrder has no expected total: it charges what the quote shows.
		order := mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", lines(moving, 2), nil))
		assert.Equal(t, quote.Total, loadStoredOrder(t, tc, order.ID).TotalPrice)
	})

	t.Run("a failed order does not leave a second one behind when the customer retries", func(t *testing.T) {
		userID, token := shop.customer(t, "retry")
		got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", lines(shop.salmon, 1, shop.soldOut, 1), nil))
		requireOrderError(t, got, orderErr, apperr.CodeProductUnavailable)
		mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", lines(shop.salmon, 1), nil))
		assert.Equal(t, 1, shop.orderCount(t, userID))
	})
}

func TestOrderingDeliveryFeeTiers(t *testing.T) {
	shop := setupShop(t)
	tc := shop.tc
	_, token := shop.customer(t, "tiers")

	// The grid the webshop displays: free under 3 km, then one euro more per kilometre up to 9 km.
	tiers := []struct {
		meters int
		fee    string // "" = out of the zone
	}{
		{0, "0.00"}, {2999, "0.00"}, {3000, "1.00"}, {3999, "1.00"}, {4000, "2.00"}, {4999, "2.00"},
		{5000, "3.00"}, {5999, "3.00"}, {6000, "4.00"}, {6999, "4.00"}, {7000, "5.00"}, {7999, "5.00"},
		{8000, "6.00"}, {8999, "6.00"}, {9000, ""}, {9001, ""}, {25000, ""},
	}
	for _, tier := range tiers {
		t.Run(fmt.Sprintf("%dm", tier.meters), func(t *testing.T) {
			extra := map[string]any{"addressPlaceId": shop.address(t, tier.meters)}
			quote := quoteAs(t, tc, token, "fr", quoteInput("DELIVERY", lines(shop.feast, 1), extra))
			got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("DELIVERY", lines(shop.feast, 1), extra))
			if tier.fee == "" {
				requireOrderError(t, got, orderErr, apperr.CodeDeliveryOutOfZone)
				assert.Contains(t, quoteCodes(quote), "DELIVERY_OUT_OF_ZONE")
				return
			}
			require.Nil(t, orderErr, "%+v", orderErr)
			assert.Equal(t, tier.fee, quote.DeliveryFee)
			stored := loadStoredOrder(t, tc, got.ID)
			require.NotNil(t, stored.DeliveryFee)
			assert.Equal(t, tier.fee, *stored.DeliveryFee)
			assert.Equal(t, quote.Total, stored.TotalPrice)
		})
	}
}

// choice fixtures: a bowl with a required broth group and an optional toppings group.
type bowl struct {
	product                   uuid.UUID
	broth, toppings           uuid.UUID
	mild, spicy               uuid.UUID // broth choices, spicy +1.00
	nori, egg, corn, sesame   uuid.UUID // toppings, +0.50 each, sesame +0.00
	otherProduct, otherChoice uuid.UUID
}

func seedBowl(t *testing.T, shop *orderingShop) bowl {
	t.Helper()
	db := shop.tc.DB.DB
	cat := testhelpers.SeedCategory(t, db, 60, shape("Bowls", "fr", "en"))
	b := bowl{}
	b.product = testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: cat, Code: "BOWL", Price: "10.00", Names: shape("Bowl", "fr", "en"), NotDiscountable: true})
	b.broth = testhelpers.SeedChoiceGroup(t, db, b.product, 1, 1, translations{"fr": "Bouillon", "en": "Broth"})
	b.toppings = testhelpers.SeedChoiceGroup(t, db, b.product, 0, 3, translations{"en": "Toppings"})
	b.mild = testhelpers.SeedChoice(t, db, b.product, b.broth, "0.00", translations{"fr": "Doux", "en": "Mild"})
	b.spicy = testhelpers.SeedChoice(t, db, b.product, b.broth, "1.00", translations{"en": "Spicy"})
	b.nori = testhelpers.SeedChoice(t, db, b.product, b.toppings, "0.50", translations{"en": "Nori"})
	b.egg = testhelpers.SeedChoice(t, db, b.product, b.toppings, "0.50", translations{"en": "Egg"})
	b.corn = testhelpers.SeedChoice(t, db, b.product, b.toppings, "0.50", translations{"en": "Corn"})
	b.sesame = testhelpers.SeedChoice(t, db, b.product, b.toppings, "0.00", translations{"en": "Sesame"})

	b.otherProduct = testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: cat, Code: "OTHER", Price: "6.00", Names: shape("Other", "fr", "en")})
	otherGroup := testhelpers.SeedChoiceGroup(t, db, b.otherProduct, 0, 1, translations{"en": "Side"})
	b.otherChoice = testhelpers.SeedChoice(t, db, b.otherProduct, otherGroup, "0.50", translations{"en": "Rice"})
	return b
}

func sel(group, choice uuid.UUID, quantity int) map[string]any {
	return map[string]any{"groupId": group.String(), "choiceId": choice.String(), "quantity": quantity}
}

func TestOrderingChoicesAndGroups(t *testing.T) {
	shop := setupShop(t)
	tc := shop.tc
	b := seedBowl(t, shop)
	_, token := shop.customer(t, "choices")

	item := func(qty int, selections ...map[string]any) []map[string]any {
		return []map[string]any{{"productId": b.product.String(), "quantity": qty, "selections": selections}}
	}

	accepted := []struct {
		name  string
		items []map[string]any
		total string
	}{
		{"one required choice", item(1, sel(b.broth, b.spicy, 1)), "11.00"},
		{"required choice plus toppings", item(1, sel(b.broth, b.mild, 1), sel(b.toppings, b.nori, 1), sel(b.toppings, b.egg, 1)), "11.00"},
		{"toppings up to the group maximum", item(1, sel(b.broth, b.mild, 1), sel(b.toppings, b.nori, 1), sel(b.toppings, b.egg, 1), sel(b.toppings, b.corn, 1)), "11.50"},
		{"a free topping is still a selection", item(1, sel(b.broth, b.mild, 1), sel(b.toppings, b.sesame, 3)), "10.00"},
		{"the group limits scale with the quantity", item(2, sel(b.broth, b.mild, 2)), "20.00"},
		{"quantity two with two different broths", item(2, sel(b.broth, b.mild, 1), sel(b.broth, b.spicy, 1)), "21.00"},
		{"the legacy single choice applies to every unit", []map[string]any{{"productId": b.product.String(), "quantity": 2, "choiceId": b.spicy.String()}}, "22.00"},
	}
	for _, c := range accepted {
		t.Run("accepted: "+c.name, func(t *testing.T) {
			quote := quoteAs(t, tc, token, "nl", quoteInput("PICKUP", c.items, nil))
			require.Empty(t, quoteCodes(quote))
			assert.Equal(t, c.total, quote.Total)
			order := mustCreateOrder(t, tc, token, "nl", createOrderInput("PICKUP", c.items, nil))
			assert.Equal(t, c.total, loadStoredOrder(t, tc, order.ID).TotalPrice)
			assert.NotEmpty(t, countRows(t, tc, `SELECT count(*) FROM order_product_choices opc JOIN order_product op ON op.id = opc.order_product_id WHERE op.order_id = $1`, order.ID))
		})
	}

	deleted := uuid.New()
	refused := []struct {
		name  string
		items []map[string]any
	}{
		{"no choice of a required group", item(1)},
		{"only toppings, the required broth is missing", item(1, sel(b.toppings, b.nori, 1))},
		{"two choices in a group that allows one", item(1, sel(b.broth, b.mild, 1), sel(b.broth, b.spicy, 1))},
		{"more toppings than the maximum", item(1, sel(b.broth, b.mild, 1), sel(b.toppings, b.nori, 1), sel(b.toppings, b.egg, 1), sel(b.toppings, b.corn, 1), sel(b.toppings, b.sesame, 1))},
		{"a selection quantity over the maximum", item(1, sel(b.broth, b.mild, 1), sel(b.toppings, b.nori, 4))},
		{"quantity two with a single broth", item(2, sel(b.broth, b.mild, 1))},
		{"selection quantity zero", item(1, sel(b.broth, b.mild, 0))},
		{"negative selection quantity", item(1, sel(b.broth, b.mild, -1))},
		{"a choice under the wrong group", item(1, sel(b.broth, b.nori, 1))},
		{"a choice of another product", item(1, sel(b.broth, b.otherChoice, 1))},
		{"a choice that was deleted", item(1, sel(b.broth, deleted, 1))},
		{"a legacy choice that was deleted", []map[string]any{{"productId": b.product.String(), "quantity": 1, "choiceId": deleted.String()}}},
	}
	for _, c := range refused {
		t.Run("refused: "+c.name, func(t *testing.T) {
			got, orderErr := createOrderAs(t, tc, token, "nl", createOrderInput("PICKUP", c.items, nil))
			e := requireOrderError(t, got, orderErr, apperr.CodeSelectionInvalid)
			assert.Equal(t, b.product.String(), e.Extensions["productId"])
			assert.NotContains(t, strings.ToLower(e.Message), "sql", "no driver text for the customer")

			quote := quoteAs(t, tc, token, "nl", quoteInput("PICKUP", c.items, nil))
			assert.Contains(t, quoteCodes(quote), "SELECTION_INVALID")
		})
	}

	t.Run("selections read back with names in the reader's language", func(t *testing.T) {
		items := item(1, sel(b.broth, b.spicy, 1), sel(b.toppings, b.nori, 2))
		order := mustCreateOrder(t, tc, token, "nl", createOrderInput("PICKUP", items, nil))
		const query = `query ($id: ID!) { myOrder(id: $id) { items { selections { quantity group { name } choice { name } } } } }`
		for _, lang := range []string{"fr", "en", "nl", "zh", ""} {
			resp := gqlAs(t, tc, token, lang, query, map[string]any{"id": order.ID})
			require.Empty(t, resp.Errors, "[%s] %+v", lang, resp.Errors)
			var data struct {
				MyOrder struct {
					Items []struct {
						Selections []struct {
							Quantity int
							Group    struct{ Name string }
							Choice   struct{ Name string }
						}
					}
				}
			}
			require.NoError(t, json.Unmarshal(resp.Data, &data))
			require.Len(t, data.MyOrder.Items, 1)
			got := map[string]string{}
			for _, s := range data.MyOrder.Items[0].Selections {
				got[s.Choice.Name] = s.Group.Name
				assert.NotEmpty(t, s.Group.Name, "[%s] a selection never loses its group name", lang)
			}
			assert.Contains(t, got, "Spicy", "[%s] Spicy only exists in en: every language falls back to it", lang)
			assert.Contains(t, got, "Nori", "[%s]", lang)
		}
	})
}

func TestOrderingCoupons(t *testing.T) {
	shop := setupShop(t)
	tc := shop.tc
	salmon2 := lines(shop.salmon, 2) // 25.00, discountable: pickup discount 2.50

	past, future := time.Now().Add(-48*time.Hour).Format(time.RFC3339), time.Now().Add(48*time.Hour).Format(time.RFC3339)
	shop.createCoupon(t, map[string]any{"code": "PCT10", "discountType": "PERCENTAGE", "discountValue": "10"})
	shop.createCoupon(t, map[string]any{"code": "FIX5", "discountType": "FIXED", "discountValue": "5", "minOrderAmount": "30"})
	shop.createCoupon(t, map[string]any{"code": "EXPIRED", "discountType": "FIXED", "discountValue": "1", "validUntil": past})
	shop.createCoupon(t, map[string]any{"code": "LATER", "discountType": "FIXED", "discountValue": "1", "validFrom": future})
	shop.createCoupon(t, map[string]any{"code": "OFF", "discountType": "FIXED", "discountValue": "1", "inactive": true})
	shop.createCoupon(t, map[string]any{"code": "ONCE", "discountType": "FIXED", "discountValue": "2", "maxUses": 1})
	shop.createCoupon(t, map[string]any{"code": "PERUSER", "discountType": "FIXED", "discountValue": "2", "maxUsesPerUser": 1})

	withCoupon := func(code string) map[string]any { return map[string]any{"couponCode": code} }

	t.Run("a valid coupon stacks with the pickup discount and is counted", func(t *testing.T) {
		_, token := shop.customer(t, "coupon")
		quote := quoteAs(t, tc, token, "fr", quoteInput("PICKUP", salmon2, withCoupon("PCT10")))
		require.Empty(t, quoteCodes(quote))
		order := mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		stored := loadStoredOrder(t, tc, order.ID)
		assert.Equal(t, "20.00", stored.TotalPrice, "25.00 - 2.50 pickup - 2.50 coupon")
		assert.Equal(t, "2.50", stored.TakeawayDiscount)
		assert.Equal(t, "2.50", stored.CouponDiscount)
		assert.Equal(t, quote.Total, stored.TotalPrice)
		require.NotNil(t, stored.CouponCode)
		assert.Equal(t, "PCT10", *stored.CouponCode)
		assert.Equal(t, 1, shop.couponUsedCount(t, "PCT10"))
		require.NotNil(t, order.CouponCode)
	})

	refusals := []struct {
		name  string
		code  string
		apper apperr.Code
		ext   map[string]any
		items []map[string]any
	}{
		{"unknown", "NOPE", apperr.CodeCouponInvalid, nil, salmon2},
		{"expired", "EXPIRED", apperr.CodeCouponInvalid, nil, salmon2},
		{"not valid yet", "LATER", apperr.CodeCouponInvalid, nil, salmon2},
		{"switched off", "OFF", apperr.CodeCouponInvalid, nil, salmon2},
		{"below its minimum order", "FIX5", apperr.CodeCouponMinOrderNotMet, map[string]any{"minimum": "30"}, salmon2},
	}
	for _, c := range refusals {
		t.Run("refused: "+c.name, func(t *testing.T) {
			userID, token := shop.customer(t, "coupon")
			got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", c.items, withCoupon(c.code)))
			e := requireOrderError(t, got, orderErr, c.apper)
			for key, value := range c.ext {
				assert.Equal(t, value, e.Extensions[key])
			}
			assert.Zero(t, shop.orderCount(t, userID))
			assert.Contains(t, quoteCodes(quoteAs(t, tc, token, "fr", quoteInput("PICKUP", c.items, withCoupon(c.code)))), string(c.apper))
		})
	}

	t.Run("a coupon at its minimum is accepted", func(t *testing.T) {
		_, token := shop.customer(t, "coupon")
		items := lines(shop.feast, 1) // 30.00 delivered free
		extra := map[string]any{"couponCode": "FIX5", "addressPlaceId": "addr-near"}
		order := mustCreateOrder(t, tc, token, "fr", createOrderInput("DELIVERY", items, extra))
		stored := loadStoredOrder(t, tc, order.ID)
		assert.Equal(t, "25.00", stored.TotalPrice)
		assert.Equal(t, "5.00", stored.CouponDiscount)
	})

	t.Run("a coupon with a use limit is gone once used", func(t *testing.T) {
		_, first := shop.customer(t, "first")
		_, second := shop.customer(t, "second")
		mustCreateOrder(t, tc, first, "fr", createOrderInput("PICKUP", salmon2, withCoupon("ONCE")))
		got, orderErr := createOrderAs(t, tc, second, "fr", createOrderInput("PICKUP", salmon2, withCoupon("ONCE")))
		requireOrderError(t, got, orderErr, apperr.CodeCouponInvalid)
		assert.Equal(t, 1, shop.couponUsedCount(t, "ONCE"))
	})

	t.Run("a per-user limit applies to that user only", func(t *testing.T) {
		firstID, first := shop.customer(t, "first")
		_, second := shop.customer(t, "second")
		order := mustCreateOrder(t, tc, first, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PERUSER")))
		// The first order is still open (one coupon at a time): finish it, so only the limit speaks.
		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE orders SET order_status = 'PICKED_UP' WHERE id = $1`, order.ID)
		require.NoError(t, err)
		got, orderErr := createOrderAs(t, tc, first, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PERUSER")))
		requireOrderError(t, got, orderErr, apperr.CodeCouponInvalid)
		assert.Equal(t, 1, shop.orderCount(t, firstID))
		mustCreateOrder(t, tc, second, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PERUSER")))
	})

	t.Run("one coupon at a time, until the order is closed", func(t *testing.T) {
		userID, token := shop.customer(t, "active")
		first := mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))

		got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		requireOrderError(t, got, orderErr, apperr.CodeCouponAlreadyActive)
		quote := quoteAs(t, tc, token, "fr", quoteInput("PICKUP", salmon2, withCoupon("PCT10")))
		assert.Contains(t, quoteCodes(quote), "COUPON_ALREADY_ACTIVE")
		// An order without a coupon is not affected.
		mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, nil))

		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE orders SET order_status = 'CANCELLED' WHERE id = $1`, first.ID)
		require.NoError(t, err)
		mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		assert.Equal(t, 3, shop.orderCount(t, userID))
	})

	t.Run("too many wrong codes lock coupons for the day", func(t *testing.T) {
		_, token := shop.customer(t, "locked")
		for i := range 5 {
			got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon(fmt.Sprintf("GUESS%d", i))))
			requireOrderError(t, got, orderErr, apperr.CodeCouponInvalid)
		}
		got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		requireOrderError(t, got, orderErr, apperr.CodeCouponRateLimited)
		// The customer can still order without a coupon.
		mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, nil))
	})

	t.Run("a coupon that is used up between the check and the reservation is COUPON_EXHAUSTED", func(t *testing.T) {
		raced := withResolver(t, tc, func(r *resolver.Resolver) {
			r.CouponService = &reservingCouponService{CouponService: r.CouponService, reserve: func() (bool, error) { return false, nil }}
		})
		userID, token := shop.customer(t, "race")
		got, orderErr := createOrderAs(t, raced, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		requireOrderError(t, got, orderErr, apperr.CodeCouponExhausted)
		assert.Zero(t, shop.orderCount(t, userID))
	})

	t.Run("a coupon that cannot be reserved is our fault and is reported", func(t *testing.T) {
		broken := withResolver(t, tc, func(r *resolver.Resolver) {
			r.CouponService = &reservingCouponService{CouponService: r.CouponService, reserve: func() (bool, error) { return false, errors.New("connection reset") }}
		})
		userID, token := shop.customer(t, "reserve")
		got, orderErr := createOrderAs(t, broken, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		require.Nil(t, got)
		require.NotNil(t, orderErr)
		assert.Equal(t, string(apperr.CodeCouponReserveFailed), orderErr.Code())
		assert.False(t, apperr.IsExpected(apperr.CodeCouponReserveFailed))
		assert.NotContains(t, orderErr.Message, "connection reset", "driver text is hidden from the customer")
		assert.Zero(t, shop.orderCount(t, userID))
	})

	t.Run("the coupon is given back when the order cannot be saved", func(t *testing.T) {
		broken := withResolver(t, tc, func(r *resolver.Resolver) {
			r.OrderService = &failingOrderService{OrderService: r.OrderService}
		})
		before := shop.couponUsedCount(t, "PCT10")
		userID, token := shop.customer(t, "save")
		got, orderErr := createOrderAs(t, broken, token, "fr", createOrderInput("PICKUP", salmon2, withCoupon("PCT10")))
		require.Nil(t, got)
		require.NotNil(t, orderErr)
		assert.Equal(t, string(apperr.CodeOrderCreateFailed), orderErr.Code())
		assert.False(t, apperr.IsExpected(apperr.CodeOrderCreateFailed))
		assert.Equal(t, before, shop.couponUsedCount(t, "PCT10"), "the reservation was rolled back")
		assert.Zero(t, shop.orderCount(t, userID))
	})

	t.Run("a refused online payment removes the order and gives the coupon back", func(t *testing.T) {
		before := shop.couponUsedCount(t, "PCT10")
		userID, token := shop.customer(t, "paymentfail")
		shop.stub.FailNext(1)
		got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, map[string]any{
			"couponCode": "PCT10", "isOnlinePayment": true,
		}))
		require.Nil(t, got)
		require.NotNil(t, orderErr)
		assert.Equal(t, string(apperr.CodePaymentFailed), orderErr.Code())
		assert.False(t, apperr.IsExpected(apperr.CodePaymentFailed))
		assert.Zero(t, shop.orderCount(t, userID), "no unpaid order is left behind")
		assert.Equal(t, before, shop.couponUsedCount(t, "PCT10"))

		// The customer can try again right away: the coupon is not stuck on a ghost order.
		mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", salmon2, map[string]any{"couponCode": "PCT10", "isOnlinePayment": true}))
	})
}

// withResolver serves the same database through a resolver tweaked by mutate.
func withResolver(t *testing.T, tc *TestContext, mutate func(*resolver.Resolver)) *TestContext {
	t.Helper()
	r := *tc.Resolver
	mutate(&r)
	client := testhelpers.NewGraphQLTestClient(&r, testhelpers.TestJWTSecret)
	t.Cleanup(client.Close)
	return &TestContext{DB: tc.DB, Resolver: &r, Client: client, Fixtures: tc.Fixtures}
}

// reservingCouponService decides the outcome of reserving a coupon, everything else is real.
type reservingCouponService struct {
	couponApplication.CouponService
	reserve func() (bool, error)
}

func (s *reservingCouponService) IncrementUsageAtomic(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return s.reserve()
}

// failingOrderService fails to save an order, everything else is real.
type failingOrderService struct {
	orderApplication.OrderService
}

func (s *failingOrderService) CreateOrder(context.Context, *orderDomain.Order, *[]orderDomain.OrderProductRaw) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	return nil, nil, errors.New("deadlock detected")
}

// The Payment mapper leaves molliePaymentId out although the schema makes it non-null.
func TestOrderPaymentExposesTheMolliePaymentID(t *testing.T) {
	shop := setupShop(t)
	_, token := shop.customer(t, "mollieid")
	order := mustCreateOrder(t, shop.tc, token, "fr", createOrderInput("PICKUP", lines(shop.salmon, 1), map[string]any{"isOnlinePayment": true}))
	require.NotNil(t, order.Payment)
	assert.NotEmpty(t, order.Payment.MolliePaymentID)
}
