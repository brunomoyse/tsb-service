package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// The checkout errors carry a stable `extensions.code` (apperr): tsb-core and tsb-mobile translate
// the code, never the English message. These tests pin the codes through the real error presenter.

const createOrderMutation = `
	mutation ($input: CreateOrderInput!) {
		createOrder(input: $input) { id }
	}`

func createOrderInput(orderType string, items []map[string]any, extra map[string]any) map[string]any {
	input := map[string]any{
		"orderType":       orderType,
		"isOnlinePayment": false,
		"items":           items,
	}
	for k, v := range extra {
		input[k] = v
	}
	return input
}

func TestCreateOrderErrorCodes(t *testing.T) {
	tc := setupTestContext(t)
	url := tc.Client.URL()

	customerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)
	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)

	// A coupon every test user can validate: 10 off, but only from 30 EUR.
	_, couponResp := postGraphQL(t, url, graphqlRequest{
		Query: `mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id } }`,
		Variables: map[string]any{"input": map[string]any{
			"code": "CODES10", "discountType": "FIXED", "discountValue": "10", "minOrderAmount": "30", "isActive": true,
		}},
	}, adminToken)
	require.Empty(t, couponResp.Errors, "coupon setup: %v", couponResp.Errors)

	salmon := tc.Fixtures.SalmonSushi.ID.String() // 12.50 EUR

	// firstError sends the mutation and returns the single error with its extensions.
	firstError := func(t *testing.T, input map[string]any) (string, map[string]any) {
		t.Helper()
		resp := postGraphQLWithExtensions(t, url, graphqlRequest{
			Query:     createOrderMutation,
			Variables: map[string]any{"input": input},
		}, customerToken)
		require.NotEmpty(t, resp.Errors, "expected an error, got data: %s", resp.Data)
		require.NotNil(t, resp.Errors[0].Extensions, "error without extensions: %+v", resp.Errors[0])
		return resp.Errors[0].Message, resp.Errors[0].Extensions
	}

	t.Run("empty order", func(t *testing.T) {
		msg, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{}, nil))
		assert.Equal(t, "ORDER_EMPTY", ext["code"])
		assert.Equal(t, "order must contain at least one item", msg, "English message is kept for logs and old clients")
	})

	t.Run("product not found carries the product id", func(t *testing.T) {
		missing := uuid.New().String()
		msg, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": missing, "quantity": 1}}, nil))
		assert.Equal(t, "PRODUCT_NOT_FOUND", ext["code"])
		assert.Equal(t, missing, ext["productId"])
		assert.Contains(t, msg, "not found")
	})

	t.Run("invalid quantity", func(t *testing.T) {
		_, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 100}}, nil))
		assert.Equal(t, "INVALID_QUANTITY", ext["code"])
		assert.Equal(t, salmon, ext["productId"])
	})

	t.Run("choice that does not exist is an invalid selection", func(t *testing.T) {
		_, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{
			{"productId": salmon, "quantity": 1, "choiceId": uuid.New().String()},
		}, nil))
		assert.Equal(t, "SELECTION_INVALID", ext["code"])
	})

	t.Run("delivery below the minimum", func(t *testing.T) {
		msg, ext := firstError(t, createOrderInput("DELIVERY", []map[string]any{{"productId": salmon, "quantity": 1}}, nil))
		assert.Equal(t, "DELIVERY_MINIMUM_NOT_MET", ext["code"])
		assert.Equal(t, "25", ext["minimum"])
		assert.Equal(t, "minimum order amount for delivery is 25", msg)
	})

	t.Run("delivery without an address", func(t *testing.T) {
		_, ext := firstError(t, createOrderInput("DELIVERY", []map[string]any{{"productId": salmon, "quantity": 2}}, nil))
		assert.Equal(t, "ADDRESS_REQUIRED", ext["code"])
	})

	t.Run("invalid cash amount", func(t *testing.T) {
		_, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 1}},
			map[string]any{"cashPaymentAmount": "-5"}))
		assert.Equal(t, "CASH_AMOUNT_INVALID", ext["code"])
	})

	t.Run("unknown coupon", func(t *testing.T) {
		msg, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 4}},
			map[string]any{"couponCode": "NOPE-000"}))
		assert.Equal(t, "COUPON_INVALID", ext["code"])
		assert.Contains(t, msg, "invalid coupon")
	})

	t.Run("coupon minimum not met", func(t *testing.T) {
		_, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 1}},
			map[string]any{"couponCode": "CODES10"}))
		assert.Equal(t, "COUPON_MIN_ORDER_NOT_MET", ext["code"])
	})

	t.Run("another active coupon order", func(t *testing.T) {
		insertCouponOrder(t, tc, tc.Fixtures.RegularUser.ID, "OTHER", "PENDING")
		msg, ext := firstError(t, createOrderInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 4}},
			map[string]any{"couponCode": "CODES10"}))
		assert.Equal(t, "COUPON_ALREADY_ACTIVE", ext["code"])
		assert.Equal(t, "you already have an active order using a coupon", msg)
	})
}

func TestValidateCouponErrorCode(t *testing.T) {
	tc := setupTestContext(t)
	url := tc.Client.URL()

	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	customerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)

	_, couponResp := postGraphQL(t, url, graphqlRequest{
		Query: `mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id } }`,
		Variables: map[string]any{"input": map[string]any{
			"code": "MINORDER", "discountType": "FIXED", "discountValue": "5", "minOrderAmount": "30", "isActive": true,
		}},
	}, adminToken)
	require.Empty(t, couponResp.Errors, "coupon setup: %v", couponResp.Errors)

	type validation struct {
		Valid        bool    `json:"valid"`
		ErrorMessage *string `json:"errorMessage"`
		ErrorCode    *string `json:"errorCode"`
	}
	validate := func(code, amount string) validation {
		_, resp := postGraphQL(t, url, graphqlRequest{
			Query: `query ($code: String!, $orderAmount: String!) {
				validateCoupon(code: $code, orderAmount: $orderAmount) { valid errorMessage errorCode }
			}`,
			Variables: map[string]any{"code": code, "orderAmount": amount},
		}, customerToken)
		require.Empty(t, resp.Errors, "unexpected GraphQL errors: %v", resp.Errors)
		var data struct {
			ValidateCoupon validation `json:"validateCoupon"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		return data.ValidateCoupon
	}

	t.Run("valid coupon has no error code", func(t *testing.T) {
		res := validate("MINORDER", "40")
		assert.True(t, res.Valid)
		assert.Nil(t, res.ErrorCode)
	})

	t.Run("minimum not met", func(t *testing.T) {
		res := validate("MINORDER", "10")
		assert.False(t, res.Valid)
		require.NotNil(t, res.ErrorCode)
		assert.Equal(t, "COUPON_MIN_ORDER_NOT_MET", *res.ErrorCode)
		require.NotNil(t, res.ErrorMessage, "the English message stays for old clients")
	})

	t.Run("unknown coupon stays generic until the daily limit", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			res := validate("NOPE-000", "40")
			require.NotNil(t, res.ErrorCode)
			assert.Equal(t, "COUPON_INVALID", *res.ErrorCode, "attempt %d", i+1)
		}
		res := validate("NOPE-000", "40")
		require.NotNil(t, res.ErrorCode)
		assert.Equal(t, "COUPON_RATE_LIMITED", *res.ErrorCode)
	})
}
