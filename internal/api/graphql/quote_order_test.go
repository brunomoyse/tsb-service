package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/shared/middleware"
)

// quoteOrder prices a basket without saving it, with the same code createOrder prices with. These
// tests run it through the real HTTP handler and compare it with what createOrder stores.

const quoteOrderQuery = `
	query ($input: QuoteOrderInput!) {
		quoteOrder(input: $input) {
			subtotal deliveryFee pickupDiscount couponDiscount onlineFee total
			coupon { code valid errorCode }
			issues { code minimum }
			lines {
				productId quantity productPrice unitPrice lineTotal
				selections { groupId choiceId quantity priceModifier }
				issues { code currentPrice }
			}
		}
	}`

type quoteResult struct {
	Subtotal       string `json:"subtotal"`
	DeliveryFee    string `json:"deliveryFee"`
	PickupDiscount string `json:"pickupDiscount"`
	CouponDiscount string `json:"couponDiscount"`
	OnlineFee      string `json:"onlineFee"`
	Total          string `json:"total"`
	Coupon         *struct {
		Code      string  `json:"code"`
		Valid     bool    `json:"valid"`
		ErrorCode *string `json:"errorCode"`
	} `json:"coupon"`
	Issues []struct {
		Code    string  `json:"code"`
		Minimum *string `json:"minimum"`
	} `json:"issues"`
	Lines []struct {
		ProductID    string  `json:"productId"`
		Quantity     int     `json:"quantity"`
		ProductPrice *string `json:"productPrice"`
		UnitPrice    string  `json:"unitPrice"`
		LineTotal    string  `json:"lineTotal"`
		Issues       []struct {
			Code         string  `json:"code"`
			CurrentPrice *string `json:"currentPrice"`
		} `json:"issues"`
	} `json:"lines"`
}

func quoteOrder(t *testing.T, tc *TestContext, token string, input map[string]any) quoteResult {
	t.Helper()
	resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
		Query: quoteOrderQuery, Variables: map[string]any{"input": input},
	}, token)
	require.Empty(t, resp.Errors, "quoteOrder returned GraphQL errors: %+v", resp.Errors)
	var data struct {
		QuoteOrder quoteResult `json:"quoteOrder"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.QuoteOrder
}

// silenceOrderEmails turns off the "order received" e-mail of the test customer: a successful cash
// createOrder sends it from a goroutine, and the test environment has no mail client.
func silenceOrderEmails(t *testing.T, tc *TestContext) {
	t.Helper()
	_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE users SET notify_order_updates = false WHERE id = $1`, tc.Fixtures.RegularUser.ID)
	require.NoError(t, err)
}

func quoteInput(orderType string, items []map[string]any, extra map[string]any) map[string]any {
	input := map[string]any{"orderType": orderType, "isOnlinePayment": false, "items": items}
	for k, v := range extra {
		input[k] = v
	}
	return input
}

func TestQuoteOrder(t *testing.T) {
	tc := setupTestContext(t)
	customerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)
	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)

	silenceOrderEmails(t, tc)
	salmon := tc.Fixtures.SalmonSushi.ID.String() // 12.50, discountable
	tea := tc.Fixtures.GreenTea.ID.String()       // 3.50, not discountable

	// A coupon every test user can use: 10 % off.
	_, couponResp := postGraphQL(t, tc.Client.URL(), graphqlRequest{
		Query: `mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id } }`,
		Variables: map[string]any{"input": map[string]any{
			"code": "QUOTE10", "discountType": "PERCENTAGE", "discountValue": "10", "isActive": true,
		}},
	}, adminToken)
	require.Empty(t, couponResp.Errors, "coupon setup: %v", couponResp.Errors)

	basket := []map[string]any{{"productId": salmon, "quantity": 2}, {"productId": tea, "quantity": 1}}

	t.Run("is public: an anonymous caller gets the totals", func(t *testing.T) {
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", basket, nil))
		assert.Empty(t, q.Issues)
		assert.Equal(t, "28.50", q.Subtotal)
		assert.Equal(t, "2.50", q.PickupDiscount) // 10 % of the discountable 25.00
		assert.Equal(t, "0.00", q.DeliveryFee)
		assert.Equal(t, "0.00", q.OnlineFee)
		assert.Equal(t, "26.00", q.Total)
		require.Len(t, q.Lines, 2)
		assert.Equal(t, salmon, q.Lines[0].ProductID)
		assert.Equal(t, "12.50", *q.Lines[0].ProductPrice)
		assert.Equal(t, "25.00", q.Lines[0].LineTotal)
		assert.Equal(t, "12.50", q.Lines[0].UnitPrice)
		assert.Empty(t, q.Lines[0].Issues)
		assert.Nil(t, q.Coupon, "no coupon code sent")
	})

	t.Run("online payment adds the fee", func(t *testing.T) {
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", basket, map[string]any{"isOnlinePayment": true}))
		assert.Equal(t, "0.30", q.OnlineFee)
		assert.Equal(t, "26.30", q.Total)
	})

	t.Run("equals what createOrder stores", func(t *testing.T) {
		coupon := map[string]any{"couponCode": "QUOTE10"}
		q := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", basket, coupon))
		require.Empty(t, q.Issues)
		require.NotNil(t, q.Coupon)
		assert.True(t, q.Coupon.Valid)

		resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
			Query:     `mutation ($input: CreateOrderInput!) { createOrder(input: $input) { id totalPrice discountAmount } }`,
			Variables: map[string]any{"input": createOrderInput("PICKUP", basket, coupon)},
		}, customerToken)
		require.Empty(t, resp.Errors, "createOrder: %+v", resp.Errors)
		var data struct {
			CreateOrder struct {
				ID             string `json:"id"`
				TotalPrice     string `json:"totalPrice"`
				DiscountAmount string `json:"discountAmount"`
			} `json:"createOrder"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))

		var stored string
		require.NoError(t, tc.DB.DB.QueryRowxContext(t.Context(),
			`SELECT total_price::text FROM orders WHERE id = $1`, data.CreateOrder.ID).Scan(&stored))
		assert.Equal(t, q.Total, stored, "the quote total is the stored total_price")
		assert.JSONEq(t, q.Total, data.CreateOrder.TotalPrice)
		// Quoting did not reserve anything: the order we just created is the only coupon order, so a
		// second quote now reports it, exactly as createOrder would refuse it.
		again := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", basket, coupon))
		require.Len(t, again.Issues, 1)
		assert.Equal(t, "COUPON_ALREADY_ACTIVE", again.Issues[0].Code)
	})
}

func TestQuoteOrderIssues(t *testing.T) {
	tc := setupTestContext(t)
	customerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)
	salmon := tc.Fixtures.SalmonSushi.ID.String() // 12.50
	tea := tc.Fixtures.GreenTea.ID.String()

	t.Run("deleted and unknown products", func(t *testing.T) {
		missing := uuid.New().String()
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", []map[string]any{
			{"productId": salmon, "quantity": 1}, {"productId": missing, "quantity": 1},
		}, nil))
		require.Len(t, q.Lines, 2)
		assert.Empty(t, q.Lines[0].Issues)
		require.Len(t, q.Lines[1].Issues, 1)
		assert.Equal(t, "PRODUCT_NOT_FOUND", q.Lines[1].Issues[0].Code)
		assert.Nil(t, q.Lines[1].ProductPrice)
		assert.Equal(t, "12.50", q.Subtotal, "the missing line adds nothing")
	})

	t.Run("a sold-out product is flagged on its line, and createOrder agrees", func(t *testing.T) {
		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET is_available = false WHERE id = $1`, tea)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET is_available = true WHERE id = $1`, tea)
		})

		items := []map[string]any{{"productId": salmon, "quantity": 1}, {"productId": tea, "quantity": 1}}
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", items, nil))
		require.Len(t, q.Lines[1].Issues, 1)
		assert.Equal(t, "PRODUCT_UNAVAILABLE", q.Lines[1].Issues[0].Code)
		require.NotNil(t, q.Lines[1].Issues[0].CurrentPrice)
		assert.Equal(t, "3.50", *q.Lines[1].Issues[0].CurrentPrice)
		assert.Equal(t, "12.50", q.Subtotal)

		// createOrder used to fail this with an untyped "failed to retrieve products" error.
		resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
			Query: createOrderMutation, Variables: map[string]any{"input": createOrderInput("PICKUP", items, nil)},
		}, customerToken)
		require.NotEmpty(t, resp.Errors)
		assert.Equal(t, "PRODUCT_UNAVAILABLE", resp.Errors[0].Extensions["code"])
		assert.Equal(t, tea, resp.Errors[0].Extensions["productId"])
	})

	t.Run("price changed since the cart was saved", func(t *testing.T) {
		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET price = 13.00 WHERE id = $1`, salmon)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET price = 12.50 WHERE id = $1`, salmon)
		})

		q := quoteOrder(t, tc, "", quoteInput("PICKUP", []map[string]any{
			{"productId": salmon, "quantity": 2, "expectedLineTotal": "25.00"},
		}, nil))
		require.Len(t, q.Lines[0].Issues, 1)
		assert.Equal(t, "PRICE_CHANGED", q.Lines[0].Issues[0].Code)
		assert.Equal(t, "13.00", *q.Lines[0].Issues[0].CurrentPrice)
		assert.Equal(t, "13.00", *q.Lines[0].ProductPrice)
		assert.Equal(t, "26.00", q.Lines[0].LineTotal)
		assert.Equal(t, "26.00", q.Subtotal, "the quote is priced at today's price")

		// Once the client shows today's price there is nothing left to accept.
		fresh := quoteOrder(t, tc, "", quoteInput("PICKUP", []map[string]any{
			{"productId": salmon, "quantity": 2, "expectedLineTotal": "26.00"},
		}, nil))
		assert.Empty(t, fresh.Lines[0].Issues)
	})

	t.Run("delivery: minimum and missing address are reported together", func(t *testing.T) {
		q := quoteOrder(t, tc, "", quoteInput("DELIVERY", []map[string]any{{"productId": tea, "quantity": 1}}, nil))
		var got []string
		for _, issue := range q.Issues {
			got = append(got, issue.Code)
		}
		assert.Equal(t, []string{"DELIVERY_MINIMUM_NOT_MET", "ADDRESS_REQUIRED"}, got)
		require.NotNil(t, q.Issues[0].Minimum)
		assert.Equal(t, "25", *q.Issues[0].Minimum)
	})

	t.Run("empty basket", func(t *testing.T) {
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", []map[string]any{}, nil))
		require.Len(t, q.Issues, 1)
		assert.Equal(t, "ORDER_EMPTY", q.Issues[0].Code)
		assert.Equal(t, "0.00", q.Total)
	})

	t.Run("a malformed expectedLineTotal is rejected", func(t *testing.T) {
		resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
			Query: quoteOrderQuery,
			Variables: map[string]any{"input": quoteInput("PICKUP", []map[string]any{
				{"productId": salmon, "quantity": 1, "expectedLineTotal": "abc"},
			}, nil)},
		}, "")
		require.NotEmpty(t, resp.Errors)
		assert.Equal(t, "INVALID_AMOUNT", resp.Errors[0].Extensions["code"])
	})
}

func TestQuoteOrderCoupons(t *testing.T) {
	tc := setupTestContext(t)
	customerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)
	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	silenceOrderEmails(t, tc)
	salmon := tc.Fixtures.SalmonSushi.ID.String()

	createCoupon := func(code, discountType, value, minOrder string) {
		input := map[string]any{"code": code, "discountType": discountType, "discountValue": value, "isActive": true}
		if minOrder != "" {
			input["minOrderAmount"] = minOrder
		}
		_, resp := postGraphQL(t, tc.Client.URL(), graphqlRequest{
			Query:     `mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id } }`,
			Variables: map[string]any{"input": input},
		}, adminToken)
		require.Empty(t, resp.Errors, "coupon setup: %v", resp.Errors)
	}
	createCoupon("PCT10", "PERCENTAGE", "10", "")
	createCoupon("MIN40", "FIXED", "5", "40")
	createCoupon("FREE", "PERCENTAGE", "100", "")

	items := func(qty int) []map[string]any { return []map[string]any{{"productId": salmon, "quantity": qty}} }
	withCoupon := func(code string) map[string]any { return map[string]any{"couponCode": code} }

	t.Run("a percentage coupon follows the basket", func(t *testing.T) {
		small := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", items(1), withCoupon("PCT10")))
		big := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", items(4), withCoupon("PCT10")))
		assert.Equal(t, "1.30", small.CouponDiscount) // 10 % of 12.50 = 1.25, snapped to 1.30
		assert.Equal(t, "5.00", big.CouponDiscount)
		require.NotNil(t, big.Coupon)
		assert.True(t, big.Coupon.Valid)
		assert.Nil(t, big.Coupon.ErrorCode)
	})

	t.Run("anonymous: the coupon is reported as not evaluated", func(t *testing.T) {
		q := quoteOrder(t, tc, "", quoteInput("PICKUP", items(4), withCoupon("PCT10")))
		require.NotNil(t, q.Coupon)
		assert.False(t, q.Coupon.Valid)
		require.NotNil(t, q.Coupon.ErrorCode)
		assert.Equal(t, "UNAUTHENTICATED", *q.Coupon.ErrorCode)
		assert.Empty(t, q.Issues, "not an issue: the customer has to sign in to order anyway")
		assert.Equal(t, "0.00", q.CouponDiscount)
	})

	t.Run("unknown coupon and unmet minimum", func(t *testing.T) {
		unknown := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", items(4), withCoupon("NOPE-1")))
		require.Len(t, unknown.Issues, 1)
		assert.Equal(t, "COUPON_INVALID", unknown.Issues[0].Code)
		assert.False(t, unknown.Coupon.Valid)

		min := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", items(1), withCoupon("MIN40")))
		require.Len(t, min.Issues, 1)
		assert.Equal(t, "COUPON_MIN_ORDER_NOT_MET", min.Issues[0].Code)
		require.NotNil(t, min.Issues[0].Minimum)
		assert.Equal(t, "40", *min.Issues[0].Minimum)
	})

	t.Run("a coupon that covers the basket never stores a negative total", func(t *testing.T) {
		// 12.95 is not a multiple of 10 cents: the 100 % coupon is snapped up to 13.00, which used to
		// store total_price = -0.10 for a cash order.
		_, err := tc.DB.DB.ExecContext(t.Context(), `UPDATE products SET price = 12.95 WHERE id = $1`, salmon)
		require.NoError(t, err)

		q := quoteOrder(t, tc, customerToken, quoteInput("PICKUP", items(1), withCoupon("FREE")))
		assert.Equal(t, "13.00", q.CouponDiscount)
		assert.Equal(t, "0.00", q.Total)

		resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
			Query:     `mutation ($input: CreateOrderInput!) { createOrder(input: $input) { id } }`,
			Variables: map[string]any{"input": createOrderInput("PICKUP", items(1), withCoupon("FREE"))},
		}, customerToken)
		require.Empty(t, resp.Errors, "createOrder: %+v", resp.Errors)
		var data struct {
			CreateOrder struct {
				ID string `json:"id"`
			} `json:"createOrder"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		var stored string
		require.NoError(t, tc.DB.DB.QueryRowxContext(t.Context(),
			`SELECT total_price::text FROM orders WHERE id = $1`, data.CreateOrder.ID).Scan(&stored))
		assert.Equal(t, "0.00", stored)
	})
}

// quoteOrder and resolveAddress are public and (on a cache miss) cost Google calls, so they are
// throttled per client IP with a RATE_LIMITED code.
func TestPublicQueriesAreRateLimitedPerIP(t *testing.T) {
	tc := setupTestContext(t)
	limiter := middleware.NewRateLimiter(0.0001, 2) // burst of 2, no refill during the test
	t.Cleanup(limiter.Stop)
	tc.Resolver.PublicQueryLimiter = limiter

	salmon := tc.Fixtures.SalmonSushi.ID.String()
	run := func(query string, vars map[string]any) graphqlResponseWithExtensions {
		return postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{Query: query, Variables: vars}, "")
	}
	quote := func() graphqlResponseWithExtensions {
		return run(quoteOrderQuery, map[string]any{"input": quoteInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 1}}, nil)})
	}

	require.Empty(t, quote().Errors)
	require.Empty(t, quote().Errors)
	third := quote()
	require.Len(t, third.Errors, 1)
	assert.Equal(t, "RATE_LIMITED", third.Errors[0].Extensions["code"])
	assert.NotContains(t, third.Errors[0].Message, "Internal", "a rate limit is the caller's doing, not a server fault")

	// resolveAddress has its own bucket (empty cache + nil Google client here, so only the error
	// code matters: the first two calls must reach the service, the third must be refused first).
	const resolveQuery = `query ($p: String!, $s: String!) { resolveAddress(placeId: $p, sessionToken: $s) { id } }`
	vars := map[string]any{"p": "nope", "s": "tok"}
	for i := 0; i < 2; i++ {
		resp := run(resolveQuery, vars)
		for _, e := range resp.Errors {
			assert.NotEqual(t, "RATE_LIMITED", e.Extensions["code"], "call %d must not be limited", i+1)
		}
	}
	resp := run(resolveQuery, vars)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "RATE_LIMITED", resp.Errors[0].Extensions["code"])
}
