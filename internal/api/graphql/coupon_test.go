package graphql_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// graphqlRequest is the JSON body sent to the GraphQL endpoint.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphqlResponse is the JSON response from the GraphQL endpoint.
type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string   `json:"message"`
		Path    []string `json:"path"`
	} `json:"errors"`
}

// postGraphQL sends a real HTTP POST to the test server and returns the parsed response.
func postGraphQL(t *testing.T, url string, reqBody graphqlRequest, token string) (*http.Response, graphqlResponse) {
	t.Helper()

	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var gqlResp graphqlResponse
	err = json.Unmarshal(respBody, &gqlResp)
	require.NoError(t, err, "response body: %s", string(respBody))

	return resp, gqlResp
}

func TestCreateCouponIntegration(t *testing.T) {
	tc := setupTestContext(t)
	url := tc.Client.URL()

	adminToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)

	regularToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)

	const createCouponMutation = `
		mutation ($input: CreateCouponInput!) {
			createCoupon(input: $input) {
				id
				code
				discountType
				discountValue
				minOrderAmount
				maxUses
				usedCount
				isActive
				createdAt
			}
		}`

	t.Run("create fixed coupon with uppercase type", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: createCouponMutation,
			Variables: map[string]any{
				"input": map[string]any{
					"code":           "TOKYO10",
					"discountType":   "FIXED",
					"discountValue":  "10",
					"minOrderAmount": "30",
					"maxUses":        nil,
					"isActive":       true,
					"validFrom":      nil,
					"validUntil":     nil,
				},
			},
		}, adminToken)

		require.Empty(t, gqlResp.Errors, "unexpected errors: %v", gqlResp.Errors)

		var data struct {
			CreateCoupon struct {
				ID             string  `json:"id"`
				Code           string  `json:"code"`
				DiscountType   string  `json:"discountType"`
				DiscountValue  string  `json:"discountValue"`
				MinOrderAmount *string `json:"minOrderAmount"`
				MaxUses        *int    `json:"maxUses"`
				UsedCount      int     `json:"usedCount"`
				IsActive       bool    `json:"isActive"`
				CreatedAt      string  `json:"createdAt"`
			} `json:"createCoupon"`
		}
		require.NoError(t, json.Unmarshal(gqlResp.Data, &data))

		c := data.CreateCoupon
		assert.NotEmpty(t, c.ID)
		assert.Equal(t, "TOKYO10", c.Code)
		assert.Equal(t, "FIXED", c.DiscountType)
		assert.Equal(t, "10", c.DiscountValue)
		assert.NotNil(t, c.MinOrderAmount)
		assert.Equal(t, "30", *c.MinOrderAmount)
		assert.Nil(t, c.MaxUses)
		assert.Equal(t, 0, c.UsedCount)
		assert.True(t, c.IsActive)
		assert.NotEmpty(t, c.CreatedAt, "createdAt must not be empty")
	})

	t.Run("create percentage coupon with lowercase type returns uppercase", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: createCouponMutation,
			Variables: map[string]any{
				"input": map[string]any{
					"code":          "SUMMER20",
					"discountType":  "percentage",
					"discountValue": "20",
					"maxUses":       100,
					"isActive":      true,
				},
			},
		}, adminToken)

		require.Empty(t, gqlResp.Errors)

		var data struct {
			CreateCoupon struct {
				DiscountType  string `json:"discountType"`
				DiscountValue string `json:"discountValue"`
				MaxUses       *int   `json:"maxUses"`
				CreatedAt     string `json:"createdAt"`
			} `json:"createCoupon"`
		}
		require.NoError(t, json.Unmarshal(gqlResp.Data, &data))

		assert.Equal(t, "PERCENTAGE", data.CreateCoupon.DiscountType)
		assert.Equal(t, "20", data.CreateCoupon.DiscountValue)
		assert.NotNil(t, data.CreateCoupon.MaxUses)
		assert.Equal(t, 100, *data.CreateCoupon.MaxUses)
		assert.NotEmpty(t, data.CreateCoupon.CreatedAt)
	})

	t.Run("invalid discount type returns error", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: createCouponMutation,
			Variables: map[string]any{
				"input": map[string]any{
					"code":          "BAD",
					"discountType":  "INVALID",
					"discountValue": "10",
					"isActive":      true,
				},
			},
		}, adminToken)

		require.NotEmpty(t, gqlResp.Errors)
		assert.Contains(t, gqlResp.Errors[0].Message, "invalid discount type")
	})

	t.Run("non-admin user is forbidden", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: createCouponMutation,
			Variables: map[string]any{
				"input": map[string]any{
					"code":          "NOPE",
					"discountType":  "FIXED",
					"discountValue": "5",
					"isActive":      true,
				},
			},
		}, regularToken)

		require.NotEmpty(t, gqlResp.Errors)
		assert.Contains(t, gqlResp.Errors[0].Message, "FORBIDDEN")
	})

	t.Run("percentage over 100 returns error", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: createCouponMutation,
			Variables: map[string]any{
				"input": map[string]any{
					"code":          "TOOMUCH",
					"discountType":  "PERCENTAGE",
					"discountValue": "150",
					"isActive":      true,
				},
			},
		}, adminToken)

		require.NotEmpty(t, gqlResp.Errors)
		assert.Contains(t, gqlResp.Errors[0].Message, "percentage discount cannot exceed 100")
	})

	t.Run("list coupons returns created coupons", func(t *testing.T) {
		_, gqlResp := postGraphQL(t, url, graphqlRequest{
			Query: `query { coupons { id code discountType } }`,
		}, adminToken)

		require.Empty(t, gqlResp.Errors)

		var data struct {
			Coupons []struct {
				ID           string `json:"id"`
				Code         string `json:"code"`
				DiscountType string `json:"discountType"`
			} `json:"coupons"`
		}
		require.NoError(t, json.Unmarshal(gqlResp.Data, &data))

		assert.GreaterOrEqual(t, len(data.Coupons), 2)

		codes := make(map[string]string)
		for _, c := range data.Coupons {
			codes[c.Code] = c.DiscountType
		}
		assert.Equal(t, "FIXED", codes["TOKYO10"])
		assert.Equal(t, "PERCENTAGE", codes["SUMMER20"])
	})
}
