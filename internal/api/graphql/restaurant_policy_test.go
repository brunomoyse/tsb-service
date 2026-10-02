package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RestaurantConfig.policy exposes the ordering rules the pricer enforces, publicly and read-only.

const restaurantPolicyQuery = `
	query {
		restaurantConfig {
			preparationMinutes
			policy {
				deliveryEnabled deliveryMinimum deliveryMaxDistanceKm
				deliveryFeeTiers { upToKm fee }
				excludedPostcodes
				pickupDiscountRate pickupDiscountMinimum
				onlinePaymentFee totalRoundingStep
				slotIntervalMinutes minimumPreparationMinutes
			}
		}
	}`

func TestRestaurantConfigPolicy(t *testing.T) {
	tc := setupTestContext(t)

	// Public: no token.
	resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{Query: restaurantPolicyQuery}, "")
	require.Empty(t, resp.Errors, "restaurantConfig.policy: %+v", resp.Errors)

	var data struct {
		RestaurantConfig struct {
			Policy struct {
				DeliveryEnabled       bool    `json:"deliveryEnabled"`
				DeliveryMinimum       string  `json:"deliveryMinimum"`
				DeliveryMaxDistanceKm float64 `json:"deliveryMaxDistanceKm"`
				DeliveryFeeTiers      []struct {
					UpToKm float64 `json:"upToKm"`
					Fee    string  `json:"fee"`
				} `json:"deliveryFeeTiers"`
				ExcludedPostcodes         []string `json:"excludedPostcodes"`
				PickupDiscountRate        float64  `json:"pickupDiscountRate"`
				PickupDiscountMinimum     string   `json:"pickupDiscountMinimum"`
				OnlinePaymentFee          string   `json:"onlinePaymentFee"`
				TotalRoundingStep         string   `json:"totalRoundingStep"`
				SlotIntervalMinutes       int      `json:"slotIntervalMinutes"`
				MinimumPreparationMinutes int      `json:"minimumPreparationMinutes"`
			} `json:"policy"`
		} `json:"restaurantConfig"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	p := data.RestaurantConfig.Policy

	assert.True(t, p.DeliveryEnabled)
	assert.Equal(t, "25.00", p.DeliveryMinimum)
	assert.InDelta(t, 9.0, p.DeliveryMaxDistanceKm, 0)
	assert.Equal(t, []string{"4610"}, p.ExcludedPostcodes)
	assert.InDelta(t, 0.10, p.PickupDiscountRate, 0)
	assert.Equal(t, "20.00", p.PickupDiscountMinimum)
	assert.Equal(t, "0.30", p.OnlinePaymentFee)
	assert.Equal(t, "0.10", p.TotalRoundingStep)
	assert.Equal(t, 15, p.SlotIntervalMinutes)
	assert.Equal(t, 15, p.MinimumPreparationMinutes)

	wantKm := []float64{3, 4, 5, 6, 7, 8, 9}
	wantFee := []string{"0.00", "1.00", "2.00", "3.00", "4.00", "5.00", "6.00"}
	require.Len(t, p.DeliveryFeeTiers, len(wantKm))
	for i, tier := range p.DeliveryFeeTiers {
		assert.InDelta(t, wantKm[i], tier.UpToKm, 0, "tier %d", i)
		assert.Equal(t, wantFee[i], tier.Fee, "tier %d", i)
	}
}

// The quote charges the numbers the policy publishes.
func TestRestaurantConfigPolicyMatchesTheQuote(t *testing.T) {
	tc := setupTestContext(t)
	salmon := tc.Fixtures.SalmonSushi.ID.String() // 12.50 EUR, discountable
	basket := []map[string]any{{"productId": salmon, "quantity": 2}}

	q := quoteOrder(t, tc, "", quoteInput("PICKUP", basket, map[string]any{"isOnlinePayment": true}))
	assert.Equal(t, "0.30", q.OnlineFee)      // policy.onlinePaymentFee
	assert.Equal(t, "2.50", q.PickupDiscount) // policy.pickupDiscountRate × 25.00, basket over policy.pickupDiscountMinimum

	under := quoteOrder(t, tc, "", quoteInput("PICKUP", []map[string]any{{"productId": salmon, "quantity": 1}}, nil))
	assert.Equal(t, "0.00", under.PickupDiscount) // 12.50 < policy.pickupDiscountMinimum
}
