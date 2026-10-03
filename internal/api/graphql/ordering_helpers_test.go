package graphql_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/testhelpers"
)

// Helpers shared by the ordering tests (order_translations_test.go, order_flow_test.go,
// order_schedule_test.go): requests with an Accept-Language, createOrder / quoteOrder wrappers that
// keep the typed error, and direct reads of what was stored.

// gqlAs posts a query as the token's user with the given Accept-Language ("" sends no header).
func gqlAs(t *testing.T, tc *TestContext, token, lang, query string, vars map[string]any) graphqlResponseWithExtensions {
	t.Helper()
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, tc.Client.URL(), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out graphqlResponseWithExtensions
	require.NoError(t, json.Unmarshal(raw, &out), "response body: %s", string(raw))
	return out
}

// createdOrder is what createOrder returns, as much as the ordering tests look at.
type createdOrder struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	Type            string  `json:"type"`
	IsOnlinePayment bool    `json:"isOnlinePayment"`
	TotalPrice      string  `json:"totalPrice"`
	DiscountAmount  string  `json:"discountAmount"`
	DeliveryFee     *string `json:"deliveryFee"`
	TransactionFee  *string `json:"transactionFee"`
	CouponCode      *string `json:"couponCode"`
	CashPayment     *string `json:"cashPaymentAmount"`
	Payment         *struct {
		MolliePaymentID string          `json:"molliePaymentId"`
		Status          string          `json:"status"`
		Links           json.RawMessage `json:"links"`
	} `json:"payment"`
	Items []orderItemView `json:"items"`
}

type orderItemView struct {
	ProductID  string `json:"productID"`
	Quantity   int    `json:"quantity"`
	UnitPrice  string `json:"unitPrice"`
	TotalPrice string `json:"totalPrice"`
	Product    struct {
		Name     string `json:"name"`
		Category struct {
			Name string `json:"name"`
		} `json:"category"`
	} `json:"product"`
	Selections []struct {
		GroupID  string `json:"groupId"`
		ChoiceID string `json:"choiceId"`
		Quantity int    `json:"quantity"`
	} `json:"selections"`
}

const orderItemsFragment = `
	items {
		productID quantity unitPrice totalPrice
		product { name category { name } }
		selections { groupId choiceId quantity }
	}`

const createOrderFullMutation = `
	mutation ($input: CreateOrderInput!) {
		createOrder(input: $input) {
			id status type isOnlinePayment totalPrice discountAmount deliveryFee transactionFee couponCode cashPaymentAmount
			payment { molliePaymentId status links }
			` + orderItemsFragment + `
		}
	}`

// orderErr is the first error of a response with the fields the tests assert.
type orderErr struct {
	Message    string
	Extensions map[string]any
}

func (e orderErr) Code() string {
	code, _ := e.Extensions["code"].(string)
	return code
}

// createOrderAs runs createOrder; exactly one of the two results is set.
func createOrderAs(t *testing.T, tc *TestContext, token, lang string, input map[string]any) (*createdOrder, *orderErr) {
	t.Helper()
	resp := gqlAs(t, tc, token, lang, createOrderFullMutation, map[string]any{"input": input})
	if len(resp.Errors) > 0 {
		return nil, &orderErr{Message: resp.Errors[0].Message, Extensions: resp.Errors[0].Extensions}
	}
	var data struct {
		CreateOrder createdOrder `json:"createOrder"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data), "createOrder data: %s", resp.Data)
	return &data.CreateOrder, nil
}

// mustCreateOrder runs createOrder and fails the test on any error.
func mustCreateOrder(t *testing.T, tc *TestContext, token, lang string, input map[string]any) *createdOrder {
	t.Helper()
	order, orderErr := createOrderAs(t, tc, token, lang, input)
	require.Nil(t, orderErr, "createOrder failed: %+v", orderErr)
	return order
}

// requireOrderError asserts createOrder failed with the code, that the code is one the presenter
// treats as the customer's doing (no Sentry), and returns the error for parameter assertions.
func requireOrderError(t *testing.T, got *createdOrder, orderErr *orderErr, code apperr.Code) *orderErr {
	t.Helper()
	require.Nil(t, got, "expected %s, but the order was created", code)
	require.NotNil(t, orderErr, "expected %s", code)
	require.Equal(t, string(code), orderErr.Code(), "message: %s ext: %v", orderErr.Message, orderErr.Extensions)
	assert.True(t, apperr.IsExpected(code), "%s is a customer error: it must not reach Sentry", code)
	return orderErr
}

// quoteAs runs quoteOrder (the issues are part of the result, never GraphQL errors).
func quoteAs(t *testing.T, tc *TestContext, token, lang string, input map[string]any) quoteResult {
	t.Helper()
	resp := gqlAs(t, tc, token, lang, quoteOrderQuery, map[string]any{"input": input})
	require.Empty(t, resp.Errors, "quoteOrder returned GraphQL errors: %+v", resp.Errors)
	var data struct {
		QuoteOrder quoteResult `json:"quoteOrder"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.QuoteOrder
}

// quoteCodes lists every issue code of a quote: order-level first, then the lines'.
func quoteCodes(q quoteResult) []string {
	var codes []string
	for _, issue := range q.Issues {
		codes = append(codes, issue.Code)
	}
	for _, line := range q.Lines {
		for _, issue := range line.Issues {
			codes = append(codes, issue.Code)
		}
	}
	return codes
}

// storedOrder is the order as the database holds it.
type storedOrder struct {
	TotalPrice       string  `db:"total_price"`
	TakeawayDiscount string  `db:"takeaway_discount"`
	CouponDiscount   string  `db:"coupon_discount"`
	DeliveryFee      *string `db:"delivery_fee"`
	TransactionFee   string  `db:"transaction_fee"`
	Language         string  `db:"language"`
	OrderType        string  `db:"order_type"`
	OrderStatus      string  `db:"order_status"`
	IsOnlinePayment  bool    `db:"is_online_payment"`
	CouponCode       *string `db:"coupon_code"`
	CashPayment      *string `db:"cash_payment_amount"`
	Postcode         *string `db:"postcode"`
	PreferredReady   *string `db:"preferred_ready_time"`
}

func loadStoredOrder(t *testing.T, tc *TestContext, orderID string) storedOrder {
	t.Helper()
	var o storedOrder
	require.NoError(t, tc.DB.DB.GetContext(t.Context(), &o, `
		SELECT total_price::text, takeaway_discount::text, coupon_discount::text, delivery_fee::text,
		       transaction_fee::text, language, order_type::text, order_status::text, is_online_payment,
		       coupon_code, cash_payment_amount::text, postcode, preferred_ready_time::text
		FROM orders WHERE id = $1`, orderID))
	return o
}

func countRows(t *testing.T, tc *TestContext, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, tc.DB.DB.GetContext(t.Context(), &n, query, args...))
	return n
}

// lines builds the items input from (productId, quantity) pairs.
func lines(pairs ...any) []map[string]any {
	var items []map[string]any
	for i := 0; i < len(pairs); i += 2 {
		id := pairs[i]
		if u, ok := id.(uuid.UUID); ok {
			id = u.String()
		}
		items = append(items, map[string]any{"productId": id, "quantity": pairs[i+1]})
	}
	return items
}

func adminToken(t *testing.T, tc *TestContext) string {
	t.Helper()
	token, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	return token
}
