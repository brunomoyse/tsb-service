package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/testhelpers"
)

// An app-only coupon (the app launch promo) is redeemable from the iOS/Android app only. The app is
// recognised by the OIDC client its access token was issued to (the test harness mirrors the auth
// middleware with testhelpers.TestAppClientID), never by anything the client sends.
func TestAppOnlyCoupon(t *testing.T) {
	tc := setupTestContext(t)
	url := tc.Client.URL()

	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	customer, webToken := testhelpers.SeedCustomer(t, tc.DB.DB, "app-only")
	appToken, err := testhelpers.GenerateTestAppAccessToken(customer.String())
	require.NoError(t, err)

	// The launch promo: 5 EUR off from 25 EUR, once per account, app only.
	_, created := postGraphQL(t, url, graphqlRequest{
		Query: `mutation ($input: CreateCouponInput!) { createCoupon(input: $input) { id appOnly } }`,
		Variables: map[string]any{"input": map[string]any{
			"code": "SAKURA5", "discountType": "FIXED", "discountValue": "5", "minOrderAmount": "25",
			"maxUsesPerUser": 1, "isActive": true, "appOnly": true,
		}},
	}, adminToken)
	require.Empty(t, created.Errors, "coupon setup: %v", created.Errors)
	var createdData struct {
		CreateCoupon struct {
			ID      string `json:"id"`
			AppOnly bool   `json:"appOnly"`
		} `json:"createCoupon"`
	}
	require.NoError(t, json.Unmarshal(created.Data, &createdData))
	assert.True(t, createdData.CreateCoupon.AppOnly)

	salmon := tc.Fixtures.SalmonSushi.ID.String() // 12.50 EUR
	items := []map[string]any{{"productId": salmon, "quantity": 3}}
	withCoupon := map[string]any{"couponCode": "SAKURA5"}

	const validate = `query ($code: String!, $amount: String!) { validateCoupon(code: $code, orderAmount: $amount) { valid discountAmount errorMessage errorCode } }`
	type validation struct {
		Valid          bool    `json:"valid"`
		DiscountAmount string  `json:"discountAmount"`
		ErrorMessage   *string `json:"errorMessage"`
		ErrorCode      *string `json:"errorCode"`
	}
	validateAs := func(t *testing.T, token, amount string) validation {
		t.Helper()
		resp := gqlAs(t, tc, token, "fr", validate, map[string]any{"code": "sakura5", "amount": amount})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var data struct {
			ValidateCoupon validation `json:"validateCoupon"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		return data.ValidateCoupon
	}
	failedAttempts := func(t *testing.T) int {
		t.Helper()
		return countRows(t, tc, `SELECT COALESCE(SUM(count), 0) FROM coupon_validation_attempts WHERE user_id = $1`, customer)
	}

	t.Run("the dashboard lists the flag", func(t *testing.T) {
		_, resp := postGraphQL(t, url, graphqlRequest{Query: `{ coupons { code appOnly } }`}, adminToken)
		require.Empty(t, resp.Errors)
		var data struct {
			Coupons []struct {
				Code    string `json:"code"`
				AppOnly bool   `json:"appOnly"`
			} `json:"coupons"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		require.Len(t, data.Coupons, 1)
		assert.Equal(t, "SAKURA5", data.Coupons[0].Code)
		assert.True(t, data.Coupons[0].AppOnly)
	})

	t.Run("on the website the code is refused with a specific code, without burning an attempt", func(t *testing.T) {
		for range 6 { // more than the 5 daily failed attempts
			got := validateAs(t, webToken, "40.00")
			assert.False(t, got.Valid)
			assert.Equal(t, "0", got.DiscountAmount)
			require.NotNil(t, got.ErrorCode)
			assert.Equal(t, string(apperr.CodeCouponAppOnly), *got.ErrorCode)
			require.NotNil(t, got.ErrorMessage)
			assert.Equal(t, "this coupon can only be used in the app", *got.ErrorMessage)
		}
		assert.Zero(t, failedAttempts(t), "an app-only refusal is not a guess")
	})

	t.Run("on the website app-only wins over the minimum", func(t *testing.T) {
		got := validateAs(t, webToken, "10.00")
		require.NotNil(t, got.ErrorCode)
		assert.Equal(t, string(apperr.CodeCouponAppOnly), *got.ErrorCode)
	})

	t.Run("in the app the code validates, and its minimum still applies", func(t *testing.T) {
		got := validateAs(t, appToken, "40.00")
		assert.True(t, got.Valid, "error: %v", got.ErrorCode)
		assert.Equal(t, "5", got.DiscountAmount)

		low := validateAs(t, appToken, "10.00")
		assert.False(t, low.Valid)
		require.NotNil(t, low.ErrorCode)
		assert.Equal(t, string(apperr.CodeCouponMinOrderNotMet), *low.ErrorCode)
	})

	t.Run("the website quote reports it as an issue, the app quote applies it", func(t *testing.T) {
		web := quoteOrder(t, tc, webToken, quoteInput("PICKUP", items, withCoupon))
		assert.Equal(t, []string{string(apperr.CodeCouponAppOnly)}, quoteCodes(web))
		require.NotNil(t, web.Coupon)
		assert.False(t, web.Coupon.Valid)
		assert.Equal(t, "0.00", web.CouponDiscount)

		app := quoteOrder(t, tc, appToken, quoteInput("PICKUP", items, withCoupon))
		assert.Empty(t, quoteCodes(app))
		require.NotNil(t, app.Coupon)
		assert.True(t, app.Coupon.Valid)
		assert.Equal(t, "5.00", app.CouponDiscount)
	})

	t.Run("createOrder from the website is refused, from the app it redeems the coupon", func(t *testing.T) {
		got, oerr := createOrderAs(t, tc, webToken, "fr", createOrderInput("PICKUP", items, withCoupon))
		requireOrderError(t, got, oerr, apperr.CodeCouponAppOnly)
		assert.Zero(t, countRows(t, tc, `SELECT COUNT(*) FROM coupon_users WHERE user_id = $1`, customer), "nothing was reserved")

		order := mustCreateOrder(t, tc, appToken, "fr", createOrderInput("PICKUP", items, withCoupon))
		require.NotNil(t, order.CouponCode)
		assert.Equal(t, "SAKURA5", *order.CouponCode)
		assert.Equal(t, "5.00", loadStoredOrder(t, tc, order.ID).CouponDiscount)
		assert.Zero(t, failedAttempts(t))
	})

	t.Run("the flag can be switched off, and the code then works on the website", func(t *testing.T) {
		_, resp := postGraphQL(t, url, graphqlRequest{
			Query:     `mutation ($id: ID!, $input: UpdateCouponInput!) { updateCoupon(id: $id, input: $input) { appOnly } }`,
			Variables: map[string]any{"id": createdData.CreateCoupon.ID, "input": map[string]any{"appOnly": false, "maxUsesPerUser": 5}},
		}, adminToken)
		require.Empty(t, resp.Errors, "%v", resp.Errors)
		assert.JSONEq(t, `{"updateCoupon":{"appOnly":false}}`, string(resp.Data))

		got := validateAs(t, webToken, "40.00")
		assert.True(t, got.Valid, "error: %v", got.ErrorCode)
	})
}
